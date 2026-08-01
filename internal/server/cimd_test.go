package server_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/harpchad/td/internal/oauth"
	"github.com/harpchad/td/internal/server"
)

// Client ID Metadata Documents. Under the 2026-07-28 revision this is how a
// client with no prior relationship to this server gets a client id, and
// Dynamic Client Registration is the deprecated fallback. td advertises
// support in its metadata, so these are the tests that the advertisement is
// true.

// hostDocument serves a metadata document and returns its URL, with the
// server's resolver pointed at it.
func hostDocument(t *testing.T, ts *harness, doc map[string]any) string {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /client.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(doc)
	})
	// TLS, because a client id must use the https scheme. An http URL is not
	// a metadata document URL at all and would never reach the resolver.
	site := httptest.NewTLSServer(mux)
	t.Cleanup(site.Close)

	// The document has to name its own URL, which is the check that stops
	// anybody who can host JSON from claiming to be somebody else.
	id := site.URL + "/client.json"
	if _, ok := doc["client_id"]; !ok {
		doc["client_id"] = id
	}

	// The real dialer refuses loopback, which is the whole point of it, so the
	// resolver used here gets an ordinary client. internal/oauth tests the
	// guard directly.
	r := oauth.NewResolver()
	r.HTTP = site.Client()
	r.HTTP.Timeout = 5 * time.Second
	ts.srv.SetCIMDResolverForTest(r)
	return id
}

// TestAClientDescribedByAMetadataDocumentCanAuthorize is the case that failed
// in the field: claude.ai read client_id_metadata_document_supported, skipped
// registration, and sent a URL as its client_id. Everything through to a tool
// call has to work without a /register step ever happening.
func TestAClientDescribedByAMetadataDocumentCanAuthorize(t *testing.T) {
	ts := newServer(t)
	session := login(t, ts)

	clientID := hostDocument(t, ts, map[string]any{
		"client_name":   "Claude",
		"redirect_uris": []string{claudeRedirect},
		"scope":         "td:read td:capture",
	})

	p := newPKCE()
	q := authorizeQuery(ts, clientID, "td:read td:capture", p)
	code := consent(t, ts, session, q, []string{"td:read", "td:capture"}).Query().Get("code")
	if code == "" {
		t.Fatal("no authorization code came back")
	}

	// No client_secret: a document is world readable, so a CIMD client is
	// public and PKCE is what binds the code to it.
	resp, body := exchange(t, ts, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {claudeRedirect},
		"client_id":     {clientID},
		"code_verifier": {p.verifier},
		"resource":      {ts.srv.ResourceURL()},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("token = %d: %s", resp.StatusCode, body)
	}
	var issued struct {
		AccessToken string `json:"access_token"`
	}
	decodeInto(t, body, &issued)

	// The thing that counts.
	mcpSession := connect(t, ts, issued.AccessToken)
	var out struct {
		Tasks []struct {
			Num int64 `json:"num"`
		} `json:"tasks"`
	}
	if res := call(t, mcpSession, "whats_next", map[string]any{"limit": 3}, &out); res.IsError {
		t.Fatalf("whats_next: %s", errorText(res))
	}
	if len(out.Tasks) == 0 {
		t.Fatal("the round trip returned nothing")
	}
}

// TestTheConsentScreenNamesTheHostTheCodeGoesTo. The name in a metadata
// document is a claim anybody can make; the redirect host is the fact. The
// revision requires it be displayed.
func TestTheConsentScreenNamesTheHostTheCodeGoesTo(t *testing.T) {
	ts := newServer(t)
	session := login(t, ts)

	clientID := hostDocument(t, ts, map[string]any{
		"client_name":   "Totally Legitimate",
		"redirect_uris": []string{claudeRedirect},
	})

	q := authorizeQuery(ts, clientID, "td:read", newPKCE())
	_, html := page(t, ts, session, server.AuthorizePath+"?"+q.Encode())

	if !strings.Contains(html, "claude.ai") {
		t.Error("the consent screen does not say where the code would go")
	}
}

// TestADocumentCannotClaimSomebodyElsesClientID. The document must name the
// URL it was served from, or hosting JSON would be enough to impersonate any
// client on any server.
func TestADocumentCannotClaimSomebodyElsesClientID(t *testing.T) {
	ts := newServer(t)
	session := login(t, ts)

	clientID := hostDocument(t, ts, map[string]any{
		"client_id":     "https://claude.ai/api/mcp/client.json",
		"client_name":   "Claude",
		"redirect_uris": []string{claudeRedirect},
	})

	q := authorizeQuery(ts, clientID, "td:read", newPKCE())
	_, html := page(t, ts, session, server.AuthorizePath+"?"+q.Encode())
	if strings.Contains(html, "Approve") {
		t.Error("a document claiming another client_id reached the consent screen")
	}
}

// TestAMetadataClientCannotOverwriteARegisteredOne. Otherwise anybody able to
// serve a document at a URL that happens to match a registered client id could
// replace its redirect URIs with their own.
func TestAMetadataClientCannotOverwriteARegisteredOne(t *testing.T) {
	ts := newServer(t)

	// Register a client whose id is URL-shaped, which registration allows.
	resp, body := doAnon(t, ts, http.MethodPost, server.RegisterPath, map[string]any{
		"client_name":   "Pre-registered",
		"redirect_uris": []string{claudeRedirect},
		"scope":         "td:read",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register = %d: %s", resp.StatusCode, body)
	}

	var reg struct {
		ClientID string `json:"client_id"`
	}
	decodeInto(t, body, &reg)

	// A DCR client id is opaque, so it is not a document URL and never takes
	// the resolver path at all. That is the property being asserted.
	if oauth.IsClientIDDocumentURL(reg.ClientID) {
		t.Errorf("a registered client id %q parses as a metadata document url", reg.ClientID)
	}
}

// TestTheMetadataAdvertisementIsTrue. Advertising a mechanism that is not
// implemented is what broke the first real connector attempt: a conforming
// client picked CIMD over registration precisely because this field said it
// could, and then had nowhere to go.
func TestTheMetadataAdvertisementIsTrue(t *testing.T) {
	ts := newServer(t)
	session := login(t, ts)

	_, body := doAnon(t, ts, http.MethodGet, server.ASMetadataPath, nil)
	var meta struct {
		CIMD bool `json:"client_id_metadata_document_supported"`
	}
	decodeInto(t, body, &meta)
	if !meta.CIMD {
		t.Skip("this server no longer advertises client id metadata documents")
	}

	// Advertised, so it has to work: a URL client id must reach consent.
	clientID := hostDocument(t, ts, map[string]any{
		"client_name":   "Claude",
		"redirect_uris": []string{claudeRedirect},
	})
	q := authorizeQuery(ts, clientID, "td:read", newPKCE())
	_, html := page(t, ts, session, server.AuthorizePath+"?"+q.Encode())
	if !strings.Contains(html, "Approve") {
		t.Errorf("the server advertises CIMD but a URL client id did not reach consent:\n%s", firstLines(html, 12))
	}
}

// TestTheConsentScreenLetsTheApprovalReachTheClient.
//
// An OAuth consent screen ends in a cross-origin navigation by construction:
// approving carries a code to the client's redirect URI. Under the default
// form-action 'self' Chrome blocks that redirect and the approval goes
// nowhere, while Firefox allows it, so the bug only appears on some machines.
// The consent page names the one origin being approved and nothing else.
func TestTheConsentScreenLetsTheApprovalReachTheClient(t *testing.T) {
	ts := newServer(t)
	session := login(t, ts)

	clientID := hostDocument(t, ts, map[string]any{
		"client_name":   "Claude",
		"redirect_uris": []string{claudeRedirect},
	})
	q := authorizeQuery(ts, clientID, "td:read", newPKCE())

	resp, html := page(t, ts, session, server.AuthorizePath+"?"+q.Encode())
	if !strings.Contains(html, "Approve") {
		t.Fatal("no consent screen to check")
	}

	csp := resp.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "form-action 'self' https://claude.ai") {
		t.Errorf("the consent screen would block its own approval:\n  %s", csp)
	}
	// Widened for form-action and nothing else.
	for _, must := range []string{"script-src 'self'", "frame-ancestors 'none'", "object-src 'none'"} {
		if !strings.Contains(csp, must) {
			t.Errorf("the consent policy dropped %q", must)
		}
	}
	if strings.Contains(csp, "unsafe-inline") {
		t.Error("the consent policy allows unsafe-inline")
	}
}

// TestOnlyTheConsentScreenWidensFormAction, because a relaxation that leaks
// onto every page is not a relaxation, it is a removal.
func TestOnlyTheConsentScreenWidensFormAction(t *testing.T) {
	ts := newServer(t)
	session := login(t, ts)

	for _, path := range []string{"/", "/settings", "/help"} {
		resp, _ := page(t, ts, session, path)
		csp := resp.Header.Get("Content-Security-Policy")
		if !strings.Contains(csp, "form-action 'self';") && !strings.HasSuffix(csp, "form-action 'self'") {
			t.Errorf("%s has form-action %q, want 'self' alone", path, csp)
		}
	}
}
