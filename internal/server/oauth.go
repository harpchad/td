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
	"github.com/harpchad/td/internal/web"
)

// The OAuth 2.1 authorization server.
//
// td is its own AS because claude.ai's custom connector UI takes a client id
// and secret and has no field for a bearer token or a custom header, so a
// static token cannot be used there at all. There is one user and no external
// IdP, so /authorize reuses the password and TOTP login that already exists
// and the consent screen is a form on the same origin.

// The endpoint paths, exported so the metadata document and the tests cannot
// disagree with the mux.
const (
	ASMetadataPath = "/.well-known/oauth-authorization-server"
	AuthorizePath  = "/authorize"
	TokenPath      = "/token"
	RegisterPath   = "/register"
	JWKSPath       = "/.well-known/jwks.json"
	RevokePath     = "/revoke"
)

// AccessTokenLifetime is how long an issued access token lives. An hour: long
// enough that a conversation does not stop mid-way, short enough that a
// revoked grant stops working while you still remember revoking it.
const AccessTokenLifetime = time.Hour

// asMetadata is the RFC 8414 document.
type asMetadata struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	RegistrationEndpoint  string   `json:"registration_endpoint"`
	RevocationEndpoint    string   `json:"revocation_endpoint"`
	JWKSURI               string   `json:"jwks_uri"`
	ScopesSupported       []string `json:"scopes_supported"`

	// No client_credentials. A machine-to-machine grant with no user in the
	// loop is not something this server supports, and advertising it would
	// invite a client to try.
	GrantTypesSupported    []string `json:"grant_types_supported"`
	ResponseTypesSupported []string `json:"response_types_supported"`

	// S256 only. OAuth 2.1 removes plain, and plain is not weak arithmetic,
	// it is no arithmetic.
	CodeChallengeMethodsSupported []string `json:"code_challenge_methods_supported"`

	TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported"`

	// RFC 8707. The resource parameter goes in both the authorization request
	// and the token request, and the token's audience is validated against it.
	ResourceIndicatorsSupported bool `json:"resource_indicators_supported"`
	// RFC 9207. The iss returned in the authorization response is what stops
	// a mix-up attack between two authorization servers.
	AuthorizationResponseISSParameterSupported bool `json:"authorization_response_iss_parameter_supported"`
	// Client ID Metadata Documents, which the 2026-07-28 revision prefers
	// over Dynamic Client Registration.
	ClientIDMetadataDocumentSupported bool `json:"client_id_metadata_document_supported"`
}

func (s *Server) asMetadataDoc(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "public, max-age=3600")
	writeJSON(w, http.StatusOK, asMetadata{
		Issuer:                s.baseURL,
		AuthorizationEndpoint: s.baseURL + AuthorizePath,
		TokenEndpoint:         s.baseURL + TokenPath,
		RegistrationEndpoint:  s.baseURL + RegisterPath,
		RevocationEndpoint:    s.baseURL + RevokePath,
		JWKSURI:               s.baseURL + JWKSPath,
		ScopesSupported: []string{
			api.MCPScopeRead, api.MCPScopeCapture, api.MCPScopeWrite,
		},
		GrantTypesSupported:               []string{"authorization_code", "refresh_token"},
		ResponseTypesSupported:            []string{"code"},
		CodeChallengeMethodsSupported:     []string{oauth.MethodS256},
		TokenEndpointAuthMethodsSupported: []string{"client_secret_post", "client_secret_basic", "none"},

		ResourceIndicatorsSupported:                true,
		AuthorizationResponseISSParameterSupported: true,
		ClientIDMetadataDocumentSupported:          true,
	})
}

// jwks publishes the public halves of the live signing keys.
func (s *Server) jwks(w http.ResponseWriter, r *http.Request) {
	keys, err := s.store.SigningKeys(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=600")
	writeJSON(w, http.StatusOK, oauth.PublicJWKS(keys))
}

// resolveClient turns a client id into a client, whichever way it exists.
//
// Two mechanisms and the caller does not care which. A URL is a Client ID
// Metadata Document, fetched from the client's own site and cached; anything
// else is a row somebody registered. The 2026-07-28 revision prefers the
// first and deprecates the second, and td advertises support for the first,
// which is a promise this function keeps.
//
// The cached copy is used while it is fresh and refetched when it is not,
// because the document is the authority on the client's name and redirect
// URIs, and a stale name on a consent screen is a consent nobody gave.
func (s *Server) resolveClient(ctx context.Context, clientID string) (store.OAuthClient, error) {
	if !oauth.IsClientIDDocumentURL(clientID) {
		return s.store.OAuthClientByID(ctx, clientID)
	}
	if s.cimd == nil {
		return store.OAuthClient{}, errors.New("client id metadata documents are not available on this server")
	}

	now := s.Now()
	cached, ok, err := s.store.CachedClientFresh(ctx, clientID, now)
	if err != nil {
		return store.OAuthClient{}, err
	}
	if ok {
		return cached, nil
	}

	doc, ttl, err := s.cimd.Resolve(ctx, clientID)
	if err != nil {
		// Logged because this is the one failure here that is somebody else's
		// server misbehaving, and the person at the browser cannot debug it.
		s.log.Warn("resolving a client id metadata document", "client_id", clientID, "err", err)
		return store.OAuthClient{}, err
	}
	return s.store.SaveResolvedClient(ctx, store.OAuthClient{
		ID:           doc.ClientID,
		Name:         doc.ClientName,
		RedirectURIs: doc.RedirectURIs,
		Scopes:       strings.Fields(doc.Scope),
		Source:       "cimd",
	}, now.Add(ttl), now)
}

// consentClient describes who is asking, for the screen that asks you about
// them. The redirect host comes from the request rather than the client's list
// because that is the one the code would actually go to.
func consentClient(client store.OAuthClient, redirectURI string) web.ConsentClient {
	out := web.ConsentClient{
		Name:          client.Name,
		SelfDescribed: client.Source == "cimd",
	}
	if u, err := url.Parse(redirectURI); err == nil {
		out.RedirectHost = u.Host
		out.LoopbackOnly = isLoopbackRedirect(u)
	}
	return out
}

func isLoopbackRedirect(u *url.URL) bool {
	host := u.Hostname()
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// clientLookupMessage says what went wrong without turning this server into a
// probe for what is reachable from it. A resolution failure names the document
// as the problem; anything else is simply not registered.
func clientLookupMessage(clientID string, err error) string {
	if !oauth.IsClientIDDocumentURL(clientID) || errors.Is(err, store.ErrNotFound) {
		return "That client is not registered."
	}
	return "That client's metadata document could not be used: " + err.Error()
}

// authorize is the consent screen.
//
// Two failure modes and they are answered very differently. If the client id
// or the redirect_uri is wrong, the error is rendered here: redirecting to an
// unvalidated URI is how an authorization code ends up at an attacker. Once
// the redirect is known good, every other error goes back to the client as
// query parameters, which is what the spec requires and what lets the client
// say something useful.
func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	client, err := s.resolveClient(r.Context(), q.Get("client_id"))
	if err != nil {
		s.oauthPage(w, r, clientLookupMessage(q.Get("client_id"), err))
		return
	}
	redirectURI := q.Get("redirect_uri")
	if !client.AllowsRedirect(redirectURI) {
		// Exact match only. A prefix or hostname match is how open
		// redirectors happen.
		s.oauthPage(w, r, "That redirect_uri is not registered for this client.")
		return
	}
	state := q.Get("state")

	if q.Get("response_type") != "code" {
		s.authError(w, r, redirectURI, state, "unsupported_response_type",
			"only the authorization code flow is supported")
		return
	}
	if err := oauth.CheckChallenge(q.Get("code_challenge"), q.Get("code_challenge_method")); err != nil {
		s.authError(w, r, redirectURI, state, "invalid_request", err.Error())
		return
	}

	// RFC 8707 makes the resource parameter mandatory, and a token minted
	// without one has an audience nobody can check.
	resource := q.Get("resource")
	if resource == "" {
		s.authError(w, r, redirectURI, state, "invalid_target",
			"the resource parameter is required")
		return
	}
	if resource != s.ResourceURL() {
		s.authError(w, r, redirectURI, state, "invalid_target",
			"this server can only issue tokens for "+s.ResourceURL())
		return
	}

	scopes, err := s.requestedScopes(q.Get("scope"), client)
	if err != nil {
		s.authError(w, r, redirectURI, state, "invalid_scope", err.Error())
		return
	}

	// The consent screen sits behind the ordinary login, which is what makes
	// this an authorization server without a second identity system. An
	// unauthenticated browser is sent to the login page and comes back here.
	if principalOf(r.Context()) == nil {
		http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.String()), http.StatusSeeOther)
		return
	}

	if s.ui == nil {
		s.oauthPage(w, r, "The consent screen is not available on this server.")
		return
	}
	s.ui.Consent(w, r, consentClient(client, redirectURI), scopes, q.Encode())
}

// approve is the consent form's POST. Approving mints the code; the consent
// screen can hand back fewer scopes than were asked for, which is the point
// of showing it.
func (s *Server) approve(w http.ResponseWriter, r *http.Request) {
	if principalOf(r.Context()) == nil {
		s.denyEmpty(w, r, "no session on the consent form")
		return
	}
	if err := r.ParseForm(); err != nil {
		s.oauthPage(w, r, "Could not read that form.")
		return
	}

	// The original query travelled through the form, so the parameters are
	// validated the same way whichever direction they arrived from.
	q, err := url.ParseQuery(r.PostFormValue("request"))
	if err != nil {
		s.oauthPage(w, r, "Could not read that request.")
		return
	}

	client, err := s.resolveClient(r.Context(), q.Get("client_id"))
	if err != nil {
		s.oauthPage(w, r, clientLookupMessage(q.Get("client_id"), err))
		return
	}
	redirectURI := q.Get("redirect_uri")
	if !client.AllowsRedirect(redirectURI) {
		s.oauthPage(w, r, "That redirect_uri is not registered for this client.")
		return
	}
	state := q.Get("state")

	if r.PostFormValue("decision") != "approve" {
		s.authError(w, r, redirectURI, state, "access_denied", "you declined")
		return
	}

	// Re-validate everything rather than trusting the round trip. The form
	// came from this server, but so would a forged one.
	if err := oauth.CheckChallenge(q.Get("code_challenge"), q.Get("code_challenge_method")); err != nil {
		s.authError(w, r, redirectURI, state, "invalid_request", err.Error())
		return
	}
	resource := q.Get("resource")
	if resource != s.ResourceURL() {
		s.authError(w, r, redirectURI, state, "invalid_target",
			"this server can only issue tokens for "+s.ResourceURL())
		return
	}

	// The consent screen lets you grant less than was asked for, so the
	// granted set comes off the form and is then narrowed against the
	// request. Widening it here would make the screen a lie.
	asked, err := s.requestedScopes(q.Get("scope"), client)
	if err != nil {
		s.authError(w, r, redirectURI, state, "invalid_scope", err.Error())
		return
	}
	granted := intersect(r.PostForm["scope"], asked)
	if len(granted) == 0 {
		s.authError(w, r, redirectURI, state, "access_denied", "you granted no scopes")
		return
	}

	code, err := s.store.IssueCode(r.Context(), store.AuthCode{
		ClientID: client.ID, RedirectURI: redirectURI, Scopes: granted,
		Resource: resource, Challenge: q.Get("code_challenge"),
	}, s.Now())
	if err != nil {
		s.fail(w, err)
		return
	}
	s.logAuth(r, api.KindOAuthGranted, s.actorOf(r), map[string]any{
		"client": client.ID, "scopes": strings.Join(granted, " "),
	})

	target, err := url.Parse(redirectURI)
	if err != nil {
		s.oauthPage(w, r, "That redirect_uri is not a URL.")
		return
	}
	out := target.Query()
	out.Set("code", code)
	if state != "" {
		out.Set("state", state)
	}
	// RFC 9207. Without iss a client talking to two authorization servers
	// cannot tell which one answered, which is a mix-up attack.
	out.Set("iss", s.baseURL)
	target.RawQuery = out.Encode()
	http.Redirect(w, r, target.String(), http.StatusSeeOther)
}

// tokenResponse is the /token body.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token,omitempty"`
	Scope        string `json:"scope"`
}

// oauthError is the RFC 6749 error body.
type oauthError struct {
	Code        string `json:"error"`
	Description string `json:"error_description,omitempty"`
}

// token exchanges a code or a refresh token for an access token.
func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.tokenError(w, http.StatusBadRequest, "invalid_request", "could not read the form")
		return
	}

	switch grant := r.PostFormValue("grant_type"); grant {
	case "authorization_code":
		s.tokenFromCode(w, r)
	case "refresh_token":
		s.tokenFromRefresh(w, r)
	case "client_credentials":
		// Named rather than lumped in with the unknown ones, because a client
		// that tries it should be told this is a decision and not an
		// oversight. A machine-to-machine grant has no user in the loop, and
		// every token this server issues acts as the one account holder.
		s.tokenError(w, http.StatusBadRequest, "unsupported_grant_type",
			"client_credentials is not supported: every token here acts as the account holder, and that needs a person in the loop")
	default:
		s.tokenError(w, http.StatusBadRequest, "unsupported_grant_type",
			"grant_type must be authorization_code or refresh_token, got "+grant)
	}
}

func (s *Server) tokenFromCode(w http.ResponseWriter, r *http.Request) {
	now := s.Now()

	clientID, secret := clientCredentials(r)
	client, err := s.authenticateClient(r.Context(), clientID, secret)
	if err != nil {
		s.tokenError(w, http.StatusUnauthorized, "invalid_client", "client authentication failed")
		return
	}

	code, err := s.store.RedeemCode(r.Context(), r.PostFormValue("code"), now)
	if err != nil {
		s.tokenError(w, http.StatusBadRequest, "invalid_grant", message(err))
		return
	}
	if code.ClientID != client.ID {
		s.tokenError(w, http.StatusBadRequest, "invalid_grant", "that code was issued to another client")
		return
	}
	// The redirect_uri is compared again at the token endpoint, which is what
	// stops a code from being redeemed against a different registered URI
	// than the one it was issued for.
	if code.RedirectURI != r.PostFormValue("redirect_uri") {
		s.tokenError(w, http.StatusBadRequest, "invalid_grant", "redirect_uri does not match the one the code was issued for")
		return
	}
	if err := oauth.VerifyChallenge(r.PostFormValue("code_verifier"), code.Challenge); err != nil {
		s.tokenError(w, http.StatusBadRequest, "invalid_grant", err.Error())
		return
	}

	// RFC 8707 again: a resource on the token request has to be the one the
	// code was issued for, or the audience is not what the user consented to.
	if requested := r.PostFormValue("resource"); requested != "" && requested != code.Resource {
		s.tokenError(w, http.StatusBadRequest, "invalid_target",
			"the resource does not match the one this code was issued for")
		return
	}

	grant, refresh, err := s.store.CreateGrant(r.Context(), store.OAuthGrant{
		ClientID: client.ID, Scopes: code.Scopes, Resource: code.Resource,
	}, now)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.issue(w, r, grant, refresh, now)
}

func (s *Server) tokenFromRefresh(w http.ResponseWriter, r *http.Request) {
	now := s.Now()

	clientID, secret := clientCredentials(r)
	client, err := s.authenticateClient(r.Context(), clientID, secret)
	if err != nil {
		s.tokenError(w, http.StatusUnauthorized, "invalid_client", "client authentication failed")
		return
	}

	grant, err := s.store.GrantByRefreshToken(r.Context(), r.PostFormValue("refresh_token"), now)
	if err != nil {
		s.tokenError(w, http.StatusBadRequest, "invalid_grant", "that refresh token is not valid")
		return
	}
	if grant.ClientID != client.ID {
		s.tokenError(w, http.StatusBadRequest, "invalid_grant", "that refresh token belongs to another client")
		return
	}

	// A refresh may narrow the scopes and must never widen them.
	if asked := strings.Fields(r.PostFormValue("scope")); len(asked) > 0 {
		narrowed := intersect(asked, grant.Scopes)
		if len(narrowed) == 0 {
			s.tokenError(w, http.StatusBadRequest, "invalid_scope", "none of those scopes were granted")
			return
		}
		grant.Scopes = narrowed
	}

	// Rotation. OAuth 2.1 requires it for public clients, and it is cheap
	// enough to do for every one: a stolen refresh token is then usable at
	// most once, and the theft surfaces as the real client suddenly failing.
	rotated, err := s.store.RotateRefreshToken(r.Context(), grant.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.issue(w, r, grant, rotated, now)
}

// issue signs an access token for a grant.
func (s *Server) issue(w http.ResponseWriter, r *http.Request, grant store.OAuthGrant, refresh string, now time.Time) {
	keys, err := s.store.EnsureSigningKeys(r.Context(), now)
	if err != nil || len(keys) == 0 {
		s.fail(w, err)
		return
	}

	access, err := oauth.Sign(keys[0], oauth.Claims{
		Issuer:   s.baseURL,
		Subject:  "me",
		Audience: grant.Resource,
		ClientID: grant.ClientID,
		Scope:    strings.Join(grant.Scopes, " "),
		IssuedAt: now.Unix(), NotBefore: now.Unix(),
		Expires: now.Add(AccessTokenLifetime).Unix(),
		JTI:     store.NewID(),
	})
	if err != nil {
		s.fail(w, err)
		return
	}

	// A token response must never be cached: it is a credential.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	writeJSON(w, http.StatusOK, tokenResponse{
		AccessToken: access, TokenType: "Bearer",
		ExpiresIn:    int(AccessTokenLifetime.Seconds()),
		RefreshToken: refresh,
		Scope:        strings.Join(grant.Scopes, " "),
	})
}

// registrationRequest is the RFC 7591 body.
type registrationRequest struct {
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	Scope                   string   `json:"scope"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
}

type registrationResponse struct {
	ClientID     string   `json:"client_id"`
	ClientSecret string   `json:"client_secret,omitempty"`
	ClientName   string   `json:"client_name"`
	RedirectURIs []string `json:"redirect_uris"`
	Scope        string   `json:"scope"`
}

// register is Dynamic Client Registration.
//
// Deprecated by the 2026-07-28 revision in favour of Client ID Metadata
// Documents, and kept because some clients speak nothing else. It is not user
// registration: it creates no account and grants no access. A registered
// client still has to send its user through /authorize, where the one account
// holder decides.
func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	var in registrationRequest
	if err := decode(r, &in); err != nil {
		s.tokenError(w, http.StatusBadRequest, "invalid_client_metadata", message(err))
		return
	}
	if len(in.RedirectURIs) == 0 {
		s.tokenError(w, http.StatusBadRequest, "invalid_redirect_uri",
			"at least one redirect_uri is required")
		return
	}
	for _, uri := range in.RedirectURIs {
		if err := checkRedirectURI(uri); err != nil {
			s.tokenError(w, http.StatusBadRequest, "invalid_redirect_uri", err.Error())
			return
		}
	}

	scopes := strings.Fields(in.Scope)
	if len(scopes) == 0 {
		scopes = []string{api.MCPScopeRead}
	}
	for _, scope := range scopes {
		if api.ScopeFromMCP(scope) == "" {
			s.tokenError(w, http.StatusBadRequest, "invalid_client_metadata",
				"unknown scope "+scope)
			return
		}
	}

	// A public client is one that says it cannot keep a secret. It relies on
	// PKCE alone, which is what OAuth 2.1 expects of it.
	var secret string
	if in.TokenEndpointAuthMethod != "none" {
		var err error
		if secret, err = newClientSecret(); err != nil {
			s.fail(w, err)
			return
		}
	}

	client, err := s.store.RegisterClient(r.Context(), store.OAuthClient{
		Name: in.ClientName, RedirectURIs: in.RedirectURIs, Scopes: scopes,
	}, secret, s.Now())
	if err != nil {
		s.fail(w, err)
		return
	}
	s.logAuth(r, api.KindOAuthRegistered, "anonymous", map[string]any{
		"client": client.ID, "name": client.Name,
	})

	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, registrationResponse{
		ClientID: client.ID, ClientSecret: secret, ClientName: client.Name,
		RedirectURIs: client.RedirectURIs, Scope: strings.Join(client.Scopes, " "),
	})
}

// revoke is RFC 7009, so a client can hand a token back.
func (s *Server) revokeGrant(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.tokenError(w, http.StatusBadRequest, "invalid_request", "could not read the form")
		return
	}
	token := r.PostFormValue("token")
	if grant, err := s.store.GrantByRefreshToken(r.Context(), token, s.Now()); err == nil {
		if err := s.store.RevokeGrant(r.Context(), grant.ID, s.Now()); err != nil {
			s.fail(w, err)
			return
		}
		s.logAuth(r, api.KindOAuthRevoked, "anonymous", map[string]any{"grant": grant.ID})
	}
	// RFC 7009 says 200 whether or not the token existed. Reporting the
	// difference would turn this into an oracle for guessing tokens.
	w.WriteHeader(http.StatusOK)
}

// --- helpers ---

// requestedScopes narrows what was asked for against what the client
// registered. An unknown scope is refused rather than dropped, because a
// client that asked for something it cannot have should be told.
// authenticateClient checks a client at the token endpoint.
//
// A CIMD client is public by construction: its document is world readable, so
// nothing in it can be a secret. It is resolved rather than authenticated, and
// PKCE is what binds the code to the client, which is what OAuth 2.1 expects
// of anything that cannot keep a secret.
func (s *Server) authenticateClient(ctx context.Context, id, secret string) (store.OAuthClient, error) {
	if !oauth.IsClientIDDocumentURL(id) {
		return s.store.AuthenticateClient(ctx, id, secret)
	}
	if secret != "" {
		return store.OAuthClient{}, &api.Error{
			Code: api.ErrUnauthorized, Message: "this client has no secret",
		}
	}
	return s.resolveClient(ctx, id)
}

func (s *Server) requestedScopes(asked string, client store.OAuthClient) ([]string, error) {
	want := strings.Fields(asked)
	if len(want) == 0 {
		want = client.Scopes
	}
	if len(want) == 0 {
		want = []string{api.MCPScopeRead}
	}

	out := make([]string, 0, len(want))
	for _, scope := range want {
		if api.ScopeFromMCP(scope) == "" {
			return nil, errors.New("unknown scope " + scope)
		}
		if len(client.Scopes) > 0 && !containsString(client.Scopes, scope) {
			return nil, errors.New("this client is not registered for " + scope)
		}
		out = append(out, scope)
	}
	return out, nil
}

// authError sends an error back to the client through the redirect, which is
// only ever called once the redirect_uri has been validated.
func (s *Server) authError(w http.ResponseWriter, r *http.Request, redirectURI, state, code, description string) {
	target, err := url.Parse(redirectURI)
	if err != nil {
		s.oauthPage(w, r, description)
		return
	}
	q := target.Query()
	q.Set("error", code)
	q.Set("error_description", description)
	if state != "" {
		q.Set("state", state)
	}
	q.Set("iss", s.baseURL)
	target.RawQuery = q.Encode()
	http.Redirect(w, r, target.String(), http.StatusSeeOther)
}

// oauthPage renders an error here rather than redirecting. It is what an
// unvalidated redirect_uri gets: sending a code or an error to a URI nobody
// checked is how an authorization code ends up somewhere else.
func (s *Server) oauthPage(w http.ResponseWriter, r *http.Request, message string) {
	if s.ui != nil && wantsHTML(r) {
		s.ui.OAuthError(w, r, message)
		return
	}
	writeJSON(w, http.StatusBadRequest, oauthError{Code: "invalid_request", Description: message})
}

func (s *Server) tokenError(w http.ResponseWriter, status int, code, description string) {
	w.Header().Set("Cache-Control", "no-store")
	if status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Bearer realm="td", error="invalid_client"`)
	}
	writeJSON(w, status, oauthError{Code: code, Description: description})
}

// clientCredentials reads the client id and secret from either the form or
// HTTP Basic, which are the two methods the metadata advertises.
func clientCredentials(r *http.Request) (id, secret string) {
	if user, pass, ok := r.BasicAuth(); ok {
		return user, pass
	}
	return r.PostFormValue("client_id"), r.PostFormValue("client_secret")
}

// checkRedirectURI refuses anything that is not a fixed https URL or a
// loopback http one.
//
// http on a public host would put an authorization code on the wire in the
// clear. Loopback is exempt because a desktop client has nowhere else to
// listen, and it is the case OAuth 2.1 explicitly keeps.
func checkRedirectURI(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("redirect_uri is not a URL")
	}
	if u.Fragment != "" {
		return errors.New("redirect_uri must not carry a fragment")
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		host := u.Hostname()
		if host == "127.0.0.1" || host == "::1" || host == "localhost" {
			return nil
		}
		return errors.New("redirect_uri must be https unless it is loopback")
	default:
		return errors.New("redirect_uri must be http or https")
	}
}

func newClientSecret() (string, error) {
	secret, _, err := auth.NewSessionSecret()
	return secret, err
}

func intersect(asked, allowed []string) []string {
	out := make([]string, 0, len(asked))
	for _, scope := range asked {
		if containsString(allowed, scope) && !containsString(out, scope) {
			out = append(out, scope)
		}
	}
	return out
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func message(err error) string {
	var apiErr *api.Error
	if errors.As(err, &apiErr) && apiErr.Message != "" {
		return apiErr.Message
	}
	return err.Error()
}
