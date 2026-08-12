package server_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/auth"
	"github.com/harpchad/td/internal/server"
	"github.com/harpchad/td/internal/store"
)

// The tests in this file are the security assertions from CLAUDE.md. Each one
// is stated there as a test rather than a review item, and this is where they
// live. Two of the thirteen belong to the OAuth authorization server and land
// in phase 9: the audience match and the /authorize PKCE rules.

// Assertion: any /api/v1/* request without a valid token returns 401 with an
// empty body.
func TestUnauthenticatedAPIRequestsGet401WithNoBody(t *testing.T) {
	ts := newServer(t)

	paths := []string{
		"/api/v1/tasks", "/api/v1/tasks/101", "/api/v1/people",
		"/api/v1/filters", "/api/v1/events", "/api/v1/whoami", "/api/v1/tokens",
	}
	for _, path := range paths {
		resp, body := doAnon(t, ts, http.MethodGet, path, nil)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("GET %s = %d, want 401", path, resp.StatusCode)
		}
		if len(body) != 0 {
			t.Errorf("GET %s returned a body of %d bytes, want none: %s", path, len(body), body)
		}
	}

	// A wrong token and a revoked token are the same answer as no token.
	for _, header := range []string{"Bearer td_nonsense", "Bearer ", "Basic abc", "td_nonsense"} {
		resp, body := request(t, ts, http.MethodGet, "/api/v1/tasks", nil,
			map[string]string{"Authorization": header})
		if resp.StatusCode != http.StatusUnauthorized || len(body) != 0 {
			t.Errorf("Authorization %q = %d with %d bytes, want 401 and nothing",
				header, resp.StatusCode, len(body))
		}
	}
}

// Assertion: a login attempt with an unknown account and one with a known
// account return the same status, the same body, and timings within 50ms.
//
// This one uses the real argon2 parameters, because the timing property is
// exactly what the dummy-hash verification exists to produce and light
// parameters would make the test pass for the wrong reason.
func TestLoginDoesNotRevealWhetherAnAccountExists(t *testing.T) {
	ts := newServerWithRealHashing(t)

	known := api.LoginRequest{Username: testUsername, Password: "wrong password entirely"}
	unknown := api.LoginRequest{Username: "nobody-here", Password: "wrong password entirely"}

	knownStatus, knownBody := doAnon(t, ts, http.MethodPost, "/login", known)
	unknownStatus, unknownBody := doAnon(t, ts, http.MethodPost, "/login", unknown)

	if knownStatus.StatusCode != unknownStatus.StatusCode {
		t.Errorf("status %d for a known account and %d for an unknown one",
			knownStatus.StatusCode, unknownStatus.StatusCode)
	}
	if string(knownBody) != string(unknownBody) {
		t.Errorf("bodies differ:\n known:   %s\n unknown: %s", knownBody, unknownBody)
	}

	// Medians rather than single samples: one scheduling hiccup should not
	// decide whether this passes.
	knownMedian := medianLoginTime(t, ts, known)
	unknownMedian := medianLoginTime(t, ts, unknown)

	delta := knownMedian - unknownMedian
	if delta < 0 {
		delta = -delta
	}
	if delta > 50*time.Millisecond {
		t.Errorf("median login time differs by %s, want under 50ms (known %s, unknown %s)",
			delta, knownMedian, unknownMedian)
	}
	// A guard against the test passing because both paths got fast: if the
	// known-account path skipped argon2 entirely, the property is not being
	// tested at all.
	if knownMedian < time.Millisecond {
		t.Errorf("a login attempt took %s, which means no password hashing happened", knownMedian)
	}
}

func medianLoginTime(t *testing.T, ts *harness, req api.LoginRequest) time.Duration {
	t.Helper()
	const samples = 7
	times := make([]time.Duration, 0, samples)
	for range samples {
		// Clear the rate limiter and the lockout between samples, so neither
		// short-circuits the path being measured.
		resetLoginState(t, ts)
		start := time.Now()
		doAnon(t, ts, http.MethodPost, "/login", req)
		times = append(times, time.Since(start))
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	return times[len(times)/2]
}

func resetLoginState(t *testing.T, ts *harness) {
	t.Helper()
	ctx := context.Background()
	if _, err := ts.store.DB().ExecContext(ctx, `DELETE FROM login_attempt`); err != nil {
		t.Fatal(err)
	}
	if _, err := ts.store.DB().ExecContext(ctx,
		`UPDATE account SET failed_password = 0, failed_totp = 0, locked_until = NULL`); err != nil {
		t.Fatal(err)
	}
}

// Assertion: five failed password attempts lock the account for 15 minutes.
func TestFivePasswordFailuresLockTheAccount(t *testing.T) {
	ts := newServer(t)
	ctx := context.Background()

	bad := api.LoginRequest{Username: testUsername, Password: "not it", TOTP: ts.totp}
	for i := range store.LockoutThreshold {
		resp, _ := doAnon(t, ts, http.MethodPost, "/login", bad)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("attempt %d = %d, want 401", i+1, resp.StatusCode)
		}
	}

	acct, err := ts.store.TheAccount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !acct.Locked(ts.now) {
		t.Fatal("the account is not locked after five failures")
	}

	// The right password does not get in while locked.
	good := api.LoginRequest{Username: testUsername, Password: testPassword, TOTP: ts.totp}
	resp, _ := doAnon(t, ts, http.MethodPost, "/login", good)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("a correct login during lockout = %d, want 401", resp.StatusCode)
	}

	// Fifteen minutes, not more and not less.
	if !acct.Locked(ts.now.Add(14 * time.Minute)) {
		t.Error("the lockout ended before 15 minutes")
	}
	if acct.Locked(ts.now.Add(16 * time.Minute)) {
		t.Error("the lockout outlasted 15 minutes")
	}
}

// Assertion: failed TOTP attempts count separately from failed passwords.
//
// The testable consequence is that four of each does not lock, even though
// eight failures have happened.
func TestTOTPFailuresCountSeparatelyFromPasswordFailures(t *testing.T) {
	ts := newServer(t)
	ctx := context.Background()

	for range store.LockoutThreshold - 1 {
		doAnon(t, ts, http.MethodPost, "/login",
			api.LoginRequest{Username: testUsername, Password: "wrong", TOTP: ts.totp})
	}
	for range store.LockoutThreshold - 1 {
		doAnon(t, ts, http.MethodPost, "/login",
			api.LoginRequest{Username: testUsername, Password: testPassword, TOTP: "000000"})
	}

	acct, err := ts.store.TheAccount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if acct.Locked(ts.now) {
		t.Fatal("four password failures and four TOTP failures locked the account, so the counters are pooled")
	}
	if acct.FailedPassword != store.LockoutThreshold-1 || acct.FailedTOTP != store.LockoutThreshold-1 {
		t.Errorf("counters = password %d, totp %d, want %d each",
			acct.FailedPassword, acct.FailedTOTP, store.LockoutThreshold-1)
	}

	// One more of either kind is what tips it.
	doAnon(t, ts, http.MethodPost, "/login",
		api.LoginRequest{Username: testUsername, Password: testPassword, TOTP: "000000"})
	acct, err = ts.store.TheAccount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !acct.Locked(ts.now) {
		t.Error("a fifth TOTP failure did not lock the account")
	}
}

// Assertion: the session cookie carries HttpOnly, Secure, and SameSite=Lax.
func TestSessionCookieFlags(t *testing.T) {
	ts := newServer(t)

	resp, body := doAnon(t, ts, http.MethodPost, "/login",
		api.LoginRequest{Username: testUsername, Password: testPassword, TOTP: ts.totp})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login = %d: %s", resp.StatusCode, body)
	}

	cookie := findCookie(resp.Header, server.SessionCookie)
	if cookie == nil {
		t.Fatal("login set no session cookie")
	}
	if !cookie.HttpOnly {
		t.Error("the session cookie is not HttpOnly")
	}
	if !cookie.Secure {
		t.Error("the session cookie is not Secure")
	}
	if cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, want Lax", cookie.SameSite)
	}
	if cookie.Value == "" {
		t.Error("the session cookie has no value")
	}
}

func findCookie(h http.Header, name string) *http.Cookie {
	resp := http.Response{Header: h}
	for _, c := range resp.Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// Assertion: no route serves an attachment without checking auth first.
//
// Written against the routing table rather than against the attachment route,
// which lands in phase 7. Everything but the health check and the login pair
// requires a credential, so the route added then is covered the day it exists.
func TestOnlyTheHealthCheckAndLoginAnswerWithoutACredential(t *testing.T) {
	ts := newServer(t)

	public := map[string]bool{"/healthz": true, "/login": true, "/logout": true}

	routes := []struct{ method, path string }{
		{http.MethodGet, "/healthz"},
		{http.MethodPost, "/login"},
		{http.MethodPost, "/logout"},
		{http.MethodGet, "/api/v1/tasks"},
		{http.MethodPost, "/api/v1/tasks"},
		{http.MethodGet, "/api/v1/tasks/101"},
		{http.MethodPatch, "/api/v1/tasks/101"},
		{http.MethodDelete, "/api/v1/tasks/101"},
		{http.MethodPost, "/api/v1/tasks/101/complete"},
		{http.MethodGet, "/api/v1/people"},
		{http.MethodGet, "/api/v1/filters"},
		{http.MethodPost, "/api/v1/filters"},
		{http.MethodDelete, "/api/v1/filters/7"},
		{http.MethodGet, "/api/v1/events"},
		{http.MethodPost, "/api/v1/undo"},
		{http.MethodGet, "/api/v1/whoami"},
		{http.MethodGet, "/api/v1/tokens"},
		{http.MethodPost, "/api/v1/tokens"},
		{http.MethodDelete, "/api/v1/tokens/whatever"},
		// Phase 7's attachment routes. They do not exist yet, and when they
		// do they inherit the same middleware, which is the point.
		{http.MethodGet, "/api/v1/tasks/101/attachments/1"},
		{http.MethodPost, "/api/v1/tasks/101/attachments"},
	}

	for _, route := range routes {
		resp, body := doAnon(t, ts, route.method, route.path, map[string]any{})

		if public[route.path] {
			// A public route may still refuse the request on its own terms:
			// /login answers 401 to a bad password. What it must not do is
			// get turned away by the middleware, and the two are told apart
			// by the body. The middleware's refusal has none.
			if resp.StatusCode == http.StatusUnauthorized && len(body) == 0 {
				t.Errorf("%s %s was refused by the auth middleware, but it is meant to be reachable",
					route.method, route.path)
			}
			continue
		}

		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s = %d without a credential, want 401", route.method, route.path, resp.StatusCode)
		}
		if len(body) != 0 {
			t.Errorf("%s %s returned a body with its 401: %s", route.method, route.path, body)
		}
	}
}

// Assertion: a database dump contains no usable token and no plaintext
// password.
func TestDatabaseDumpHoldsNoUsableCredential(t *testing.T) {
	ts := newServer(t)
	ctx := context.Background()

	// Log in so a session exists, and mint a second token, so the dump has
	// one of every kind of secret in it.
	resp, _ := doAnon(t, ts, http.MethodPost, "/login",
		api.LoginRequest{Username: testUsername, Password: testPassword, TOTP: ts.totp})
	sessionCookie := findCookie(resp.Header, server.SessionCookie)
	if sessionCookie == nil {
		t.Fatal("no session to test with")
	}

	second, err := ts.store.CreateToken(ctx, "another", "mcp:claude", []string{api.ScopeRead}, ts.now)
	if err != nil {
		t.Fatal(err)
	}

	dump := dumpDatabase(t, ts.store)

	secrets := map[string]string{
		"the account password":    testPassword,
		"the harness token":       ts.token,
		"a second token":          second.Secret,
		"the session cookie":      sessionCookie.Value,
		"the first recovery code": ts.recovery[0],
	}
	for what, secret := range secrets {
		if strings.Contains(dump, secret) {
			t.Errorf("%s appears verbatim in a database dump", what)
		}
	}
	// The normalized form of a recovery code is what gets hashed, so check it
	// too rather than only the printed form with its hyphens.
	if strings.Contains(dump, auth.NormalizeRecoveryCode(ts.recovery[0])) {
		t.Error("a recovery code appears in a database dump once its hyphens are stripped")
	}

	// And the stored material really is there, so this test cannot pass by
	// looking at an empty dump.
	if !strings.Contains(dump, auth.HashSecret(ts.token)) {
		t.Fatal("the dump does not contain the token hash, so it is not dumping what it should")
	}
}

// dumpDatabase reads every value out of every table, which is what someone
// with the file gets.
func dumpDatabase(t *testing.T, st *store.Store) string {
	t.Helper()
	ctx := context.Background()

	tables, err := st.DB().QueryContext(ctx,
		`SELECT name FROM sqlite_master WHERE type = 'table'`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for tables.Next() {
		var name string
		if err := tables.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	_ = tables.Close()

	var out strings.Builder
	for _, name := range names {
		rows, err := st.DB().QueryContext(ctx, `SELECT * FROM "`+name+`"`)
		if err != nil {
			// Virtual FTS shadow tables are not all directly selectable.
			continue
		}
		cols, err := rows.Columns()
		if err != nil {
			_ = rows.Close()
			continue
		}
		for rows.Next() {
			cells := make([]any, len(cols))
			for i := range cells {
				cells[i] = new(any)
			}
			if err := rows.Scan(cells...); err != nil {
				continue
			}
			for _, cell := range cells {
				if v := *(cell.(*any)); v != nil {
					out.WriteString(strings.TrimSpace(stringify(v)))
					out.WriteByte('\n')
				}
			}
		}
		_ = rows.Close()
	}
	return out.String()
}

func stringify(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []byte:
		return string(t)
	default:
		b, _ := json.Marshal(t)
		return string(b)
	}
}

// Assertion: each recovery code works exactly once.
func TestEachRecoveryCodeWorksExactlyOnce(t *testing.T) {
	ts := newServer(t)

	code := ts.recovery[0]
	req := api.LoginRequest{Username: testUsername, Password: testPassword, RecoveryCode: code}

	resp, body := doAnon(t, ts, http.MethodPost, "/login", req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first use of a recovery code = %d: %s", resp.StatusCode, body)
	}

	resp, _ = doAnon(t, ts, http.MethodPost, "/login", req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("second use of the same recovery code = %d, want 401", resp.StatusCode)
	}

	// A different code is still good, so the first use spent one and not all.
	other := api.LoginRequest{Username: testUsername, Password: testPassword, RecoveryCode: ts.recovery[1]}
	resp, body = doAnon(t, ts, http.MethodPost, "/login", other)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("a different recovery code = %d, want 200: %s", resp.StatusCode, body)
	}

	// A code typed in the wrong case and without hyphens still works.
	typed := strings.ToLower(strings.ReplaceAll(ts.recovery[2], "-", ""))
	resp, _ = doAnon(t, ts, http.MethodPost, "/login",
		api.LoginRequest{Username: testUsername, Password: testPassword, RecoveryCode: typed})
	if resp.StatusCode != http.StatusOK {
		t.Errorf("a code typed without its hyphens = %d, want 200", resp.StatusCode)
	}
}

// Assertion: the response carries HSTS, X-Frame-Options: DENY, and a CSP with
// no unsafe-inline.
func TestSecurityHeadersOnEveryResponse(t *testing.T) {
	ts := newServer(t)

	type probe struct {
		name string
		call func() respMeta
	}
	probes := []probe{
		{"an authenticated read", func() respMeta {
			r, _ := do(t, ts, http.MethodGet, "/api/v1/tasks", nil)
			return r
		}},
		{"the empty-bodied 401", func() respMeta {
			r, _ := doAnon(t, ts, http.MethodGet, "/api/v1/tasks", nil)
			return r
		}},
		{"the health check", func() respMeta {
			r, _ := doAnon(t, ts, http.MethodGet, "/healthz", nil)
			return r
		}},
		{"a failed login", func() respMeta {
			r, _ := doAnon(t, ts, http.MethodPost, "/login", api.LoginRequest{Username: "x"})
			return r
		}},
		{"a 404", func() respMeta {
			r, _ := doAnon(t, ts, http.MethodGet, "/nothing-here", nil)
			return r
		}},
	}

	for _, p := range probes {
		resp := p.call()

		if hsts := resp.Header.Get("Strict-Transport-Security"); !strings.Contains(hsts, "max-age=") {
			t.Errorf("%s: HSTS = %q", p.name, hsts)
		}
		if got := resp.Header.Get("X-Frame-Options"); got != "DENY" {
			t.Errorf("%s: X-Frame-Options = %q, want DENY", p.name, got)
		}

		csp := resp.Header.Get("Content-Security-Policy")
		if csp == "" {
			t.Errorf("%s: no CSP", p.name)
			continue
		}
		if strings.Contains(csp, "unsafe-inline") {
			t.Errorf("%s: CSP contains unsafe-inline: %s", p.name, csp)
		}
		if strings.Contains(csp, "unsafe-eval") {
			t.Errorf("%s: CSP contains unsafe-eval: %s", p.name, csp)
		}
		for _, directive := range []string{"default-src", "script-src", "frame-ancestors"} {
			if !strings.Contains(csp, directive) {
				t.Errorf("%s: CSP has no %s: %s", p.name, directive, csp)
			}
		}
	}
}

// Assertion: there is no registration route and no password reset route.
func TestThereIsNoRegistrationOrPasswordResetRoute(t *testing.T) {
	ts := newServer(t)

	// /register is deliberately absent from this list. Section 15 states the
	// carve-out itself: "OAuth client registration is not user registration
	// and creates no account." What that route does is asserted below.
	paths := []string{
		"/signup", "/sign-up", "/account/create", "/accounts",
		"/reset", "/forgot", "/forgot-password", "/password/reset", "/password-reset",
		"/api/v1/register", "/api/v1/signup", "/api/v1/accounts",
		"/api/v1/password/reset", "/api/v1/account",
	}

	for _, path := range paths {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			// Unauthenticated, which is how anyone would find such a route.
			resp, _ := doAnon(t, ts, method, path, map[string]any{})
			if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("%s %s = %d, want 404 or 401: this looks like a route that should not exist",
					method, path, resp.StatusCode)
			}
			// And with a full-scope credential, so a hidden route cannot pass
			// by being merely unauthenticated.
			resp, _ = do(t, ts, method, path, map[string]any{})
			if resp.StatusCode != http.StatusNotFound {
				t.Errorf("authenticated %s %s = %d, want 404", method, path, resp.StatusCode)
			}
		}
	}
}

// Assertion: OAuth client registration is not user registration and creates
// no account.
//
// This is the other half of the rule above. /register exists because some MCP
// clients speak nothing but Dynamic Client Registration, and the thing that
// makes it safe is that a registered client has no access at all until the
// one account holder approves a consent screen.
func TestClientRegistrationCreatesNoAccount(t *testing.T) {
	ts := newServer(t)

	before, err := ts.store.TheAccount(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	resp, body := doAnon(t, ts, http.MethodPost, "/register", map[string]any{
		"client_name":   "a client",
		"redirect_uris": []string{"https://claude.ai/api/mcp/auth_callback"},
		"scope":         "td:read",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d: %s", resp.StatusCode, body)
	}
	var registered struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}
	decodeInto(t, body, &registered)
	if registered.ClientID == "" {
		t.Fatal("no client_id came back")
	}

	// Still exactly one account, and the same one.
	after, err := ts.store.TheAccount(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if after.ID != before.ID || after.Username != before.Username {
		t.Error("registering a client changed the account")
	}

	// And the registration bought no access. The client id and secret are
	// not a credential for anything until a consent screen is approved.
	for _, credential := range []string{registered.ClientID, registered.ClientSecret} {
		if credential == "" {
			continue
		}
		resp, out := request(t, ts, http.MethodGet, "/api/v1/tasks", nil, map[string]string{
			"Authorization": "Bearer " + credential,
		})
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("a registration credential reached the API: %d %s", resp.StatusCode, out)
		}
	}
}

// Assertion: no endpoint accepts a token in a query string.
//
// A credential in a URL ends up in access logs, in browser history, and in
// the Referer header of anything the page links to.
func TestNoEndpointAcceptsATokenInAQueryString(t *testing.T) {
	ts := newServer(t)

	for _, key := range []string{"access_token", "token", "api_key", "apikey", "bearer", "auth"} {
		path := "/api/v1/tasks?" + key + "=" + ts.token
		resp, body := doAnon(t, ts, http.MethodGet, path, nil)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s in the query string = %d, want 401", key, resp.StatusCode)
		}
		if len(body) != 0 {
			t.Errorf("%s in the query string returned a body", key)
		}

		// Even alongside a valid header, the request is refused rather than
		// quietly succeeding: the parameter should never have been sent.
		resp, _ = do(t, ts, http.MethodGet, path, nil)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s in the query string with a valid header = %d, want 401", key, resp.StatusCode)
		}
	}
}

// Assertion, from section 14: until an account exists, every route returns
// 503 with "no account configured".
func TestUnconfiguredServerAnswers503(t *testing.T) {
	st, err := store.Open(":memory:", store.Options{Location: time.UTC})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	srv, err := server.New(st, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	for _, path := range []string{"/api/v1/tasks", "/login", "/api/v1/whoami", "/anything"} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var apiErr api.Error
		_ = json.NewDecoder(resp.Body).Decode(&apiErr)
		_ = resp.Body.Close()

		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("%s on an unconfigured server = %d, want 503", path, resp.StatusCode)
		}
		if apiErr.Code != api.ErrNoAccount {
			t.Errorf("%s error = %q, want %q", path, apiErr.Code, api.ErrNoAccount)
		}
		if !strings.Contains(apiErr.Message, "tdd account create") {
			t.Errorf("%s message = %q, it should say what to do", path, apiErr.Message)
		}
	}

	// The health check still answers: it reports whether the process is
	// alive, and a liveness probe that fails on a live process is broken.
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+"/healthz", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("/healthz on an unconfigured server = %d, want 200", resp.StatusCode)
	}
}

// newServerWithRealHashing builds a harness whose account was hashed with the
// production argon2 parameters, for the timing assertion.
func newServerWithRealHashing(t *testing.T) *harness {
	t.Helper()
	ts := newServer(t)

	hash, err := auth.HashPassword(testPassword, auth.DefaultParams)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ts.store.DB().ExecContext(context.Background(),
		`UPDATE account SET password_hash = ?`, hash); err != nil {
		t.Fatal(err)
	}
	return ts
}

// Assertion: a sync token reaches its own source and nothing else.
//
// Each plugin gets its own scope, sync:<source>, so a compromised Planner
// token cannot rewrite the Jira mirror and cannot touch anything local. This
// is the reason the scope is namespaced rather than one shared "sync".
func TestASyncScopeIsPerSource(t *testing.T) {
	ts := newServer(t)

	planner, err := ts.store.CreateToken(t.Context(), "planner", "plugin:planner",
		[]string{api.ScopeSyncPrefix + "planner"}, ts.now)
	if err != nil {
		t.Fatal(err)
	}

	body := map[string]any{"items": []map[string]any{
		{"external_id": "PLAN-1", "title": "Renew the certificate", "status": "todo", "rev": "1"},
	}}

	// Its own source works.
	resp, out := request(t, ts, http.MethodPost, "/api/v1/sync/planner", body,
		map[string]string{"Authorization": "Bearer " + planner.Secret})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("own source = %d: %s", resp.StatusCode, out)
	}

	// Another plugin's does not.
	resp, _ = request(t, ts, http.MethodPost, "/api/v1/sync/jira", body,
		map[string]string{"Authorization": "Bearer " + planner.Secret})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("another source = %d, want 403", resp.StatusCode)
	}

	// And it reaches nothing else in the API. A sync scope is not a write
	// scope, and a plugin has no business creating tasks by hand.
	for _, call := range []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, "/api/v1/tasks", map[string]any{"title": "not yours"}},
		{http.MethodPatch, "/api/v1/tasks/103", map[string]any{"title": "not yours"}},
		{http.MethodDelete, "/api/v1/tasks/103", nil},
		{http.MethodPost, "/api/v1/undo", map[string]any{}},
		{http.MethodGet, "/api/v1/tasks", nil},
		{http.MethodGet, "/api/v1/tokens", nil},
	} {
		resp, _ := request(t, ts, call.method, call.path, call.body,
			map[string]string{"Authorization": "Bearer " + planner.Secret})
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s = %d, want 403 for a sync-only token",
				call.method, call.path, resp.StatusCode)
		}
	}
}

// Assertion: a sync batch is attributed to the plugin and is undoable, which
// is what makes a bad import recoverable rather than permanent.
func TestASyncBatchIsUndoable(t *testing.T) {
	ts := newServer(t)

	tok, err := ts.store.CreateToken(t.Context(), "planner", "plugin:planner",
		[]string{api.ScopeSyncPrefix + "planner"}, ts.now)
	if err != nil {
		t.Fatal(err)
	}
	resp, out := request(t, ts, http.MethodPost, "/api/v1/sync/planner", map[string]any{
		"items": []map[string]any{
			{"external_id": "PLAN-1", "title": "Renew the certificate", "status": "todo", "rev": "1"},
		},
	}, map[string]string{"Authorization": "Bearer " + tok.Secret})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("sync = %d: %s", resp.StatusCode, out)
	}

	events, err := ts.store.Events(t.Context(), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, e := range events {
		if e.Actor == "plugin:planner" {
			found = true
		}
	}
	if !found {
		t.Fatal("the sync wrote no event attributed to the plugin")
	}

	// Undo is scoped to the actor, so undoing as the plugin reverses the
	// import without touching your own last change.
	if _, err := ts.store.Undo(t.Context(), "plugin:planner", ts.now); err != nil {
		t.Errorf("undoing a plugin batch: %v", err)
	}
}

// Assertion: nothing over HTTP can delete a task permanently.
//
// `tdd reset tasks` is the one hard delete in td and it is a command on the
// server, never a route. That is the property that makes an operator-only
// wrecking tool acceptable: a token cannot reach it, and neither can a
// compromised client or a confused agent.
func TestNoRouteHardDeletesATask(t *testing.T) {
	ts := newServer(t)

	before, err := ts.store.CountTasks(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if before == 0 {
		t.Fatal("no tasks to try to delete")
	}

	// DELETE on a task is a drop, not a delete: the activity feed is supposed
	// to show what you abandoned.
	if resp, _ := do(t, ts, http.MethodDelete, "/api/v1/tasks/103", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("drop = %d", resp.StatusCode)
	}
	after, err := ts.store.CountTasks(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("%d tasks before a drop, %d after: something hard deleted one", before, after)
	}

	// And no route spelled like a reset exists at all.
	for _, path := range []string{
		"/api/v1/reset", "/api/v1/tasks/reset", "/api/v1/reset/tasks",
		"/api/v1/plugins/planner/reset", "/w/reset",
	} {
		for _, method := range []string{http.MethodPost, http.MethodDelete} {
			resp, _ := do(t, ts, method, path, map[string]any{})
			if resp.StatusCode != http.StatusNotFound {
				t.Errorf("%s %s = %d, want 404: a wrecking route must not exist",
					method, path, resp.StatusCode)
			}
		}
	}
}
