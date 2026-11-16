package server_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/harpchad/td/internal/server"
)

// The consent screen offers more than the client asked for, unchecked.
//
// The 401 challenge names read and capture and leaves write to a step-up.
// claude.ai asks for exactly what the challenge says and never steps up, so
// its connector could read and capture but never write. These are the tests
// that the owner can grant write on the screen without the wire asking every
// client for it.

// checkbox returns the consent screen's input for one scope, or "" when the
// screen does not offer it.
func checkbox(html, scope string) string {
	at := strings.Index(html, `value="`+scope+`"`)
	if at < 0 {
		return ""
	}
	end := strings.Index(html[at:], ">")
	if end < 0 {
		return ""
	}
	return html[at : at+end]
}

// TestTheConsentScreenOffersWriteUnchecked is the claude.ai case: a metadata
// document with no scope of its own, asking for read and capture because the
// challenge said so.
func TestTheConsentScreenOffersWriteUnchecked(t *testing.T) {
	ts := newServer(t)
	session := login(t, ts)
	clientID := hostDocument(t, ts, map[string]any{
		"client_name":   "Claude",
		"redirect_uris": []string{claudeRedirect},
	})

	q := authorizeQuery(ts, clientID, "td:read td:capture", newPKCE())
	resp, html := page(t, ts, session, server.AuthorizePath+"?"+q.Encode())
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the consent screen answered %d", resp.StatusCode)
	}

	for _, scope := range []string{"td:read", "td:capture"} {
		box := checkbox(html, scope)
		if box == "" || !strings.Contains(box, "checked") {
			t.Errorf("%s was asked for and should start checked: %q", scope, box)
		}
	}
	box := checkbox(html, "td:write")
	if box == "" {
		t.Fatal("the screen does not offer write, so the connector can never have it")
	}
	if strings.Contains(box, "checked") {
		t.Errorf("write was not asked for but starts checked: %q", box)
	}
	if !strings.Contains(html, "not requested") {
		t.Error("the screen does not say write goes beyond the request")
	}
}

// TestTickingWriteGrantsIt is the round trip that counts: the owner ticks the
// box the client did not ask for, and a write tool runs on the token.
func TestTickingWriteGrantsIt(t *testing.T) {
	ts := newServer(t)
	session := login(t, ts)
	clientID := hostDocument(t, ts, map[string]any{
		"client_name":   "Claude",
		"redirect_uris": []string{claudeRedirect},
	})

	p := newPKCE()
	q := authorizeQuery(ts, clientID, "td:read td:capture", p)
	code := consent(t, ts, session, q,
		[]string{"td:read", "td:capture", "td:write"}).Query().Get("code")

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
		Scope       string `json:"scope"`
	}
	decodeInto(t, body, &issued)
	// RFC 6749 section 5.1 requires the scope in the response when it differs
	// from the request, which is how the client learns it got more.
	if issued.Scope != "td:read td:capture td:write" {
		t.Errorf("scope = %q, want what was ticked", issued.Scope)
	}

	mcpSession := connect(t, ts, issued.AccessToken)
	if res := call(t, mcpSession, "create_task",
		map[string]any{"title": "written through a ticked box"}, nil); res.IsError {
		t.Fatalf("create_task on a token granted write: %s", errorText(res))
	}
}

// TestLeavingWriteUncheckedLeavesItOut. Offering is not granting.
func TestLeavingWriteUncheckedLeavesItOut(t *testing.T) {
	ts := newServer(t)
	session := login(t, ts)
	clientID := hostDocument(t, ts, map[string]any{
		"client_name":   "Claude",
		"redirect_uris": []string{claudeRedirect},
	})

	p := newPKCE()
	q := authorizeQuery(ts, clientID, "td:read td:capture", p)
	code := consent(t, ts, session, q, []string{"td:read", "td:capture"}).Query().Get("code")

	_, body := exchange(t, ts, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {claudeRedirect},
		"client_id":     {clientID},
		"code_verifier": {p.verifier},
		"resource":      {ts.srv.ResourceURL()},
	})
	var issued struct {
		Scope string `json:"scope"`
	}
	decodeInto(t, body, &issued)
	if issued.Scope != "td:read td:capture" {
		t.Errorf("scope = %q, want only what was ticked", issued.Scope)
	}
}

// TestARegisteredCeilingHolds. A client registered for read and capture is
// not offered write, and a form that ticks it anyway does not get it.
func TestARegisteredCeilingHolds(t *testing.T) {
	ts := newServer(t)
	session := login(t, ts)
	clientID, secret := registerClient(t, ts, "td:read td:capture")

	p := newPKCE()
	q := authorizeQuery(ts, clientID, "td:read td:capture", p)
	_, html := page(t, ts, session, server.AuthorizePath+"?"+q.Encode())
	if checkbox(html, "td:write") != "" {
		t.Error("the screen offers write to a client registered without it")
	}

	code := consent(t, ts, session, q,
		[]string{"td:read", "td:capture", "td:write"}).Query().Get("code")
	_, body := exchange(t, ts, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {claudeRedirect},
		"client_id":     {clientID},
		"client_secret": {secret},
		"code_verifier": {p.verifier},
	})
	var issued struct {
		Scope string `json:"scope"`
	}
	decodeInto(t, body, &issued)
	if strings.Contains(issued.Scope, "td:write") {
		t.Errorf("a forged form widened past the registration: %q", issued.Scope)
	}
}
