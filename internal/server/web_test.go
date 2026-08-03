package server_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/harpchad/td/internal/api"
)

// The web UI is rendered HTML, so these read the markup. Every case is a rule
// section 12, tokens.css, or mockup.html states.

// login signs in through the form and returns the session cookie, which is
// what a browser would carry.
func login(t *testing.T, ts *harness) string {
	t.Helper()
	form := url.Values{
		"username": {testUsername},
		"password": {testPassword},
		"totp":     {ts.totp},
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		ts.URL+"/login", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("form login = %d, want a redirect to the app", resp.StatusCode)
	}
	for _, c := range resp.Cookies() {
		if c.Name == "td_session" {
			return c.Value
		}
	}
	t.Fatal("form login set no session cookie")
	return ""
}

// page fetches an HTML page as a signed-in browser.
func page(t *testing.T, ts *harness, session, path string) (respMeta, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "text/html")
	if session != "" {
		req.AddCookie(&http.Cookie{Name: "td_session", Value: session})
	}
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	body := make([]byte, 1<<20)
	n, _ := resp.Body.Read(body)
	for n < len(body) {
		more, err := resp.Body.Read(body[n:])
		n += more
		if err != nil {
			break
		}
	}
	return respMeta{StatusCode: resp.StatusCode, Header: resp.Header}, string(body[:n])
}

// postForm submits an HTML form as a signed-in browser.
func postForm(t *testing.T, ts *harness, session, path string, form url.Values) respMeta {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		ts.URL+path, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
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

// postFormBody is postForm when the answer's body is what matters.
func postFormBody(t *testing.T, ts *harness, session, path string, form url.Values) string {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		ts.URL+path, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "td_session", Value: session})

	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// TestHomeRendersTheFixtureOrder covers parity with the TUI: the same filter
// produces the same list in the same order, with the subtask lifted under its
// parent.
func TestHomeRendersTheFixtureOrder(t *testing.T) {
	ts := newServer(t)
	session := login(t, ts)

	resp, html := page(t, ts, session, "/?q="+url.QueryEscape("is:open src:local -is:inbox -is:snoozed -is:deferred"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("home = %d", resp.StatusCode)
	}

	nums := regexp.MustCompile(`data-num="(\d+)"`).FindAllStringSubmatch(html, -1)
	var got []string
	for _, m := range nums {
		got = append(got, m[1])
	}
	// The comparator order is 104 102 101 114 108 106 113 103; display order
	// lifts 113 under 101, which is what query.Arrange does for both clients.
	want := []string{"104", "102", "101", "113", "114", "108", "106", "103"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("row order = %v\nwant %v", got, want)
	}

	// The subtask carries the modifier the stylesheet indents on.
	if !strings.Contains(html, `td-row--sub`) {
		t.Error("no td-row--sub, so the subtask is not indented")
	}
	// And the parent's count is always drawn, because when it is folded that
	// count is the only signal the children exist.
	if !strings.Contains(html, `class="td-children">0/1<`) {
		t.Error("the parent does not draw its child count")
	}
}

// TestRowMarkupMatchesTheMockup covers the classes the mockup fixes. The
// stylesheet is the authority and it keys off these names.
func TestRowMarkupMatchesTheMockup(t *testing.T) {
	ts := newServer(t)
	session := login(t, ts)
	_, html := page(t, ts, session, "/")

	for _, class := range []string{
		"td-bar", "td-bar--status", "td-bar__spacer", "td-count",
		"td-row", "td-num", "td-prio", "td-title", "td-token", "td-due",
		"td-children", "td-box", "td-done", "td-fold", "td-fold--leaf",
		"td-group", "td-key", "td-link", "td-scroll",
	} {
		if !strings.Contains(html, class) {
			t.Errorf("the home page never uses %q, which tokens.css styles", class)
		}
	}

	// Priority is encoded in weight and value, never in hue.
	if !strings.Contains(html, "td-prio--1") || !strings.Contains(html, "td-prio--2") {
		t.Error("the priority ramp classes are missing")
	}
	// The overdue token is the one paired color exception.
	if !strings.Contains(html, "td-due--overdue") {
		t.Error("the overdue row does not carry td-due--overdue")
	}
	// Task state is a real checkbox, not a toggle. Toggles are for settings.
	if !strings.Contains(html, `type="checkbox" class="td-done"`) {
		t.Error("task state is not a native checkbox")
	}
	if strings.Contains(html, "td-toggle") {
		t.Error("the task list uses a toggle, which is for persistent settings only")
	}
}

// TestUnauthenticatedBrowserGetsTheLoginPage covers the one route that
// renders anything without a credential, and checks the API still answers
// with nothing at all.
func TestUnauthenticatedBrowserGetsTheLoginPage(t *testing.T) {
	ts := newServer(t)

	resp, _ := page(t, ts, "", "/")
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("home without a session = %d, want a redirect to /login", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/login" {
		t.Errorf("redirected to %q, want /login", loc)
	}

	resp, html := page(t, ts, "", "/login")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login page = %d", resp.StatusCode)
	}
	for _, want := range []string{`name="username"`, `name="password"`, `name="totp"`, "td-modal"} {
		if !strings.Contains(html, want) {
			t.Errorf("the login page is missing %q", want)
		}
	}
	// No registration and no password reset, anywhere on the page.
	for _, bad := range []string{"register", "sign up", "signup", "forgot", "reset"} {
		if strings.Contains(strings.ToLower(html), bad) {
			t.Errorf("the login page mentions %q, and there is no such route", bad)
		}
	}

	// The API is unchanged: 401 and an empty body.
	apiResp, body := doAnon(t, ts, http.MethodGet, "/api/v1/tasks", nil)
	if apiResp.StatusCode != http.StatusUnauthorized || len(body) != 0 {
		t.Errorf("the API answered %d with %d bytes", apiResp.StatusCode, len(body))
	}
}

// TestNoInlineScriptAnywhere covers the CSP rule. A single inline handler
// would force unsafe-inline back into the policy.
func TestNoInlineScriptAnywhere(t *testing.T) {
	ts := newServer(t)
	session := login(t, ts)

	// The consent screen is included because it is a form a browser posts
	// credentials-adjacent decisions through, and a control silently dropped
	// by the CSP there is a control that grants the wrong thing.
	clientID, _ := registerClient(t, ts, "td:read td:write")
	consentPath := "/authorize?" + authorizeQuery(ts, clientID, "td:read td:write", newPKCE()).Encode()

	for _, path := range []string{
		"/", "/login", "/help", "/settings", "/t/101", "/triage", consentPath,
	} {
		_, html := page(t, ts, session, path)

		// Go's regexp has no lookahead, so match every script element and
		// check each one carries a src and an empty body.
		scripts := regexp.MustCompile(`(?s)<script([^>]*)>(.*?)</script>`)
		for _, m := range scripts.FindAllStringSubmatch(html, -1) {
			attrs, body := m[1], strings.TrimSpace(m[2])
			if !strings.Contains(attrs, "src=") {
				t.Errorf("%s carries a script with no src: <script%s>", path, attrs)
			}
			if body != "" {
				t.Errorf("%s carries an inline script body: %q", path, body)
			}
		}
		// An inline style attribute is dropped by style-src 'self' just as
		// silently as an inline script is. That is worse than it sounds: the
		// markup looks right, the page renders, and every control that was
		// hidden or laid out by one is simply wrong. It put a visible "drop"
		// button on every row until this test existed.
		if idx := strings.Index(html, "style=\""); idx >= 0 {
			end := strings.Index(html[idx+7:], "\"")
			t.Errorf("%s carries an inline style, which the CSP drops: style=%q",
				path, html[idx+7:idx+7+end])
		}
		// Inline event handlers are the other way inline code gets in, and
		// they need unsafe-inline just as much as a <script> block does.
		for _, handler := range []string{"onclick=", "onchange=", "onsubmit=", "onload=", "oninput="} {
			if strings.Contains(html, handler) {
				t.Errorf("%s carries an inline %s handler", path, handler)
			}
		}
		// javascript: URLs are the other way inline code gets in.
		if strings.Contains(html, "javascript:") {
			t.Errorf("%s carries a javascript: URL", path)
		}
	}

	// And the policy itself still refuses inline.
	resp, _ := page(t, ts, session, "/")
	csp := resp.Header.Get("Content-Security-Policy")
	if strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "unsafe-eval") {
		t.Errorf("CSP = %q", csp)
	}
	if !strings.Contains(csp, "script-src 'self'") {
		t.Errorf("CSP = %q, want script-src 'self' for the vendored files", csp)
	}
}

// TestEveryActionWorksWithoutJavaScript covers the progressive path: the
// forms post and redirect when htmx is not driving them, which is also what
// makes them testable without a browser.
func TestEveryActionWorksWithoutJavaScript(t *testing.T) {
	ts := newServer(t)
	session := login(t, ts)

	post := func(path string) respMeta {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, ts.URL+path, strings.NewReader(""))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(&http.Cookie{Name: "td_session", Value: session})
		client := &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		return respMeta{StatusCode: resp.StatusCode, Header: resp.Header}
	}

	id, err := ts.store.Resolve(t.Context(), "103")
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		"/w/complete/" + id,
		"/w/undo",
		"/w/fold/" + id,
		"/w/drop/" + id,
	} {
		if got := post(path).StatusCode; got != http.StatusSeeOther {
			t.Errorf("POST %s without htmx = %d, want a redirect", path, got)
		}
	}
}

// TestHtmxActionsReturnTheListFragment covers the swap path: an action fired
// by htmx answers with the list rather than a whole page.
func TestHtmxActionsReturnTheListFragment(t *testing.T) {
	ts := newServer(t)
	session := login(t, ts)

	id, err := ts.store.Resolve(t.Context(), "103")
	if err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		ts.URL+"/w/complete/"+id, strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	req.AddCookie(&http.Cookie{Name: "td_session", Value: session})

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	body := make([]byte, 1<<20)
	n, _ := resp.Body.Read(body)
	html := string(body[:n])

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("htmx action = %d", resp.StatusCode)
	}
	if strings.Contains(html, "<!doctype html>") {
		t.Error("an htmx action returned a whole page rather than the fragment")
	}
	if !strings.Contains(html, "td-row") {
		t.Error("the fragment carries no rows")
	}
	if !strings.Contains(html, "done 103") {
		t.Error("the fragment does not report what happened")
	}
}

// TestEmptyStateNamesTheFilter covers the rule that empty is an invitation.
func TestWebEmptyStateNamesTheFilter(t *testing.T) {
	ts := newServer(t)
	session := login(t, ts)

	_, html := page(t, ts, session, "/?q="+url.QueryEscape("#nosuchtag"))
	if !strings.Contains(html, "Nothing matches") {
		t.Error("no empty state")
	}
	if !strings.Contains(html, "#nosuchtag") {
		t.Error("the empty state does not name the filter that found nothing")
	}

	_, html = page(t, ts, session, "/?q=is%3Ainbox")
	if strings.Contains(html, "td-empty") && !strings.Contains(html, "Inbox zero") {
		t.Error("an empty inbox does not say the one satisfied thing")
	}
}

// TestABadFilterReportsTheParserMessage covers the shared grammar: the web
// box and the API say the same thing about the same typo.
func TestABadFilterReportsTheParserMessage(t *testing.T) {
	ts := newServer(t)
	session := login(t, ts)

	_, html := page(t, ts, session, "/?q="+url.QueryEscape("foo:bar"))
	if !strings.Contains(html, "unknown filter key") {
		t.Errorf("the page does not carry the parser's message:\n%s", firstLines(html, 40))
	}
}

// TestSettingsListsThemesAndTokens covers the picker being a list rather than
// a gallery, and the settings page section 15 asks for.
func TestSettingsListsThemesAndTokens(t *testing.T) {
	ts := newServer(t)
	session := login(t, ts)

	_, html := page(t, ts, session, "/settings")

	for _, theme := range []string{"auto", "nord", "dracula", "tokyo-night", "solarized-light", "light", "dark"} {
		if !strings.Contains(html, `value="`+theme+`"`) {
			t.Errorf("the theme picker does not offer %q", theme)
		}
	}
	// A list, not a gallery and not a live preview: radios, no swatches.
	if !strings.Contains(html, "td-radio") {
		t.Error("the theme picker is not a list of radios")
	}

	// Tokens with their last-used time and a revoke control.
	if !strings.Contains(html, "revoke") {
		t.Error("the settings page has no revoke control")
	}
	if !strings.Contains(html, "td_") {
		t.Error("the settings page does not show a token prefix")
	}
	// And never the secret itself.
	if strings.Contains(html, ts.token) {
		t.Error("the settings page rendered a token secret")
	}
}

// TestThemeCookieSelectsThePalette covers the picker end to end.
func TestThemeCookieSelectsThePalette(t *testing.T) {
	ts := newServer(t)
	session := login(t, ts)

	// With nothing picked the page carries no data-theme at all, which is
	// what lets the prefers-color-scheme rule apply. Forcing light here would
	// mean a browser set to dark gets a light page until someone visits
	// settings.
	_, html := page(t, ts, session, "/")
	if strings.Contains(html, "data-theme=") {
		t.Error("the default page pins a theme instead of following the system")
	}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, ts.URL+"/w/theme",
		strings.NewReader("theme=nord"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "td_session", Value: session})
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	var themeCookie string
	for _, c := range resp.Cookies() {
		if c.Name == "td_theme" {
			themeCookie = c.Value
		}
	}
	if themeCookie != "nord" {
		t.Fatalf("theme cookie = %q, want nord", themeCookie)
	}

	req, err = http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "text/html")
	req.AddCookie(&http.Cookie{Name: "td_session", Value: session})
	req.AddCookie(&http.Cookie{Name: "td_theme", Value: "nord"})
	resp2, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp2.Body.Close() }()
	body := make([]byte, 4096)
	n, _ := resp2.Body.Read(body)
	if !strings.Contains(string(body[:n]), `data-theme="nord"`) {
		t.Error("the theme cookie did not select the palette")
	}
}

// TestStylesheetIsOneFileAndCarriesTheSystem covers "one hand-written
// stylesheet": the browser fetches one, assembled from the files that are the
// authority.
func TestStylesheetIsOneFileAndCarriesTheSystem(t *testing.T) {
	ts := newServer(t)
	session := login(t, ts)

	_, html := page(t, ts, session, "/")
	sheets := regexp.MustCompile(`<link[^>]+rel="stylesheet"`).FindAllString(html, -1)
	if len(sheets) != 1 {
		t.Errorf("the page loads %d stylesheets, want one", len(sheets))
	}

	_, css := page(t, ts, session, "/static/td.css")
	for _, want := range []string{
		"--td-paper", "--td-ink", "--td-accent", "--td-dim",
		".td-row--selected", ".td-done", ".td-toggle", ".td-modal__title",
		`[data-theme="nord"]`, `[data-theme="dracula"]`,
	} {
		if !strings.Contains(css, want) {
			t.Errorf("the stylesheet is missing %q", want)
		}
	}

	// No framework got in.
	for _, bad := range []string{"tailwind", "bootstrap", "!important;transition"} {
		if strings.Contains(strings.ToLower(css), bad) {
			t.Errorf("the stylesheet contains %q", bad)
		}
	}
}

func firstLines(s string, n int) string {
	lines := strings.SplitN(s, "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

// TestTheDetailPageEditsEveryField covers editing from the browser, which is
// the half of the gap the TUI keys were the other half of.
func TestTheDetailPageEditsEveryField(t *testing.T) {
	ts := newServer(t)
	session := login(t, ts)

	id, err := ts.store.Resolve(t.Context(), "103")
	if err != nil {
		t.Fatal(err)
	}

	form := url.Values{
		"title":    {"Order tires and an alignment"},
		"notes":    {"Discount tire quoted 780 for the set."},
		"priority": {"1"},
		"due":      {"friday"},
		"tags":     {"truck garage"},
		"notify":   {"on"},
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		ts.URL+"/w/edit/"+id, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "td_session", Value: session})

	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("edit = %d, want a redirect back to the task", resp.StatusCode)
	}

	task, err := ts.store.Get(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if task.Title != "Order tires and an alignment" {
		t.Errorf("title = %q", task.Title)
	}
	if task.Notes == "" {
		t.Error("the notes were not saved")
	}
	if task.Priority == nil || *task.Priority != 1 {
		t.Errorf("priority = %v", task.Priority)
	}
	// The date vocabulary is shared, so a keyword works in the form.
	if task.DueAt == nil || *task.DueAt != "2026-08-07" {
		t.Errorf("due = %v, want friday resolved against the pinned clock", task.DueAt)
	}
	if strings.Join(task.Tags, ",") != "garage,truck" {
		t.Errorf("tags = %v", task.Tags)
	}
	if task.Notify != "on" {
		t.Errorf("notify = %q", task.Notify)
	}
}

// TestAnEmptyFieldClears covers the rule the form makes visible: a box you
// emptied means clear it, not leave it.
func TestAnEmptyFieldClears(t *testing.T) {
	ts := newServer(t)
	session := login(t, ts)

	id, err := ts.store.Resolve(t.Context(), "101")
	if err != nil {
		t.Fatal(err)
	}
	before, err := ts.store.Get(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if before.Priority == nil || before.DueAt == nil {
		t.Fatal("the fixture task should start with a priority and a due date")
	}

	form := url.Values{
		"title": {before.Title}, "notes": {""},
		"priority": {""}, "due": {""}, "tags": {""}, "notify": {"auto"},
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		ts.URL+"/w/edit/"+id, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "td_session", Value: session})
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	after, err := ts.store.Get(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if after.Priority != nil {
		t.Errorf("priority = %v, want cleared", *after.Priority)
	}
	if after.DueAt != nil {
		t.Errorf("due = %v, want cleared", *after.DueAt)
	}
	if len(after.Tags) != 0 {
		t.Errorf("tags = %v, want cleared", after.Tags)
	}
}

// TestNotifyIsRadiosNotAToggle covers the control inventory rule: the
// tri-state is three values, and a toggle cannot express "whatever the
// default says". Toggles are for persistent settings.
func TestNotifyIsRadiosNotAToggle(t *testing.T) {
	ts := newServer(t)
	session := login(t, ts)

	_, html := page(t, ts, session, "/t/101")

	for _, mode := range []string{"auto", "on", "off"} {
		if !strings.Contains(html, `type="radio" name="notify" value="`+mode+`"`) {
			t.Errorf("the notify control has no %q radio", mode)
		}
	}
	if strings.Contains(html, "td-toggle") {
		t.Error("notify is drawn as a toggle, which cannot express three states")
	}
	// And a textarea for notes, which is the one multi-line field.
	if !strings.Contains(html, "<textarea") {
		t.Error("the detail page has no notes editor")
	}
}

// TestTriageIsItsOwnScreen covers section 7: triage is a dedicated mode, not
// a view. A filtered list cannot get you from 20 to 0 in two minutes because
// every decision makes the eye hunt for the next row.
func TestTriageIsItsOwnScreen(t *testing.T) {
	ts := newServer(t)
	session := login(t, ts)

	resp, html := page(t, ts, session, "/triage")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	// The fixture's inbox has tasks, so one card is showing with the actions
	// on it.
	for _, want := range []string{"Promote", "Skip", "Drop", `name="do"`} {
		if !strings.Contains(html, want) {
			t.Errorf("the triage card has no %q", want)
		}
	}

	// One task, not the list. The home view shows eight rows; triage shows
	// one title.
	if n := strings.Count(html, `class="td-row`); n > 0 {
		t.Errorf("triage rendered %d list rows; it is one card", n)
	}
}

// TestTriagePromoteTakesTheTaskOutOfTheInbox is the action that makes triage
// worth having. A priority is what lets a task leave the inbox, so setting
// one and promoting is one request rather than two.
func TestTriagePromoteTakesTheTaskOutOfTheInbox(t *testing.T) {
	ts := newServer(t)
	session := login(t, ts)

	inbox, err := ts.store.List(t.Context(), "is:inbox", ts.now)
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox) == 0 {
		t.Fatal("the fixture has no inbox tasks")
	}
	target := inbox[0]

	form := url.Values{"do": {"promote"}, "priority": {"2"}, "at": {"0"}}
	resp := postForm(t, ts, session, "/w/triage/"+target.ID, form)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want a redirect back into the queue", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); !strings.HasPrefix(loc, "/triage") {
		t.Errorf("Location = %q, want to stay in triage", loc)
	}

	after, err := ts.store.Get(t.Context(), target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != api.StatusTodo {
		t.Errorf("status = %s, want the task out of the inbox", after.Status)
	}
	if after.Priority == nil || *after.Priority != 2 {
		t.Errorf("priority = %v, want 2", after.Priority)
	}
}

// TestASubtaskIsCreatedUnderItsParent covers the web half of subtasks. The
// parent is the commitment; completing it never cascades.
func TestASubtaskIsCreatedUnderItsParent(t *testing.T) {
	ts := newServer(t)
	session := login(t, ts)

	parent, err := ts.store.GetByNum(t.Context(), 103)
	if err != nil {
		t.Fatal(err)
	}

	form := url.Values{"line": {"call the dealer #truck p:2"}}
	resp := postForm(t, ts, session, "/w/sub/"+parent.ID, form)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	children, err := ts.store.Children(t.Context(), parent.ID, ts.now)
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 1 {
		t.Fatalf("%d children, want 1", len(children))
	}
	if children[0].Title != "call the dealer" {
		t.Errorf("title = %q", children[0].Title)
	}
	if children[0].ParentID == nil || *children[0].ParentID != parent.ID {
		t.Error("the subtask is not linked to its parent")
	}

	// And the detail page shows it.
	_, html := page(t, ts, session, "/t/103")
	if !strings.Contains(html, "call the dealer") {
		t.Error("the parent's detail page does not list the subtask")
	}
}

// A filter is a place you are, not an argument you passed. These cover the
// rule that it stays put until you clear it.

// activeFilter reads what the filter bar is actually showing.
func activeFilter(t *testing.T, html string) string {
	t.Helper()
	m := regexp.MustCompile(`name="q" value="([^"]*)"`).FindStringSubmatch(html)
	if m == nil {
		t.Fatal("the page has no filter bar")
	}
	return html2text(m[1])
}

func html2text(s string) string {
	r := strings.NewReplacer("&#34;", `"`, "&amp;", "&", "&lt;", "<", "&gt;", ">", "&#39;", "'")
	return r.Replace(s)
}

// TestTheFilterSurvivesLeavingTheList. Open a task from a filtered list, press
// Escape, and you should be back on the list you were reading.
func TestTheFilterSurvivesLeavingTheList(t *testing.T) {
	ts := newServer(t)
	session := login(t, ts)

	const filter = "#certs"
	if _, html := page(t, ts, session, "/?q="+url.QueryEscape(filter)); activeFilter(t, html) != filter {
		t.Fatalf("the filtered list did not apply %q", filter)
	}

	// Whatever "back" points at has to land on that same list.
	_, detail := page(t, ts, session, "/t/101")
	m := regexp.MustCompile(`href="([^"]*)" data-back`).FindStringSubmatch(detail)
	if m == nil {
		t.Fatal("the detail page has no back link")
	}
	_, back := page(t, ts, session, m[1])
	if got := activeFilter(t, back); got != filter {
		t.Errorf("back from a task landed on %q, want the %q list still", got, filter)
	}
}

// TestTheFilterSurvivesComingBackLater. Closing the tab is not clearing the
// filter, so returning to the bare root gives you what you were last reading.
func TestTheFilterSurvivesComingBackLater(t *testing.T) {
	ts := newServer(t)
	session := login(t, ts)

	const filter = "#certs"
	page(t, ts, session, "/?q="+url.QueryEscape(filter))

	if _, html := page(t, ts, session, "/"); activeFilter(t, html) != filter {
		t.Errorf("the bare root showed %q, want %q", activeFilter(t, html), filter)
	}
}

// TestClearingTheFilterClearsIt, which is the other half: sticky state you
// cannot get rid of is worse than state that does not stick.
func TestClearingTheFilterClearsIt(t *testing.T) {
	ts := newServer(t)
	session := login(t, ts)

	page(t, ts, session, "/?q="+url.QueryEscape("#certs"))

	// An empty box submits as ?q= with the key present, which is the clear.
	if _, html := page(t, ts, session, "/?q="); activeFilter(t, html) != "" {
		t.Fatalf("clearing left %q in the bar", activeFilter(t, html))
	}
	if _, html := page(t, ts, session, "/"); activeFilter(t, html) != "" {
		t.Errorf("a cleared filter came back as %q", activeFilter(t, html))
	}
}

// TestABrokenFilterIsNotRemembered. Sticky state plus a filter that does not
// parse is a home page you cannot get back to except by editing the URL, so
// only a filter that actually ran is worth keeping.
func TestABrokenFilterIsNotRemembered(t *testing.T) {
	ts := newServer(t)
	session := login(t, ts)

	const good = "#certs"
	page(t, ts, session, "/?q="+url.QueryEscape(good))

	_, broken := page(t, ts, session, "/?q="+url.QueryEscape("p:<=notanumber"))
	if !strings.Contains(broken, "td-error") && !strings.Contains(broken, "error") {
		t.Log("the broken filter did not render an error; the case may no longer be broken")
	}

	if _, html := page(t, ts, session, "/"); activeFilter(t, html) != good {
		t.Errorf("home came back as %q, want the last filter that worked, %q",
			activeFilter(t, html), good)
	}
}

// TestAnActionKeepsTheFilterItWasFiredFrom covers the htmx path: completing a
// task on a filtered list re-renders that list, not the default one.
func TestAnActionKeepsTheFilterItWasFiredFrom(t *testing.T) {
	ts := newServer(t)
	session := login(t, ts)

	const filter = "#certs"
	page(t, ts, session, "/?q="+url.QueryEscape(filter))

	// No `q` in the form and no referer either, which is the case that used to
	// fall all the way through to slot 1.
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		ts.URL+"/w/undo", strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "td_session", Value: session})
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if _, html := page(t, ts, session, "/"); activeFilter(t, html) != filter {
		t.Errorf("after an action the list was %q, want %q", activeFilter(t, html), filter)
	}
}

// TestTheFilterIsTheSameOneInBothClients. The done criterion says the TUI and
// the web UI show the same list for the same filter; this is the other half of
// that, which is that "the same filter" is one piece of state rather than two.
// A filter set through the API, which is how the TUI sets it, is the list the
// browser opens on.
func TestTheFilterIsTheSameOneInBothClients(t *testing.T) {
	ts := newServer(t)
	session := login(t, ts)

	const filter = "#certs"
	resp, _ := do(t, ts, http.MethodPut, "/api/v1/ui/filter", api.ViewFilter{Filter: filter})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT /ui/filter = %d", resp.StatusCode)
	}

	if _, html := page(t, ts, session, "/"); activeFilter(t, html) != filter {
		t.Errorf("the browser opened on %q, want the %q the API set", activeFilter(t, html), filter)
	}

	// And back the other way, which is what the TUI reads on its next start.
	page(t, ts, session, "/?q="+url.QueryEscape("is:inbox"))

	var view api.ViewFilter
	_, body := do(t, ts, http.MethodGet, "/api/v1/ui/filter", nil)
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatal(err)
	}
	if view.Filter != "is:inbox" || !view.Chosen {
		t.Errorf("the API reports %+v, want the is:inbox the browser set", view)
	}
}

// TestAStoredFilterThatDoesNotParseIsRefused. The store is read on every home
// render, so a query that cannot run is a home page nobody can open.
func TestAStoredFilterThatDoesNotParseIsRefused(t *testing.T) {
	ts := newServer(t)

	resp, _ := do(t, ts, http.MethodPut, "/api/v1/ui/filter", api.ViewFilter{Filter: "p:<=nonsense"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("PUT of an unparseable filter = %d, want 400", resp.StatusCode)
	}
}

// Recurrence in the browser. The rule was creatable from the CLI and the TUI
// only, which is a burden on a deployment where the web UI is how you touch
// the thing at all.

// TestTheDetailPageShowsTheRuleInEnglish. RRULE is the right storage format
// and the wrong thing to show a person.
func TestTheDetailPageShowsTheRuleInEnglish(t *testing.T) {
	ts := newServer(t)
	session := login(t, ts)

	form := url.Values{"repeat": {"every 2 weeks"}}
	if resp := postForm(t, ts, session, "/w/repeat/101", form); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("repeat = %d, want a redirect", resp.StatusCode)
	}

	_, html := page(t, ts, session, "/t/101")
	if !strings.Contains(html, "every 2 weeks") {
		t.Errorf("the detail page does not say the rule in English:\n%s", firstLines(html, 40))
	}
	// The rule itself is still shown, because the description is for people
	// and the rule is what the server runs.
	if !strings.Contains(html, "FREQ=WEEKLY;INTERVAL=2") {
		t.Error("the detail page does not show the stored rule")
	}
}

// TestRepeatingATaskLeavesTheInstanceAlone. Section 3: editing an instance and
// editing the series are two different actions, and this is the one that must
// not touch the task in front of you.
func TestRepeatingATaskLeavesTheInstanceAlone(t *testing.T) {
	ts := newServer(t)
	session := login(t, ts)

	var before api.Task
	_, body := do(t, ts, http.MethodGet, "/api/v1/tasks/101", nil)
	decodeInto(t, body, &before)

	postForm(t, ts, session, "/w/repeat/101", url.Values{"repeat": {"every monday"}})

	var after api.Task
	_, body = do(t, ts, http.MethodGet, "/api/v1/tasks/101", nil)
	decodeInto(t, body, &after)

	if after.Title != before.Title || after.Status != before.Status {
		t.Errorf("the instance changed: %q/%s became %q/%s",
			before.Title, before.Status, after.Title, after.Status)
	}
	if after.DueAt == nil || before.DueAt == nil || *after.DueAt != *before.DueAt {
		t.Error("repeating the task moved its due date")
	}
	if after.SeriesID == nil || *after.SeriesID == "" {
		t.Error("the task was not attached to a series")
	}
}

// TestARuleThatDoesNotParseSaysWhy, rather than silently repeating the wrong
// thing for a year.
func TestARuleThatDoesNotParseSaysWhy(t *testing.T) {
	ts := newServer(t)
	session := login(t, ts)

	resp := postForm(t, ts, session, "/w/repeat/101", url.Values{"repeat": {"every blue moon"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("repeat = %d", resp.StatusCode)
	}
	target := resp.Header.Get("Location")
	if !strings.Contains(target, "blue") && !strings.Contains(target, "cannot+read") {
		t.Errorf("the redirect does not carry the parser's complaint: %s", target)
	}

	// And nothing was created from an unreadable rule.
	var task api.Task
	_, body := do(t, ts, http.MethodGet, "/api/v1/tasks/101", nil)
	decodeInto(t, body, &task)
	if task.SeriesID != nil && *task.SeriesID != "" {
		t.Error("an unparseable rule still created a series")
	}
}

// TestChangingTheRuleUpdatesRatherThanForking. Pressing the button twice is a
// correction, not two series racing to materialize instances of the same task.
func TestChangingTheRuleUpdatesRatherThanForking(t *testing.T) {
	ts := newServer(t)
	session := login(t, ts)

	postForm(t, ts, session, "/w/repeat/101", url.Values{"repeat": {"every week"}})
	var first api.Task
	_, body := do(t, ts, http.MethodGet, "/api/v1/tasks/101", nil)
	decodeInto(t, body, &first)

	postForm(t, ts, session, "/w/repeat/101", url.Values{"repeat": {"every 3 days"}})
	var second api.Task
	_, body = do(t, ts, http.MethodGet, "/api/v1/tasks/101", nil)
	decodeInto(t, body, &second)

	if first.SeriesID == nil || second.SeriesID == nil || *first.SeriesID != *second.SeriesID {
		t.Errorf("changing the rule forked the series: %v then %v", first.SeriesID, second.SeriesID)
	}
	_, html := page(t, ts, session, "/t/101")
	if !strings.Contains(html, "every 3 days") {
		t.Error("the detail page still shows the old rule")
	}
}
