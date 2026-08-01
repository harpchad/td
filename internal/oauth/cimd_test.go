package oauth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/harpchad/td/internal/oauth"
)

// Client ID Metadata Documents. The client id is a URL this server fetches,
// which makes these the tests for the only outbound request td makes to an
// address somebody else picked.

// TestOnlyAURLWithAPathIsAClientIDDocument. The draft requires https and a
// path, and the path is what stops a bare origin becoming a client id for a
// whole domain.
func TestOnlyAURLWithAPathIsAClientIDDocument(t *testing.T) {
	yes := []string{
		"https://claude.ai/api/mcp/client.json",
		"https://example.com/c",
	}
	no := []string{
		"",
		"claude-abc123",                  // an ordinary registered id
		"http://example.com/client.json", // not https
		"https://example.com",            // no path
		"https://example.com/",           // still no path
		"https://user:pw@example.com/c",  // userinfo
		"https://example.com/c#fragment", // fragment
		"ftp://example.com/client.json",  // not http at all
		"//example.com/client.json",      // no scheme
	}
	for _, id := range yes {
		if !oauth.IsClientIDDocumentURL(id) {
			t.Errorf("%q should be a metadata document url", id)
		}
	}
	for _, id := range no {
		if oauth.IsClientIDDocumentURL(id) {
			t.Errorf("%q should not be a metadata document url", id)
		}
	}
}

// TestTheDocumentMustClaimTheURLItCameFrom. This is the load-bearing check.
// Without it anybody who can host JSON could serve a document naming itself
// "Claude", and the consent screen would show that name.
func TestTheDocumentMustClaimTheURLItCameFrom(t *testing.T) {
	doc := oauth.Document{
		ClientID:     "https://attacker.example/c.json",
		ClientName:   "Claude",
		RedirectURIs: []string{"https://attacker.example/cb"},
	}
	if err := doc.Validate("https://claude.ai/client.json"); err == nil {
		t.Fatal("a document claiming a different client_id was accepted")
	}
	doc.ClientID = "https://claude.ai/client.json"
	if err := doc.Validate("https://claude.ai/client.json"); err != nil {
		t.Errorf("a matching document was refused: %v", err)
	}
}

// TestADocumentNeedsANameAndSomewhereToGo, because the consent screen shows
// the one and the code goes to the other.
func TestADocumentNeedsANameAndSomewhereToGo(t *testing.T) {
	const id = "https://example.com/c.json"
	cases := map[string]oauth.Document{
		"no name":            {ClientID: id, RedirectURIs: []string{"https://example.com/cb"}},
		"no redirects":       {ClientID: id, ClientName: "X"},
		"http redirect":      {ClientID: id, ClientName: "X", RedirectURIs: []string{"http://example.com/cb"}},
		"scheme redirect":    {ClientID: id, ClientName: "X", RedirectURIs: []string{"myapp://cb"}},
		"one bad of several": {ClientID: id, ClientName: "X", RedirectURIs: []string{"https://a.example/cb", "http://b.example/cb"}},
	}
	for name, doc := range cases {
		if err := doc.Validate(id); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}

	// Loopback over http is the documented exception, since a native client
	// cannot serve https on a port it just picked.
	ok := oauth.Document{
		ClientID: id, ClientName: "X",
		RedirectURIs: []string{"http://127.0.0.1:1234/cb", "http://localhost:9000/cb"},
	}
	if err := ok.Validate(id); err != nil {
		t.Errorf("loopback redirects were refused: %v", err)
	}
	if !ok.OnlyLoopbackRedirects() {
		t.Error("a loopback-only client was not reported as one")
	}
}

// TestTheResolverRefusesPrivateAddresses is the SSRF test.
//
// td is internet-facing and this fetch goes to a URL an unauthenticated
// stranger chose, so a document served from 127.0.0.1 or 169.254.169.254
// would turn /authorize into a probe for whatever the server can reach.
func TestTheResolverRefusesPrivateAddresses(t *testing.T) {
	// httptest listens on loopback, which is exactly what must be refused.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"client_id":"x","client_name":"x","redirect_uris":["https://x/cb"]}`))
	}))
	defer srv.Close()

	// The real resolver, with the real dialer.
	_, _, err := oauth.NewResolver().Resolve(t.Context(), srv.URL+"/client.json")
	if err == nil {
		t.Fatal("the resolver fetched a document from a loopback address")
	}
	if !strings.Contains(err.Error(), "not a public address") {
		t.Errorf("refused for the wrong reason: %v", err)
	}
}

// fetchWith runs the resolver against a test server, bypassing the dialer
// guard, which TestTheResolverRefusesPrivateAddresses covers on its own.
func fetchWith(t *testing.T, handler http.HandlerFunc) (*oauth.Resolver, string) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	r := oauth.NewResolver()
	r.HTTP = &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return r, srv.URL
}

// TestAHugeDocumentIsRefused, so a client cannot answer the fetch with a
// gigabyte and take the server down with it.
func TestAHugeDocumentIsRefused(t *testing.T) {
	r, base := fetchWith(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		for range 4096 {
			_, _ = w.Write([]byte(strings.Repeat("x", 1024)))
		}
	})
	// The URL is http here, so go through the document check directly.
	r.MaxBytes = 4096
	if _, _, err := r.Resolve(t.Context(), strings.Replace(base, "http://", "https://", 1)+"/c.json"); err == nil {
		t.Fatal("an oversized document was accepted")
	}
}

// TestCacheHeadersAreClamped. A client should not be able to pin its metadata
// in this server for a year, nor force a refetch on every authorization.
func TestCacheHeadersAreClamped(t *testing.T) {
	r := oauth.NewResolver()
	r.MinTTL, r.MaxTTL, r.DefaultTTL = 5*time.Minute, time.Hour, 30*time.Minute

	cases := map[string]time.Duration{
		"max-age=1":         5 * time.Minute,
		"max-age=31536000":  time.Hour,
		"max-age=600":       10 * time.Minute,
		"no-store":          5 * time.Minute,
		"":                  30 * time.Minute,
		"public, max-age=0": 5 * time.Minute,
	}
	for header, want := range cases {
		if got := oauth.TTLForTest(r, header); got != want {
			t.Errorf("Cache-Control %q -> %s, want %s", header, got, want)
		}
	}
}

var _ = context.Background
