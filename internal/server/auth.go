package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/auth"
	"github.com/harpchad/td/internal/oauth"
	"github.com/harpchad/td/internal/store"
)

// SessionCookie is the name of the browser session cookie.
const SessionCookie = "td_session"

// loginRateLimit and loginRateWindow implement the app-level limit from
// section 15. It is at the app rather than at the proxy on purpose: the proxy
// is not the only way in, and a limit you cannot test is not a limit.
const (
	loginRateLimit  = 10
	loginRateWindow = time.Minute
)

// principal is who is making a request, once authenticated.
type principal struct {
	// Actor is what a mutation writes to the event log: me, mcp:<name>, or
	// plugin:<source>.
	Actor  string
	Scopes []string
	// Kind is "session" or "token".
	Kind string
	// TokenName is set for a token principal, for logging.
	TokenName string
}

func (p *principal) has(scope string) bool {
	for _, s := range p.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

type principalKey struct{}

func withPrincipal(ctx context.Context, p *principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

func principalOf(ctx context.Context) *principal {
	p, _ := ctx.Value(principalKey{}).(*principal)
	return p
}

// sessionScopes is what a logged-in browser carries. The account holder at a
// keyboard can do anything; scopes exist to constrain the credentials that
// get pasted into other programs.
var sessionScopes = []string{api.ScopeRead, api.ScopeCapture, api.ScopeWrite}

// requireAccount answers 503 until `tdd account create` has been run.
//
// Account creation is a command on the server and is never offered over HTTP,
// so an unconfigured server has nothing useful to say to any request. The
// health check is exempt: it reports whether the process is alive, and a
// liveness probe that fails on a live process is a broken probe.
func (s *Server) requireAccount(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		ok, err := s.store.HasAccount(r.Context())
		if err != nil {
			s.fail(w, err)
			return
		}
		if !ok {
			writeJSON(w, http.StatusServiceUnavailable, &api.Error{
				Code:    api.ErrNoAccount,
				Message: "no account configured. Run `tdd account create` on the server.",
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// publicPaths are the only routes an unauthenticated request reaches. Every
// other route, present or future, requires a credential: the list is a
// allowlist rather than a set of exceptions checked per handler, so adding a
// route cannot accidentally add a hole.
func isPublicPath(path string) bool {
	switch path {
	case "/healthz", "/login", "/logout":
		return true
	case PRMPath, ASMetadataPath, JWKSPath:
		// Discovery documents. A client reads them precisely because it has
		// no credential yet, so requiring one would make discovery
		// impossible.
		return true
	case TokenPath, RegisterPath, RevokePath:
		// The OAuth endpoints authenticate the client themselves, by secret
		// or by PKCE. A session cookie has nothing to do with it.
		//
		// /register is client registration, not user registration: it creates
		// no account and grants no access. A registered client still has to
		// send its user through /authorize.
		return true
	}
	// The stylesheet and the two scripts are needed to render the login page
	// itself, so they sit outside the credential check. They carry no data.
	return strings.HasPrefix(path, "/static/")
}

// isBrowserPath reports whether a path belongs to the server-rendered UI
// rather than the API. The two answer an unauthenticated request very
// differently: the API answers 401 with nothing at all, and a browser gets
// sent to the login page, which is the only route that renders anything.
func isBrowserPath(path string) bool {
	switch {
	case path == "/", path == "/settings", path == "/help":
		return true
	case path == AuthorizePath, path == "/triage":
		return true
	case strings.HasPrefix(path, "/t/"), strings.HasPrefix(path, "/w/"), strings.HasPrefix(path, "/p/"):
		return true
	}
	return false
}

// authenticate resolves a credential onto a principal, or refuses.
//
// /api/v1/* answers 401 with an empty body: no error code, no message, and
// nothing that distinguishes a bad token from a missing one.
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isPublicPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		// A token in a query string ends up in access logs, browser history,
		// and Referer headers. There is no endpoint that accepts one.
		if hasTokenInQuery(r) {
			s.denyEmpty(w, r, "token in query string")
			return
		}

		p, err := s.resolveCredential(r)
		if err != nil || p == nil {
			if r.URL.Path == MCPPath {
				// The WWW-Authenticate header is the whole of MCP client
				// discovery. Without it the client never finds the
				// authorization server, and the symptom is this endpoint
				// seeing traffic while the AS sees none.
				s.mcpUnauthorized(w, r, api.MCPScopeRead)
				return
			}
			if isBrowserPath(r.URL.Path) && wantsHTML(r) {
				s.logAuth(r, api.KindAuthDenied, "anonymous", map[string]any{
					"path": r.URL.Path, "reason": "no session",
				})
				http.Redirect(w, r, "/login", http.StatusSeeOther)
				return
			}
			s.denyEmpty(w, r, "no valid credential")
			return
		}

		if scope := requiredScope(r); scope != "" && !p.has(scope) {
			if r.URL.Path == MCPPath {
				// 403 with insufficient_scope, not 401: the credential is
				// valid and the client needs more scope, not a new grant.
				s.logAuth(r, api.KindAuthDenied, p.Actor, map[string]any{
					"path": r.URL.Path, "scope": scope,
				})
				s.insufficientScope(w, api.ScopeToMCP(scope))
				return
			}
			s.logAuth(r, api.KindAuthDenied, p.Actor, map[string]any{
				"path": r.URL.Path, "scope": scope,
			})
			writeJSON(w, http.StatusForbidden, &api.Error{
				Code:    api.ErrForbidden,
				Message: "this token does not carry the " + scope + " scope",
			})
			return
		}

		next.ServeHTTP(w, r.WithContext(withPrincipal(r.Context(), p)))
	})
}

// denyEmpty answers 401 with no body at all. The login page is the only route
// that renders anything to an unauthenticated request.
func (s *Server) denyEmpty(w http.ResponseWriter, r *http.Request, reason string) {
	s.logAuth(r, api.KindAuthDenied, "anonymous", map[string]any{
		"path": r.URL.Path, "reason": reason,
	})
	w.WriteHeader(http.StatusUnauthorized)
}

func (s *Server) resolveCredential(r *http.Request) (*principal, error) {
	now := s.Now()

	if secret, ok := bearerToken(r); ok {
		// Two shapes of bearer, told apart by shape rather than by trying
		// both: a td_ token is opaque and a JWT has three dot-separated
		// parts. Guessing would mean a failed database lookup on every OAuth
		// request and a failed signature check on every token one.
		if strings.Count(secret, ".") == 2 {
			return s.principalFromJWT(r, secret, now)
		}
		tok, err := s.store.LookupToken(r.Context(), secret, now)
		if err != nil {
			return nil, err
		}
		return &principal{
			Actor: tok.Actor, Scopes: tok.Scopes, Kind: "token", TokenName: tok.Name,
		}, nil
	}

	cookie, err := r.Cookie(SessionCookie)
	if err != nil || cookie.Value == "" {
		return nil, errors.New("no credential")
	}
	if _, err := s.store.LookupSession(r.Context(), cookie.Value, now); err != nil {
		return nil, err
	}
	return &principal{Actor: "me", Scopes: sessionScopes, Kind: "session"}, nil
}

// principalFromJWT validates an OAuth access token.
//
// Signature, issuer, expiry, not-before, and audience are all checked in
// oauth.Verify. Audience is an exact match against this server's resource,
// which is what stops a token minted for another server from being replayed
// here, and it is the failure people actually hit.
func (s *Server) principalFromJWT(r *http.Request, token string, now time.Time) (*principal, error) {
	keys, err := s.store.SigningKeys(r.Context())
	if err != nil {
		return nil, err
	}
	claims, err := oauth.Verifier{
		Issuer: s.baseURL, Audience: s.ResourceURL(), Keys: keys,
	}.Verify(token, now)
	if err != nil {
		// The reason goes to the log rather than to the client: "expired" and
		// "wrong audience" send an operator to different places, and neither
		// is something an attacker should be told.
		s.log.Info("oauth token refused", "err", err, "path", r.URL.Path)
		return nil, err
	}

	// The OAuth scopes are namespaced, the internal ones are not. An unknown
	// scope maps to nothing rather than to read, so a token carrying junk
	// gets less rather than more.
	scopes := make([]string, 0, 3)
	for _, scope := range claims.Scopes() {
		if internal := api.ScopeFromMCP(scope); internal != "" {
			scopes = append(scopes, internal)
		}
	}

	// The actor is the client, so a bad agent batch stays separable from your
	// own work and one /undo loop away from gone.
	actor := "mcp:" + claims.ClientID
	if name, err := s.store.OAuthClientByID(r.Context(), claims.ClientID); err == nil && name.Name != "" {
		actor = "mcp:" + strings.ToLower(strings.Fields(name.Name)[0])
	}
	return &principal{
		Actor: actor, Scopes: scopes, Kind: "oauth", TokenName: claims.ClientID,
	}, nil
}

// wantsHTML reports whether the caller is a browser following a link, rather
// than something asking for JSON.
func wantsHTML(r *http.Request) bool {
	if r.Header.Get("HX-Request") == "true" {
		return true
	}
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

func bearerToken(r *http.Request) (string, bool) {
	header := r.Header.Get("Authorization")
	scheme, rest, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, "bearer") {
		return "", false
	}
	rest = strings.TrimSpace(rest)
	return rest, rest != ""
}

// tokenQueryKeys are the parameter names a client might reach for. Any of
// them present is a refusal rather than an ignored parameter, so the mistake
// surfaces at the caller instead of silently falling back to no credential.
var tokenQueryKeys = []string{"access_token", "token", "api_key", "apikey", "bearer", "auth"}

func hasTokenInQuery(r *http.Request) bool {
	q := r.URL.Query()
	for _, key := range tokenQueryKeys {
		if q.Has(key) {
			return true
		}
	}
	return false
}

// requiredScope maps a request onto the scope it needs. A read is a read; a
// capture is the narrow write that creates an inbox item; everything else
// that mutates needs write.
func requiredScope(r *http.Request) string {
	if strings.HasPrefix(r.URL.Path, "/api/v1/sync/") {
		source := strings.TrimPrefix(r.URL.Path, "/api/v1/sync/")
		return api.ScopeSyncPrefix + source
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		return api.ScopeRead
	case http.MethodPost:
		if r.URL.Path == "/api/v1/tasks" {
			return api.ScopeCapture
		}
		if r.URL.Path == MCPPath {
			// Read is the floor for reaching the protocol at all. Which tools
			// the credential may actually call is checked per tool, because a
			// client that can list the tools and is told which ones it may use
			// is a better failure than one that sees nothing.
			return api.ScopeRead
		}
		return api.ScopeWrite
	default:
		return api.ScopeWrite
	}
}

// login authenticates and issues a session cookie.
//
// Both factors arrive in one request. Section 15 requires the failures to be
// counted separately, which this does, and a two-step flow would add a
// pending-login state without changing any stated property.
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	now := s.Now()
	ip := s.clientIP(r)

	allowed, err := s.store.RateLimitLogin(r.Context(), ip, loginRateLimit, loginRateWindow, now)
	if err != nil {
		s.fail(w, err)
		return
	}
	if !allowed {
		s.logAuth(r, api.KindAuthRateLimited, "anonymous", nil)
		writeJSON(w, http.StatusTooManyRequests, &api.Error{
			Code:    api.ErrRateLimited,
			Message: "too many attempts, wait a minute",
		})
		return
	}

	req, err := decodeLogin(r)
	if err != nil {
		s.fail(w, err)
		return
	}

	acct, err := s.store.AccountByUsername(r.Context(), req.Username)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.fail(w, err)
			return
		}
		// An unknown username spends the same time as a known one. Skipping
		// the hash here is what turns a timing difference into an account
		// enumeration oracle.
		_ = auth.VerifyPassword(req.Password, s.dummyHash)
		s.logAuth(r, api.KindAuthLoginFailed, "anonymous", map[string]any{"stage": "username"})
		s.failLogin(w, r)
		return
	}

	if acct.Locked(now) {
		// Same answer as a wrong password, so probing cannot discover that an
		// account is locked, which would confirm it exists.
		s.logAuth(r, api.KindAuthLoginFailed, "anonymous", map[string]any{"stage": "locked"})
		s.failLogin(w, r)
		return
	}

	if err := auth.VerifyPassword(req.Password, acct.PasswordHash); err != nil {
		if _, err := s.store.RecordFailure(r.Context(), acct.ID, store.FailurePassword, now); err != nil {
			s.fail(w, err)
			return
		}
		s.logAuth(r, api.KindAuthLoginFailed, "anonymous", map[string]any{"stage": "password"})
		s.failLogin(w, r)
		return
	}

	if err := s.verifySecondFactor(r, acct, req, now); err != nil {
		if _, err := s.store.RecordFailure(r.Context(), acct.ID, store.FailureTOTP, now); err != nil {
			s.fail(w, err)
			return
		}
		s.logAuth(r, api.KindAuthLoginFailed, "anonymous", map[string]any{"stage": "totp"})
		s.failLogin(w, r)
		return
	}

	if err := s.store.ClearFailures(r.Context(), acct.ID); err != nil {
		s.fail(w, err)
		return
	}

	secret, sess, err := s.store.CreateSession(r.Context(), acct.ID, ip, r.UserAgent(), now)
	if err != nil {
		s.fail(w, err)
		return
	}
	http.SetCookie(w, s.sessionCookie(secret, sess.ExpiresAt))
	s.logAuth(r, api.KindAuthLogin, "me", nil)

	if isFormPost(r) {
		// A login that interrupted something goes back to it. That is how
		// /authorize gets a session without the authorization server needing
		// a second identity system of its own.
		http.Redirect(w, r, safeNext(r.PostFormValue("next")), http.StatusSeeOther)
		return
	}
	writeJSON(w, http.StatusOK, api.SessionInfo{
		Username: acct.Username, Scopes: sessionScopes,
		Actor: "me", ExpiresAt: sess.ExpiresAt, Kind: "session",
	})
}

// safeNext keeps a post-login redirect on this origin.
//
// An absolute URL here would be an open redirect on the login form, which is
// exactly the thing the authorization server spends its own code avoiding. A
// path is the only shape accepted, and a protocol-relative "//evil.example"
// is a path to url.Parse but a different host to a browser.
func safeNext(raw string) string {
	if raw == "" || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") {
		return "/"
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "" || u.Host != "" {
		return "/"
	}
	return u.String()
}

// verifySecondFactor accepts either the authenticator code or a recovery
// code. A recovery code works exactly once.
func (s *Server) verifySecondFactor(r *http.Request, acct store.Account, req api.LoginRequest, now time.Time) error {
	if req.RecoveryCode != "" {
		hash := auth.HashSecret(auth.NormalizeRecoveryCode(req.RecoveryCode))
		if err := s.store.RedeemRecoveryCode(r.Context(), acct.ID, hash, now); err != nil {
			return err
		}
		s.logAuth(r, api.KindAuthRecoveryUsed, "me", nil)
		return nil
	}
	return auth.VerifyTOTP(req.TOTP, acct.TOTPSecret, now)
}

// denyLogin is the single failure answer. Same status, same body, whatever
// went wrong, so nothing about the account leaks.
func (s *Server) denyLogin(w http.ResponseWriter) {
	writeJSON(w, http.StatusUnauthorized, &api.Error{
		Code:    api.ErrUnauthorized,
		Message: loginFailureMessage,
	})
}

// loginFailureMessage is the only thing a failed sign-in ever says. Not which
// factor was wrong, and not whether the account exists.
const loginFailureMessage = "that combination did not work"

func isFormPost(r *http.Request) bool {
	ct := r.Header.Get("Content-Type")
	return strings.HasPrefix(ct, "application/x-www-form-urlencoded") ||
		strings.HasPrefix(ct, "multipart/form-data")
}

// decodeLogin reads the credentials from a JSON body or an HTML form. The
// browser posts a form because the login page has to work with no JavaScript
// at all, which is also what makes it testable without one.
func decodeLogin(r *http.Request) (api.LoginRequest, error) {
	if !isFormPost(r) {
		var req api.LoginRequest
		err := decode(r, &req)
		return req, err
	}
	if err := r.ParseForm(); err != nil {
		return api.LoginRequest{}, &api.Error{Code: api.ErrBadRequest, Message: "could not read the form"}
	}
	req := api.LoginRequest{
		Username: r.PostFormValue("username"),
		Password: r.PostFormValue("password"),
	}
	// One field takes either factor: a six digit code or a recovery code.
	// Asking a person to pick the right box for a string they are reading off
	// a card is a question the server can answer itself.
	code := strings.TrimSpace(r.PostFormValue("totp"))
	if isSixDigits(code) {
		req.TOTP = code
	} else {
		req.RecoveryCode = code
	}
	return req, nil
}

func isSixDigits(s string) bool {
	if len(s) != 6 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// failLogin answers a failed sign-in. A form post goes back to the page
// carrying the one message; anything else gets the JSON refusal. Both are the
// same status and say the same thing.
func (s *Server) failLogin(w http.ResponseWriter, r *http.Request) {
	if isFormPost(r) && s.ui != nil {
		w.WriteHeader(http.StatusUnauthorized)
		s.ui.Login(w, r, loginFailureMessage)
		return
	}
	s.denyLogin(w)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(SessionCookie); err == nil && cookie.Value != "" {
		if err := s.store.DeleteSession(r.Context(), cookie.Value); err != nil {
			s.fail(w, err)
			return
		}
		s.logAuth(r, api.KindAuthLogout, "me", nil)
	}
	expired := s.sessionCookie("", "")
	expired.MaxAge = -1
	http.SetCookie(w, expired)

	if isFormPost(r) {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// whoami reports the calling principal, so a client can show which credential
// it is using and what that credential may do.
func (s *Server) whoami(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r.Context())
	if p == nil {
		s.denyEmpty(w, r, "no principal")
		return
	}
	writeJSON(w, http.StatusOK, api.SessionInfo{
		Username: p.TokenName, Scopes: p.Scopes, Actor: p.Actor, Kind: p.Kind,
	})
}

func (s *Server) sessionCookie(value, expiresAt string) *http.Cookie {
	c := &http.Cookie{
		Name:  SessionCookie,
		Value: value,
		Path:  "/",
		// The three flags section 15 requires. Secure is set even on a
		// loopback development server: browsers treat localhost as a secure
		// context, and a cookie whose flags differ between development and
		// production is a bug waiting for the deploy.
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	}
	if expiresAt != "" {
		if at, err := time.Parse(time.RFC3339, expiresAt); err == nil {
			c.Expires = at
			c.MaxAge = int(time.Until(at).Seconds())
		}
	}
	return c
}

// clientIP resolves the source address for the rate limiter and the auth log.
//
// X-Forwarded-For is only believed when the immediate peer is a configured
// trusted proxy. Believing it unconditionally would let anyone put a fresh
// address in the header on every attempt and walk straight past a per-IP
// limit.
func (s *Server) clientIP(r *http.Request) string {
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		peer = r.RemoteAddr
	}
	if !s.trusted(peer) {
		return peer
	}
	forwarded := r.Header.Get("X-Forwarded-For")
	if forwarded == "" {
		return peer
	}
	// The left-most entry is the original client, as appended by the first
	// proxy in the chain.
	first, _, _ := strings.Cut(forwarded, ",")
	first = strings.TrimSpace(first)
	if net.ParseIP(first) == nil {
		return peer
	}
	return first
}

func (s *Server) trusted(peer string) bool {
	ip := net.ParseIP(peer)
	if ip == nil {
		return false
	}
	for _, network := range s.TrustedProxies {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// logAuth appends an auth event. Section 15 wants every one of these, with
// the source IP, because you will want them the first time something looks
// odd. A failure to write the log must not turn into a failure to answer, so
// it is reported and swallowed.
func (s *Server) logAuth(r *http.Request, kind, actor string, extra map[string]any) {
	err := s.store.LogAuthEvent(r.Context(), kind, actor, s.clientIP(r), extra, s.Now())
	if err != nil {
		s.log.Error("writing auth event", "kind", kind, "err", err)
	}
}
