package server_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/mcpsrv"
	"github.com/harpchad/td/internal/oauth"
	"github.com/harpchad/td/internal/server"
)

// The whole flow, end to end. BUILD-SPEC's definition of done for this work
// is "td connects as a claude.ai custom connector and a tool call round-trips,
// and nothing short of it counts", so the last test here does exactly that
// sequence: register, authorize, consent, exchange, call a tool.

// claudeRedirect is the redirect URI claude.ai actually uses. It is here as a
// literal because getting it wrong is a deployment failure that looks like a
// bug in this code.
const claudeRedirect = "https://claude.ai/api/mcp/auth_callback"

// pkce is one client's verifier and the challenge derived from it.
type pkce struct {
	verifier  string
	challenge string
}

func newPKCE() pkce {
	verifier := "a-code-verifier-of-a-legal-length-0123456789abcdef"
	sum := sha256.Sum256([]byte(verifier))
	return pkce{verifier: verifier, challenge: base64.RawURLEncoding.EncodeToString(sum[:])}
}

// registerClient runs Dynamic Client Registration and returns the id and
// secret.
func registerClient(t *testing.T, ts *harness, scope string) (id, secret string) {
	t.Helper()
	resp, body := doAnon(t, ts, http.MethodPost, server.RegisterPath, map[string]any{
		"client_name":   "Claude",
		"redirect_uris": []string{claudeRedirect},
		"scope":         scope,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register = %d: %s", resp.StatusCode, body)
	}
	var out struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}
	decodeInto(t, body, &out)
	return out.ClientID, out.ClientSecret
}

// authorizeQuery builds a valid /authorize query.
func authorizeQuery(ts *harness, clientID, scope string, p pkce) url.Values {
	return url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {claudeRedirect},
		"scope":                 {scope},
		"state":                 {"opaque-state"},
		"resource":              {ts.srv.ResourceURL()},
		"code_challenge":        {p.challenge},
		"code_challenge_method": {"S256"},
	}
}

// consent approves an authorization request and returns the redirect the
// browser is sent to.
func consent(t *testing.T, ts *harness, session string, q url.Values, granted []string) *url.URL {
	t.Helper()
	form := url.Values{"request": {q.Encode()}, "decision": {"approve"}}
	for _, scope := range granted {
		form.Add("scope", scope)
	}
	resp := postForm(t, ts, session, "/w/approve", form)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("approve = %d, want a redirect", resp.StatusCode)
	}
	target, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	return target
}

// exchange runs the token endpoint.
func exchange(t *testing.T, ts *harness, form url.Values) (respMeta, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		ts.URL+server.TokenPath, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body := make([]byte, 1<<16)
	n, _ := resp.Body.Read(body)
	return respMeta{StatusCode: resp.StatusCode, Header: resp.Header}, body[:n]
}

// TestAuthorizationServerMetadata covers RFC 8414 and the three things the
// spec asks to be advertised by name.
func TestAuthorizationServerMetadata(t *testing.T) {
	ts := newServer(t)

	resp, body := doAnon(t, ts, http.MethodGet, server.ASMetadataPath, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 with no credential", resp.StatusCode)
	}

	var doc struct {
		Issuer                        string   `json:"issuer"`
		AuthorizationEndpoint         string   `json:"authorization_endpoint"`
		TokenEndpoint                 string   `json:"token_endpoint"`
		JWKSURI                       string   `json:"jwks_uri"`
		GrantTypesSupported           []string `json:"grant_types_supported"`
		CodeChallengeMethodsSupported []string `json:"code_challenge_methods_supported"`
		ResourceIndicators            bool     `json:"resource_indicators_supported"`
		ISSParameter                  bool     `json:"authorization_response_iss_parameter_supported"`
	}
	decodeInto(t, body, &doc)

	if doc.Issuer != "https://td.example.com" {
		t.Errorf("issuer = %q", doc.Issuer)
	}
	if doc.AuthorizationEndpoint != "https://td.example.com/authorize" {
		t.Errorf("authorization_endpoint = %q", doc.AuthorizationEndpoint)
	}
	if doc.TokenEndpoint != "https://td.example.com/token" {
		t.Errorf("token_endpoint = %q", doc.TokenEndpoint)
	}
	if doc.JWKSURI == "" {
		t.Error("no jwks_uri, so nothing can verify a token")
	}

	// client_credentials is not supported and must not be advertised: a
	// machine-to-machine grant has no user in the loop.
	if contains(doc.GrantTypesSupported, "client_credentials") {
		t.Error("client_credentials is advertised")
	}
	// S256 only. plain is not weak arithmetic, it is no arithmetic.
	if len(doc.CodeChallengeMethodsSupported) != 1 || doc.CodeChallengeMethodsSupported[0] != "S256" {
		t.Errorf("code_challenge_methods_supported = %v", doc.CodeChallengeMethodsSupported)
	}
	if !doc.ResourceIndicators {
		t.Error("resource_indicators_supported is false; RFC 8707 is mandatory")
	}
	if !doc.ISSParameter {
		t.Error("authorization_response_iss_parameter_supported is false; RFC 9207 stops a mix-up attack")
	}
}

// TestTheJWKSHasTwoLiveKeys covers the rotation requirement: keeping two live
// means rotating does not invalidate every session.
func TestTheJWKSHasTwoLiveKeys(t *testing.T) {
	ts := newServer(t)

	if _, err := ts.store.EnsureSigningKeys(t.Context(), ts.now); err != nil {
		t.Fatal(err)
	}

	resp, body := doAnon(t, ts, http.MethodGet, server.JWKSPath, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var doc oauth.JWKS
	decodeInto(t, body, &doc)
	if len(doc.Keys) != 2 {
		t.Fatalf("%d keys, want two live", len(doc.Keys))
	}
	if doc.Keys[0].Kid == doc.Keys[1].Kid {
		t.Error("both keys have the same kid")
	}
	// No private halves, ever.
	if strings.Contains(string(body), `"d"`) {
		t.Fatal("the JWKS contains a private key")
	}

	// And rotation keeps the previous key verifying.
	oldKid := doc.Keys[0].Kid
	if err := ts.store.RotateSigningKey(t.Context(), ts.now); err != nil {
		t.Fatal(err)
	}
	_, body = doAnon(t, ts, http.MethodGet, server.JWKSPath, nil)
	decodeInto(t, body, &doc)
	if len(doc.Keys) != 2 {
		t.Errorf("%d keys after rotation, want two", len(doc.Keys))
	}
	for _, k := range doc.Keys {
		if k.Kid == oldKid {
			t.Error("the key that was rotated out is still published")
		}
	}
}

// TestAuthorizeRejectsPKCEPlainAndMissing is a security assertion from
// section 15, stated there in exactly those words.
func TestAuthorizeRejectsPKCEPlainAndMissing(t *testing.T) {
	ts := newServer(t)
	session := login(t, ts)
	clientID, _ := registerClient(t, ts, "td:read")
	p := newPKCE()

	for name, mutate := range map[string]func(url.Values){
		"missing challenge": func(q url.Values) { q.Del("code_challenge") },
		"empty challenge":   func(q url.Values) { q.Set("code_challenge", "") },
		"missing method":    func(q url.Values) { q.Del("code_challenge_method") },
		"plain":             func(q url.Values) { q.Set("code_challenge_method", "plain") },
		"PLAIN":             func(q url.Values) { q.Set("code_challenge_method", "PLAIN") },
	} {
		q := authorizeQuery(ts, clientID, "td:read", p)
		mutate(q)

		resp := getWithSession(t, ts, session, server.AuthorizePath+"?"+q.Encode())
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("%s: status = %d, want the error sent back to the client", name, resp.StatusCode)
		}
		target, err := url.Parse(resp.Header.Get("Location"))
		if err != nil {
			t.Fatal(err)
		}
		if target.Query().Get("error") == "" {
			t.Errorf("%s: no error in the redirect: %s", name, target)
		}
		if target.Query().Get("code") != "" {
			t.Errorf("%s: a code was issued anyway", name)
		}
	}
}

// TestAuthorizeRefusesAnUnregisteredRedirect renders the failure rather than
// redirecting. Sending a code, or even an error, to a URI nobody validated is
// how an authorization code ends up somewhere else.
func TestAuthorizeRefusesAnUnregisteredRedirect(t *testing.T) {
	ts := newServer(t)
	session := login(t, ts)
	clientID, _ := registerClient(t, ts, "td:read")
	p := newPKCE()

	for _, evil := range []string{
		"https://evil.example.com/callback",
		claudeRedirect + "/../evil",
		claudeRedirect + "?x=1",
		"https://claude.ai.evil.example.com/api/mcp/auth_callback",
		"",
	} {
		q := authorizeQuery(ts, clientID, "td:read", p)
		q.Set("redirect_uri", evil)

		resp := getWithSession(t, ts, session, server.AuthorizePath+"?"+q.Encode())
		if resp.StatusCode == http.StatusSeeOther {
			t.Errorf("redirect_uri %q produced a redirect to %q",
				evil, resp.Header.Get("Location"))
		}
	}
}

// TestTheResourceParameterIsRequiredAndChecked covers RFC 8707, which the
// 2026-07-28 revision makes mandatory.
func TestTheResourceParameterIsRequiredAndChecked(t *testing.T) {
	ts := newServer(t)
	session := login(t, ts)
	clientID, _ := registerClient(t, ts, "td:read")
	p := newPKCE()

	for name, value := range map[string]string{
		"missing":     "",
		"the issuer":  "https://td.example.com",
		"another one": "https://evil.example.com/mcp",
		"a prefix":    "https://td.example.com/mc",
	} {
		q := authorizeQuery(ts, clientID, "td:read", p)
		if value == "" {
			q.Del("resource")
		} else {
			q.Set("resource", value)
		}

		resp := getWithSession(t, ts, session, server.AuthorizePath+"?"+q.Encode())
		target, err := url.Parse(resp.Header.Get("Location"))
		if err != nil {
			t.Fatal(err)
		}
		if got := target.Query().Get("error"); got != "invalid_target" {
			t.Errorf("resource %s: error = %q, want invalid_target", name, got)
		}
	}
}

// TestClientCredentialsIsRefused is a hard rule from the spec, and the
// refusal names it rather than lumping it in with the unknown grants.
func TestClientCredentialsIsRefused(t *testing.T) {
	ts := newServer(t)
	clientID, secret := registerClient(t, ts, "td:read td:write")

	resp, body := exchange(t, ts, url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {clientID},
		"client_secret": {secret},
		"scope":         {"td:write"},
		"resource":      {ts.srv.ResourceURL()},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, body)
	}
	var out struct {
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	decodeInto(t, body, &out)
	if out.Error != "unsupported_grant_type" {
		t.Errorf("error = %q", out.Error)
	}
	if !strings.Contains(out.Description, "client_credentials") {
		t.Errorf("the refusal does not name the grant: %q", out.Description)
	}
	if strings.Contains(string(body), "access_token") {
		t.Fatal("a token came back")
	}
}

// TestACodeIsSingleUse. A second exchange has to fail, or an intercepted code
// is worth as much as the token it buys.
func TestACodeIsSingleUse(t *testing.T) {
	ts := newServer(t)
	session := login(t, ts)
	clientID, secret := registerClient(t, ts, "td:read")
	p := newPKCE()

	q := authorizeQuery(ts, clientID, "td:read", p)
	target := consent(t, ts, session, q, []string{"td:read"})
	code := target.Query().Get("code")
	if code == "" {
		t.Fatalf("no code in %s", target)
	}

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {claudeRedirect},
		"client_id":     {clientID},
		"client_secret": {secret},
		"code_verifier": {p.verifier},
	}
	if resp, body := exchange(t, ts, form); resp.StatusCode != http.StatusOK {
		t.Fatalf("first exchange = %d: %s", resp.StatusCode, body)
	}
	resp, body := exchange(t, ts, form)
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("the same code was exchanged twice: %s", body)
	}
}

// TestTheWrongVerifierIsRefused, which is the whole point of PKCE.
func TestTheWrongVerifierIsRefused(t *testing.T) {
	ts := newServer(t)
	session := login(t, ts)
	clientID, secret := registerClient(t, ts, "td:read")
	p := newPKCE()

	q := authorizeQuery(ts, clientID, "td:read", p)
	code := consent(t, ts, session, q, []string{"td:read"}).Query().Get("code")

	resp, body := exchange(t, ts, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {claudeRedirect},
		"client_id":     {clientID},
		"client_secret": {secret},
		"code_verifier": {"a-different-verifier-of-a-legal-length-0123456789"},
	})
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("a wrong verifier bought a token: %s", body)
	}
}

// TestTheConsentScreenCanGrantLess is what a consent screen is for. A screen
// you can only agree with is a notification.
func TestTheConsentScreenCanGrantLess(t *testing.T) {
	ts := newServer(t)
	session := login(t, ts)
	clientID, secret := registerClient(t, ts, "td:read td:capture td:write")
	p := newPKCE()

	q := authorizeQuery(ts, clientID, "td:read td:capture td:write", p)

	// The screen shows all three, checked.
	resp := getWithSession(t, ts, session, server.AuthorizePath+"?"+q.Encode())
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the consent screen answered %d", resp.StatusCode)
	}

	// Approve only two of them.
	code := consent(t, ts, session, q, []string{"td:read", "td:capture"}).Query().Get("code")
	_, body := exchange(t, ts, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {claudeRedirect},
		"client_id":     {clientID},
		"client_secret": {secret},
		"code_verifier": {p.verifier},
	})
	var out struct {
		AccessToken string `json:"access_token"`
		Scope       string `json:"scope"`
	}
	decodeInto(t, body, &out)
	if out.Scope != "td:read td:capture" {
		t.Errorf("scope = %q, want only what was approved", out.Scope)
	}

	// And the token carries only those, so the narrowing is not cosmetic.
	keys, err := ts.store.SigningKeys(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	claims, err := oauth.Verifier{
		Issuer: "https://td.example.com", Audience: ts.srv.ResourceURL(), Keys: keys,
	}.Verify(out.AccessToken, ts.now)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if contains(claims.Scopes(), "td:write") {
		t.Error("the token carries a scope the consent screen did not grant")
	}
}

// TestRefreshRotatesAndNarrows covers the refresh grant. Rotation means a
// stolen refresh token is usable at most once.
func TestRefreshRotatesAndNarrows(t *testing.T) {
	ts := newServer(t)
	session := login(t, ts)
	clientID, secret := registerClient(t, ts, "td:read td:capture")
	p := newPKCE()

	q := authorizeQuery(ts, clientID, "td:read td:capture", p)
	code := consent(t, ts, session, q, []string{"td:read", "td:capture"}).Query().Get("code")

	_, body := exchange(t, ts, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {claudeRedirect},
		"client_id":     {clientID},
		"client_secret": {secret},
		"code_verifier": {p.verifier},
	})
	var first struct {
		RefreshToken string `json:"refresh_token"`
	}
	decodeInto(t, body, &first)
	if first.RefreshToken == "" {
		t.Fatal("no refresh token")
	}

	refreshForm := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {first.RefreshToken},
		"client_id":     {clientID},
		"client_secret": {secret},
		"scope":         {"td:read"},
	}
	resp, body := exchange(t, ts, refreshForm)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("refresh = %d: %s", resp.StatusCode, body)
	}
	var second struct {
		RefreshToken string `json:"refresh_token"`
		Scope        string `json:"scope"`
	}
	decodeInto(t, body, &second)

	if second.RefreshToken == first.RefreshToken {
		t.Error("the refresh token was not rotated")
	}
	if second.Scope != "td:read" {
		t.Errorf("scope = %q, want the narrowed set", second.Scope)
	}

	// The old one is dead.
	if resp, _ := exchange(t, ts, refreshForm); resp.StatusCode == http.StatusOK {
		t.Error("the rotated-out refresh token still works")
	}

	// And a refresh cannot widen beyond what was granted.
	widen := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {second.RefreshToken},
		"client_id":     {clientID},
		"client_secret": {secret},
		"scope":         {"td:write"},
	}
	if resp, body := exchange(t, ts, widen); resp.StatusCode == http.StatusOK {
		t.Errorf("a refresh widened the scopes: %s", body)
	}
}

// TestRevokingAGrantCutsOffTheClient, which is what the settings page button
// is for. claude.ai holds a refresh token for your task list.
func TestRevokingAGrantCutsOffTheClient(t *testing.T) {
	ts := newServer(t)
	session := login(t, ts)
	clientID, secret := registerClient(t, ts, "td:read")
	p := newPKCE()

	q := authorizeQuery(ts, clientID, "td:read", p)
	code := consent(t, ts, session, q, []string{"td:read"}).Query().Get("code")
	_, body := exchange(t, ts, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {claudeRedirect},
		"client_id":     {clientID},
		"client_secret": {secret},
		"code_verifier": {p.verifier},
	})
	var issued struct {
		RefreshToken string `json:"refresh_token"`
	}
	decodeInto(t, body, &issued)

	// The settings page lists it next to the static tokens.
	_, html := page(t, ts, session, "/settings")
	if !strings.Contains(html, "Connected apps") || !strings.Contains(html, "Claude") {
		t.Error("the grant is not listed on the settings page")
	}

	grants, err := ts.store.Grants(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(grants) != 1 {
		t.Fatalf("%d grants", len(grants))
	}
	if resp := postForm(t, ts, session, "/w/grants/"+grants[0].ID+"/revoke", url.Values{}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("revoke = %d", resp.StatusCode)
	}

	// The refresh token stops working immediately.
	resp, _ := exchange(t, ts, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {issued.RefreshToken},
		"client_id":     {clientID},
		"client_secret": {secret},
	})
	if resp.StatusCode == http.StatusOK {
		t.Error("a revoked grant still refreshes")
	}
}

// TestTheAuthorizationResponseCarriesISS covers RFC 9207. Without it a client
// talking to two authorization servers cannot tell which one answered.
func TestTheAuthorizationResponseCarriesISS(t *testing.T) {
	ts := newServer(t)
	session := login(t, ts)
	clientID, _ := registerClient(t, ts, "td:read")
	p := newPKCE()

	q := authorizeQuery(ts, clientID, "td:read", p)
	target := consent(t, ts, session, q, []string{"td:read"})

	if got := target.Query().Get("iss"); got != "https://td.example.com" {
		t.Errorf("iss = %q, want the issuer", got)
	}
	if got := target.Query().Get("state"); got != "opaque-state" {
		t.Errorf("state = %q, want it returned untouched", got)
	}

	// And on the error path too, since a client has to attribute a failure
	// just as much as a success.
	bad := authorizeQuery(ts, clientID, "td:read", p)
	bad.Set("code_challenge_method", "plain")
	resp := getWithSession(t, ts, session, server.AuthorizePath+"?"+bad.Encode())
	errTarget, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if errTarget.Query().Get("iss") != "https://td.example.com" {
		t.Error("the error redirect carries no iss")
	}
}

// TestAnOAuthTokenReachesMCPAndATooCallRoundTrips is the definition of done
// for this phase, and BUILD-SPEC says nothing short of it counts: register as
// a claude.ai custom connector would, complete the grant, and call a tool
// with the access token that comes out.
func TestAnOAuthTokenReachesMCPAndATooCallRoundTrips(t *testing.T) {
	ts := newServer(t)
	session := login(t, ts)
	clientID, secret := registerClient(t, ts, "td:read td:capture")
	p := newPKCE()

	q := authorizeQuery(ts, clientID, "td:read td:capture", p)
	code := consent(t, ts, session, q, []string{"td:read", "td:capture"}).Query().Get("code")

	resp, body := exchange(t, ts, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {claudeRedirect},
		"client_id":     {clientID},
		"client_secret": {secret},
		"code_verifier": {p.verifier},
		"resource":      {ts.srv.ResourceURL()},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("token = %d: %s", resp.StatusCode, body)
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Error("the token response is cacheable")
	}
	var issued struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int    `json:"expires_in"`
	}
	decodeInto(t, body, &issued)
	if issued.TokenType != "Bearer" || issued.ExpiresIn <= 0 {
		t.Errorf("token response = %+v", issued)
	}

	// The audience is the MCP URL exactly, which is what stops this token
	// from being replayed at another server.
	keys, err := ts.store.SigningKeys(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	claims, err := oauth.Verifier{
		Issuer: "https://td.example.com", Audience: ts.srv.ResourceURL(), Keys: keys,
	}.Verify(issued.AccessToken, ts.now)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if claims.Audience != "https://td.example.com/mcp" {
		t.Errorf("aud = %q", claims.Audience)
	}

	// And now the thing that counts: a tool call over MCP with that token.
	mcpSession := connect(t, ts, issued.AccessToken)
	var out struct {
		Tasks []struct {
			Num   int64  `json:"num"`
			Title string `json:"title"`
		} `json:"tasks"`
		Total int `json:"total"`
	}
	if res := call(t, mcpSession, "whats_next", map[string]any{"limit": 3}, &out); res.IsError {
		t.Fatalf("whats_next: %s", errorText(res))
	}
	if len(out.Tasks) == 0 {
		t.Fatal("the round trip returned nothing")
	}

	// Capture works, and lands as the client rather than as you.
	var captured struct {
		Task struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"task"`
	}
	if res := call(t, mcpSession, "capture",
		map[string]any{"title": "something Claude heard"}, &captured); res.IsError {
		t.Fatalf("capture: %s", errorText(res))
	}
	if captured.Task.Status != api.StatusInbox {
		t.Errorf("capture landed in %s", captured.Task.Status)
	}

	// Write was never granted, so it is refused before the call is dispatched,
	// with a challenge naming the scope to go and get. A tool error would say
	// no and leave the client with nothing to do about it.
	resp, body = rawToolCall(t, ts, issued.AccessToken, "create_task",
		map[string]any{"title": "nope"})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("create_task on a grant with no write scope = %d: %s", resp.StatusCode, body)
	}
	challenge := resp.Header.Get("WWW-Authenticate")
	for _, want := range []string{`error="insufficient_scope"`, `scope="td:write"`, "resource_metadata="} {
		if !strings.Contains(challenge, want) {
			t.Errorf("the challenge is missing %s: %s", want, challenge)
		}
	}
}

// rawToolCall posts a tools/call without the SDK, so a test can read the HTTP
// status and headers of a refusal rather than only the tool result.
func rawToolCall(t *testing.T, ts *harness, token, tool string, args map[string]any) (respMeta, []byte) {
	t.Helper()
	payload := map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{
			"name": tool, "arguments": args,
			"_meta": map[string]any{
				"io.modelcontextprotocol/protocolVersion":    mcpsrv.Revision,
				"io.modelcontextprotocol/clientCapabilities": map[string]any{},
				"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "test", "version": "1"},
			},
		},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		ts.URL+server.MCPPath, bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Mcp-Protocol-Version", mcpsrv.Revision)
	req.Header.Set("Mcp-Method", "tools/call")
	req.Header.Set("Mcp-Name", tool)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return respMeta{StatusCode: resp.StatusCode, Header: resp.Header}, out
}

// TestATokenForAnotherAudienceIsRefusedAtMCP is the replay case the spec
// calls out: audience mismatch is the failure people hit, because it is what
// stops a token minted for another server from being used here.
func TestATokenForAnotherAudienceIsRefusedAtMCP(t *testing.T) {
	ts := newServer(t)

	keys, err := ts.store.EnsureSigningKeys(t.Context(), ts.now)
	if err != nil {
		t.Fatal(err)
	}
	// Signed by this server's own key, so only the audience is wrong.
	forged, err := oauth.Sign(keys[0], oauth.Claims{
		Issuer: "https://td.example.com", Subject: "me",
		Audience: "https://other.example.com/mcp",
		ClientID: "someone", Scope: "td:read td:write",
		IssuedAt: ts.now.Unix(), NotBefore: ts.now.Unix(),
		Expires: ts.now.Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}

	resp, body := request(t, ts, http.MethodPost, server.MCPPath,
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"},
		map[string]string{"Authorization": "Bearer " + forged})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401: %s", resp.StatusCode, body)
	}
	if resp.Header.Get("WWW-Authenticate") == "" {
		t.Error("the refusal carries no discovery chain")
	}
}

// getWithSession fetches a URL as a signed-in browser without following
// redirects, so the test can read the Location.
func getWithSession(t *testing.T, ts *harness, session, path string) respMeta {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "text/html")
	req.AddCookie(&http.Cookie{Name: "td_session", Value: session})

	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return respMeta{StatusCode: resp.StatusCode, Header: resp.Header}
}
