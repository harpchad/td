package server_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// The plugin control surface. What matters here is that a credential never
// comes back out, that the settings form cannot destroy one, and that the
// settings page renders without CSP-dropped controls.

// TestAPluginCredentialNeverLeavesTheServer. The whole reason the mirror moved
// server-side is that the credential lives there; a route that hands it back
// would undo that.
func TestAPluginCredentialNeverLeavesTheServer(t *testing.T) {
	ts := newServer(t)

	const refresh = "a-refresh-token-nobody-should-ever-see"
	stored, err := json.Marshal(map[string]any{
		"refresh_token": refresh,
		"account":       "chad@example.invalid",
		"config":        map[string]string{"client_id": "abc", "tenant_id": "t"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := ts.store.SavePluginCredential(t.Context(), "planner", stored, ts.now); err != nil {
		t.Fatal(err)
	}

	resp, body := do(t, ts, http.MethodGet, "/api/v1/plugins/planner", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %s", resp.StatusCode, body)
	}
	if strings.Contains(string(body), refresh) {
		t.Fatal("the API returned the refresh token")
	}
	for _, word := range []string{"refresh_token", "access_token", "credential"} {
		if strings.Contains(string(body), word) {
			t.Errorf("the response mentions %q", word)
		}
	}

	var view struct {
		Connected bool   `json:"connected"`
		Account   string `json:"account"`
	}
	decodeInto(t, body, &view)
	if !view.Connected {
		t.Error("connected = false with a credential stored")
	}
	// The account is shown because "as whom" is the question somebody has.
	if view.Account != "chad@example.invalid" {
		t.Errorf("account = %q", view.Account)
	}

	// And the settings page does not leak it either.
	session := login(t, ts)
	_, html := page(t, ts, session, "/settings")
	if strings.Contains(html, refresh) {
		t.Fatal("the settings page rendered the refresh token")
	}
}

// TestSavingSettingsCannotDestroyTheCredential. A settings form that could
// blank a stored refresh token by omitting a field is one that eventually
// will, and the failure would look like "Planner just stopped working".
func TestSavingSettingsCannotDestroyTheCredential(t *testing.T) {
	ts := newServer(t)

	if err := ts.store.SavePluginCredential(t.Context(), "planner",
		json.RawMessage(`{"refresh_token":"keep-me"}`), ts.now); err != nil {
		t.Fatal(err)
	}

	resp, body := do(t, ts, http.MethodPut, "/api/v1/plugins/planner", map[string]any{
		"enabled":          true,
		"settings":         map[string]any{"plans": []string{"PLAN-1"}},
		"interval_minutes": 30,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %s", resp.StatusCode, body)
	}

	cfg, err := ts.store.PluginConfigByName(t.Context(), "planner")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Connected() {
		t.Fatal("saving settings dropped the credential")
	}
	if !cfg.Enabled || cfg.IntervalMinutes != 30 {
		t.Errorf("settings did not save: %+v", cfg)
	}

	// And the web form cannot either.
	session := login(t, ts)
	if resp := postForm(t, ts, session, "/w/planner", url.Values{
		"enabled": {"1"}, "plans": {"PLAN-1\n\nPLAN-2\n"}, "interval": {"20"},
	}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("form save = %d", resp.StatusCode)
	}
	cfg, err = ts.store.PluginConfigByName(t.Context(), "planner")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Connected() {
		t.Fatal("the web form dropped the credential")
	}
	// Blank lines are not plan ids.
	var settings struct {
		Plans []string `json:"plans"`
	}
	decodeInto(t, cfg.Settings, &settings)
	if len(settings.Plans) != 2 {
		t.Errorf("plans = %v, want the blank line dropped", settings.Plans)
	}
}

// TestDisconnectIsTheOnlyWayACredentialGoes.
func TestDisconnectIsTheOnlyWayACredentialGoes(t *testing.T) {
	ts := newServer(t)
	if err := ts.store.SavePluginCredential(t.Context(), "planner",
		json.RawMessage(`{"refresh_token":"x"}`), ts.now); err != nil {
		t.Fatal(err)
	}

	resp, _ := do(t, ts, http.MethodPost, "/api/v1/plugins/planner/disconnect", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	cfg, err := ts.store.PluginConfigByName(t.Context(), "planner")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Connected() {
		t.Error("still connected after a disconnect")
	}
}

// TestAnUnconnectedPluginIsNeverScheduled. Enabled but never connected is an
// ordinary state, and the scheduler must not spin on it every minute.
func TestAnUnconnectedPluginIsNeverScheduled(t *testing.T) {
	ts := newServer(t)

	if err := ts.store.SavePluginSettings(t.Context(), "planner", true,
		json.RawMessage(`{"plans":["PLAN-1"]}`), 15, ts.now); err != nil {
		t.Fatal(err)
	}
	due, err := ts.store.DuePlugins(t.Context(), ts.now)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 0 {
		t.Errorf("%d plugins due while none is connected", len(due))
	}

	// Connected, and it becomes due.
	if err := ts.store.SavePluginCredential(t.Context(), "planner",
		json.RawMessage(`{"refresh_token":"x"}`), ts.now); err != nil {
		t.Fatal(err)
	}
	due, err = ts.store.DuePlugins(t.Context(), ts.now)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 {
		t.Fatalf("%d due, want the connected one", len(due))
	}

	// Having just run, it is not due again until the interval elapses.
	if err := ts.store.RecordPluginRun(t.Context(), "planner", "0 created", nil, nil, ts.now); err != nil {
		t.Fatal(err)
	}
	due, err = ts.store.DuePlugins(t.Context(), ts.now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 0 {
		t.Error("due again one minute into a fifteen minute interval")
	}
	due, err = ts.store.DuePlugins(t.Context(), ts.now.Add(16*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 {
		t.Error("not due after the interval elapsed")
	}
}

// TestADisabledPluginNeverRuns, whatever else is configured.
func TestADisabledPluginNeverRuns(t *testing.T) {
	ts := newServer(t)
	if err := ts.store.SavePluginSettings(t.Context(), "planner", false,
		json.RawMessage(`{"plans":["PLAN-1"]}`), 15, ts.now); err != nil {
		t.Fatal(err)
	}
	if err := ts.store.SavePluginCredential(t.Context(), "planner",
		json.RawMessage(`{"refresh_token":"x"}`), ts.now); err != nil {
		t.Fatal(err)
	}
	due, err := ts.store.DuePlugins(t.Context(), ts.now)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 0 {
		t.Error("a disabled plugin was scheduled")
	}
}

// TestTheSettingsPageRendersThePlannerSection, and does it without any
// control the CSP would silently drop.
func TestTheSettingsPageRendersThePlannerSection(t *testing.T) {
	ts := newServer(t)
	session := login(t, ts)

	_, html := page(t, ts, session, "/settings")
	for _, want := range []string{
		"Microsoft Planner", "one plan id per line", "Tasks.Read",
		`name="client_id"`, `action="/w/planner"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("the settings page has no %q", want)
		}
	}
	if strings.Contains(html, "style=\"") {
		t.Error("the section carries an inline style, which the CSP drops")
	}
	for _, handler := range []string{"onclick=", "onchange=", "onsubmit="} {
		if strings.Contains(html, handler) {
			t.Errorf("the section carries an inline %s handler", handler)
		}
	}
}

// TestTheServerSideMirrorWorkflow is the thing this whole change exists for:
// configure it in the browser, let the server hold the credential, have the
// scheduler run it, and answer the unmatched people from the settings page
// rather than from a terminal.
func TestTheServerSideMirrorWorkflow(t *testing.T) {
	ts := newServer(t)
	graph := newFakeGraph(t)
	session := login(t, ts)

	// 1. Configure it, the way the settings form does.
	if resp := postForm(t, ts, session, "/w/planner", url.Values{
		"enabled": {"1"}, "interval": {"15"},
		"plans": {"xqQg5FS2LkCp935s-FIFm2QAFkHM"},
	}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("save = %d", resp.StatusCode)
	}
	// The form does not carry the Graph endpoint; a test has to point it at
	// the stub, which is the one thing a real deployment does not do.
	settings, err := json.Marshal(map[string]any{
		"plans":    []string{"xqQg5FS2LkCp935s-FIFm2QAFkHM"},
		"endpoint": graph.URL + "/v1.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := ts.store.SavePluginSettings(t.Context(), "planner", true, settings, 15, ts.now); err != nil {
		t.Fatal(err)
	}

	// 2. A completed device code sign-in leaves this behind.
	cred, err := json.Marshal(map[string]any{
		"config":        map[string]string{"client_id": "c", "tenant_id": "t"},
		"refresh_token": "rt", "account": "chad@example.invalid",
		"access_token": "graph-token", "expires_at": ts.now.Add(time.Hour).UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := ts.store.SavePluginCredential(t.Context(), "planner", cred, ts.now); err != nil {
		t.Fatal(err)
	}

	// 3. The scheduler picks it up, with nothing at a terminal.
	due, err := ts.store.DuePlugins(t.Context(), ts.now)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 {
		t.Fatalf("%d plugins due, want the configured one", len(due))
	}
	if resp := postForm(t, ts, session, "/w/planner/run", url.Values{}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("run = %d", resp.StatusCode)
	}

	mirrored, err := ts.store.List(t.Context(), "src:planner", ts.now)
	if err != nil {
		t.Fatal(err)
	}
	if len(mirrored) != 3 {
		t.Fatalf("%d mirrored tasks, want the fixture's 3", len(mirrored))
	}

	// 4. The settings page shows who it would not guess at, with a dropdown.
	_, html := page(t, ts, session, "/settings")
	if !strings.Contains(html, "People it would not guess at") {
		t.Fatal("the settings page does not show the unmatched people")
	}
	for _, want := range []string{"Stacey Whitlock", `name="source_user"`, "who is this?"} {
		if !strings.Contains(html, want) {
			t.Errorf("the unmatched list has no %q", want)
		}
	}

	// 5. Answer one from the browser.
	if resp := postForm(t, ts, session, "/w/planner/map", url.Values{
		"handle": {"stacey"}, "source": {"planner"},
		"source_user": {"8f3d2e11-0000-4a2b-9c3d-000000000001"},
	}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("map = %d", resp.StatusCode)
	}
	stacey, err := ts.store.PersonByHandle(t.Context(), "stacey")
	if err != nil {
		t.Fatal(err)
	}
	found, err := ts.store.PersonByIdentity(t.Context(), "planner", "8f3d2e11-0000-4a2b-9c3d-000000000001")
	if err != nil || found.ID != stacey.ID {
		t.Fatalf("the mapping did not stick: %v", err)
	}

	// 6. Re-apply, and the link appears without waiting for Planner to change.
	if resp := postForm(t, ts, session, "/w/planner/run?relink=1", url.Values{}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("relink = %d", resp.StatusCode)
	}
	mirrored, err = ts.store.List(t.Context(), "src:planner", ts.now)
	if err != nil {
		t.Fatal(err)
	}
	var linked bool
	for _, task := range mirrored {
		for _, p := range task.People {
			if p.PersonID == stacey.ID {
				linked = true
			}
		}
	}
	if !linked {
		t.Error("the mapped person was not backfilled by a relink")
	}
}

// TestTheConnectPanelIsAWholePageWhenTheBrowserNavigatedToIt.
//
// This is the bug that shipped: the connect POST answered with a bare
// fragment, so the response had no layout, no stylesheet and no htmx. Nothing
// polled, and the only working control was the submit button, which fell back
// to a native GET at the current URL and 404ed with the device code sitting
// in the address bar.
func TestTheConnectPanelIsAWholePageWhenTheBrowserNavigatedToIt(t *testing.T) {
	ts := newServer(t)
	session := login(t, ts)

	// An unreachable authority, so the panel renders its error rather than
	// this test reaching Microsoft. The shape of the response is what is
	// under test, not the sign-in.
	resp := postForm(t, ts, session, "/w/planner/connect", url.Values{
		"tenant_id": {"t"}, "client_id": {"c"},
	})
	// Whatever it says, it must not be a fragment.
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

// TestThePollFormWorksWithTheScriptOff is the rule the rest of this UI
// follows and that the first version of this broke. Every action is a real
// form: with JavaScript off it posts and the server answers.
func TestThePollFormWorksWithTheScriptOff(t *testing.T) {
	ts := newServer(t)
	session := login(t, ts)

	// A poll with a device code Microsoft will never accept. What matters is
	// that it is a POST to a route that exists and comes back as a page, not
	// that the sign-in succeeds.
	resp := postForm(t, ts, session, "/w/planner/poll", url.Values{
		"device_code": {"nonsense"}, "tenant_id": {"t"},
		"client_id": {"c"}, "interval": {"5"},
	})
	if resp.StatusCode == http.StatusNotFound {
		t.Fatal("the poll route does not exist for a plain form post")
	}
}

// TestAGetIntoTheConnectFlowRedirectsRatherThan404. A refresh, a back button,
// or a bookmarked mid-flow URL all land on GET, and a device code is single
// use so there is nothing to render for one.
func TestAGetIntoTheConnectFlowRedirectsRatherThan404(t *testing.T) {
	ts := newServer(t)
	session := login(t, ts)

	for _, path := range []string{
		"/w/planner/connect?device_code=x&tenant_id=t&client_id=c&interval=5",
		"/w/planner/poll",
	} {
		resp := getWithSession(t, ts, session, path)
		if resp.StatusCode == http.StatusNotFound {
			t.Errorf("GET %s = 404", path)
		}
		if resp.StatusCode != http.StatusSeeOther {
			t.Errorf("GET %s = %d, want a redirect back to settings", path, resp.StatusCode)
		}
	}
}

// TestTheDeviceCodeNeverTravelsInAURL. It arrived in the address bar the
// first time round, which put a credential-adjacent value into browser
// history and anything the page links to.
func TestTheDeviceCodeNeverTravelsInAURL(t *testing.T) {
	ts := newServer(t)
	session := login(t, ts)

	_, html := page(t, ts, session, "/settings")
	// The connect form posts; nothing about this flow is a GET with fields.
	if strings.Contains(html, `action="/w/planner/connect?`) {
		t.Error("the connect form puts fields in the query string")
	}
	if !strings.Contains(html, `action="/w/planner/connect" method="post"`) &&
		!strings.Contains(html, `action="/w/planner/connect"`) {
		t.Error("the connect form has no explicit action")
	}
}
