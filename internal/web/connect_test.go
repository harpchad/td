package web

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The device code panel has to render two ways: a whole page when a browser
// navigated to it, a fragment when htmx is swapping. Getting that backwards
// shipped once, and the failure was quiet in the worst way. The response had
// no layout, so no stylesheet and no htmx; nothing polled; and the only
// control that did anything was the submit button, which fell back to a
// native GET at the current URL and 404ed with the device code in the address
// bar.

func testUI(t *testing.T) *UI {
	t.Helper()
	ui, err := New(nil, Load("", slog.New(slog.DiscardHandler)),
		slog.New(slog.DiscardHandler), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	return ui
}

func renderConnect(t *testing.T, htmx bool) string {
	t.Helper()
	ui := testUI(t)

	r := httptest.NewRequest(http.MethodPost, "/w/plugins/planner/connect", nil)
	if htmx {
		r.Header.Set("HX-Request", "true")
	}
	w := httptest.NewRecorder()
	ui.ConnectPanel(w, r, ConnectCode{
		Plugin:   "planner",
		UserCode: "FJDNSKQP", VerificationURI: "https://microsoft.com/devicelogin",
		DeviceCode: "the-secret-half", TenantID: "t", ClientID: "c", Interval: 5,
	})
	return w.Body.String()
}

// TestABrowserGetsAWholePage, with the stylesheet and htmx on it. Without
// those the panel cannot poll and cannot be read.
func TestABrowserGetsAWholePage(t *testing.T) {
	body := renderConnect(t, false)

	for _, want := range []string{"<!doctype html>", "/static/td.css", "/static/htmx.min.js"} {
		if !strings.Contains(body, want) {
			t.Errorf("the page has no %q, so the panel is inert", want)
		}
	}
	if !strings.Contains(body, "FJDNSKQP") {
		t.Error("the code is not on the page")
	}
}

// TestHtmxGetsOnlyTheFragment, because a whole page swapped into a div would
// nest a second <html> inside the first.
func TestHtmxGetsOnlyTheFragment(t *testing.T) {
	body := renderConnect(t, true)

	if strings.Contains(body, "<!doctype html>") || strings.Contains(body, "<head>") {
		t.Error("htmx was handed a whole page to swap into a div")
	}
	if !strings.Contains(body, "FJDNSKQP") {
		t.Error("the fragment does not carry the code")
	}
	if !strings.Contains(body, `id="td-connect"`) {
		t.Error("the fragment has no swap target, so the poll replaces nothing")
	}
}

// TestThePollFormPostsToAnExplicitAction. With no action and no method a
// submit is a GET at whatever URL the browser happens to be on, which is
// exactly how the device code ended up in a query string.
func TestThePollFormPostsToAnExplicitAction(t *testing.T) {
	body := renderConnect(t, false)

	if !strings.Contains(body, `action="/w/plugins/planner/poll"`) {
		t.Error("the poll form has no action, so a submit goes to the current URL")
	}
	if !strings.Contains(body, `method="post"`) {
		t.Error("the poll form has no method, so a submit is a GET")
	}
	// The device code belongs in the body, never in a URL.
	if !strings.Contains(body, `name="device_code"`) {
		t.Error("the device code is not carried in the form")
	}
	if strings.Contains(body, "?device_code=") {
		t.Error("the device code appears in a URL")
	}
	// And it still polls on its own where scripting is available.
	if !strings.Contains(body, `hx-post="/w/plugins/planner/poll"`) {
		t.Error("nothing polls automatically")
	}
}

// TestTheErrorPanelOffersAWayOut rather than leaving somebody on a dead page
// with a code that will never work.
func TestTheErrorPanelOffersAWayOut(t *testing.T) {
	ui := testUI(t)
	w := httptest.NewRecorder()
	ui.ConnectPanel(w, httptest.NewRequest(http.MethodPost, "/w/plugins/planner/connect", nil),
		ConnectCode{Plugin: "planner", Error: "the sign-in was declined"})

	body := w.Body.String()
	if !strings.Contains(body, "the sign-in was declined") {
		t.Error("the error is not shown")
	}
	if !strings.Contains(body, `href="/settings"`) {
		t.Error("no way back to settings from a failed sign-in")
	}
}

// TestThePanelCarriesEverythingItNeedsToRedrawItself.
//
// Each poll re-renders the whole panel, so every field it draws with has to
// travel in the form. Leaving the code and the link out is what made them
// vanish about five seconds after they appeared: the first swap replaced a
// panel that had them with one that did not, and the person watching had done
// nothing but wait.
func TestThePanelCarriesEverythingItNeedsToRedrawItself(t *testing.T) {
	body := renderConnect(t, false)

	for _, field := range []string{
		"device_code", "user_code", "verification_uri",
		"tenant_id", "client_id", "interval",
	} {
		if !strings.Contains(body, `name="`+field+`"`) {
			t.Errorf("the form does not carry %s, so a poll cannot redraw it", field)
		}
	}

	// And the two that are visible are actually populated, not just declared.
	if !strings.Contains(body, `name="user_code" value="FJDNSKQP"`) {
		t.Error("the code is not carried forward")
	}
	if !strings.Contains(body, `name="verification_uri" value="https://microsoft.com/devicelogin"`) {
		t.Error("the verification link is not carried forward")
	}
}

// TestAPendingPollStillShowsTheCode is the same rule from the other side: what
// the poll renders has to look like what it replaced, apart from the message.
func TestAPendingPollStillShowsTheCode(t *testing.T) {
	ui := testUI(t)
	r := httptest.NewRequest(http.MethodPost, "/w/plugins/planner/poll", nil)
	r.Header.Set("HX-Request", "true")
	w := httptest.NewRecorder()

	ui.ConnectPending(w, r, ConnectCode{
		Plugin:   "planner",
		UserCode: "FJDNSKQP", VerificationURI: "https://microsoft.com/devicelogin",
		DeviceCode: "the-secret-half", TenantID: "t", ClientID: "c", Interval: 5,
	}, "Waiting for the sign-in…")

	body := w.Body.String()
	if !strings.Contains(body, "FJDNSKQP") {
		t.Fatal("a pending poll dropped the code, which is what somebody is reading off the screen")
	}
	if !strings.Contains(body, "microsoft.com/devicelogin") {
		t.Error("a pending poll dropped the link, leaving \"Go to and enter this code\"")
	}
	if !strings.Contains(body, "Waiting for the sign-in") {
		t.Error("the pending message is missing")
	}
}
