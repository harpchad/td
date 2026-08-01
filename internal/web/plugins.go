package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/store"
	"github.com/harpchad/td/internal/sync"
)

// The Planner section of the settings page.
//
// Everything here is a form the server renders and a POST it redirects from,
// like the rest of the web UI, with one exception: the device code sign-in
// needs to ask Microsoft repeatedly whether you have finished. That is an
// htmx poll on a fragment rather than a script, so the Content-Security-Policy
// still needs no unsafe-inline and no per-page hash.

// plannerView is the section as the template reads it.
type plannerView struct {
	Enabled   bool
	Connected bool
	Account   string
	Plans     string
	Interval  int
	// Everything is the whole-plan opt-in. A Planner plan is a team board, so
	// the default is what is assigned to you.
	Everything bool

	LastRunAt  string
	LastResult string
	LastError  string

	Unresolved []unresolvedRow
	People     []personChoice
}

// unresolvedRow is one upstream identity waiting for an answer.
type unresolvedRow struct {
	Source     string
	SourceUser string
	Who        string
	Reason     string

	// Name and Email travel with the row so a new person can be made from
	// them without asking for anything already known.
	Name  string
	Email string
	// Suggested is a free handle derived from the full name. The reason most
	// of these are unresolved is that the first name is taken, so the useful
	// default is the one that is not: stacey-kennedy rather than stacey.
	Suggested string
}

type personChoice struct {
	Handle string
	Name   string
}

// plannerSection builds the view. A plugin nobody has configured renders as
// an empty form rather than as nothing, because "where do I turn this on" is
// the first question somebody has.
func (u *UI) plannerSection(ctx context.Context) plannerView {
	svc, ok := u.svc.(interface {
		PluginConfigByName(ctx context.Context, name string) (store.PluginConfig, error)
	})
	if !ok {
		return plannerView{}
	}
	cfg, err := svc.PluginConfigByName(ctx, "planner")
	if err != nil {
		u.log.Error("reading planner config", "err", err)
		return plannerView{}
	}

	out := plannerView{
		Enabled: cfg.Enabled, Connected: cfg.Connected(),
		Interval: cfg.IntervalMinutes,
	}
	if out.Interval == 0 {
		out.Interval = 15
	}

	var settings struct {
		Plans   []string `json:"plans"`
		Include string   `json:"include"`
	}
	if len(cfg.Settings) > 0 {
		_ = json.Unmarshal(cfg.Settings, &settings)
	}
	// One per line in the textarea: a plan id is long and opaque, and a
	// comma-separated field of them is unreadable.
	out.Plans = strings.Join(settings.Plans, "\n")
	out.Everything = settings.Include == "all"

	if cfg.LastRunAt != nil {
		out.LastRunAt = *cfg.LastRunAt
	}
	if cfg.LastResult != nil {
		out.LastResult = *cfg.LastResult
	}
	if cfg.LastError != nil {
		out.LastError = *cfg.LastError
	}
	if cfg.Connected() {
		var cred struct {
			Account string `json:"account"`
		}
		if json.Unmarshal(cfg.Credential, &cred) == nil {
			out.Account = cred.Account
		}
	}

	var pending []sync.Unresolved
	if len(cfg.LastUnresolved) > 0 {
		_ = json.Unmarshal(cfg.LastUnresolved, &pending)
	}
	if len(pending) == 0 {
		return out
	}

	// The existing people are needed twice: to offer as choices, and to know
	// which handles are taken so a suggestion does not collide with one.
	taken := map[string]bool{}
	if lister, ok := u.svc.(interface {
		People(ctx context.Context) ([]api.Person, error)
	}); ok {
		if people, err := lister.People(ctx); err == nil {
			for _, p := range people {
				out.People = append(out.People, personChoice{Handle: p.Handle, Name: p.Name})
				taken[p.Handle] = true
			}
		}
	}

	for _, p := range pending {
		who := p.Name
		if p.Email != "" {
			who = strings.TrimSpace(who + " <" + p.Email + ">")
		}
		if who == "" {
			who = p.SourceUser
		}
		suggested := suggestHandle(p.Name, p.Email, taken)
		// Reserved as it is suggested, so two new people in the same batch do
		// not both get offered the same handle and the second one fail.
		taken[suggested] = true

		out.Unresolved = append(out.Unresolved, unresolvedRow{
			Source: p.Source, SourceUser: p.SourceUser,
			Who: who, Reason: p.Reason,
			Name: p.Name, Email: p.Email, Suggested: suggested,
		})
	}
	return out
}

// savePlanner writes the settings form.
func (u *UI) savePlanner(w http.ResponseWriter, r *http.Request) {
	svc, ok := u.svc.(interface {
		SavePluginSettings(ctx context.Context, name string, enabled bool, settings json.RawMessage, interval int, now time.Time) error
	})
	if !ok {
		http.Redirect(w, r, "/settings?e=plugins+are+not+available", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/settings?e=could+not+read+that+form", http.StatusSeeOther)
		return
	}

	// One plan id per line, blanks dropped. Somebody pasting a list with a
	// trailing newline should not get an empty plan that fails every run.
	var plans []string
	for _, line := range strings.Split(r.PostFormValue("plans"), "\n") {
		if id := strings.TrimSpace(line); id != "" {
			plans = append(plans, id)
		}
	}
	// Assigned unless the whole board was asked for on purpose.
	include := "assigned"
	if r.PostFormValue("everything") != "" {
		include = "all"
	}
	settings, err := json.Marshal(map[string]any{"plans": plans, "include": include})
	if err != nil {
		u.fail(w, r, err)
		return
	}

	interval, _ := strconv.Atoi(r.PostFormValue("interval"))
	if interval <= 0 {
		interval = 15
	}
	// A minute is the tick, so anything faster is a lie. It is also more
	// traffic at somebody's tenant than a task list can justify.
	if interval < 5 {
		interval = 5
	}

	enabled := r.PostFormValue("enabled") != ""
	if err := svc.SavePluginSettings(r.Context(), "planner", enabled, settings, interval, u.Now()); err != nil {
		u.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

// ConnectPanel renders the device code for somebody to type elsewhere.
//
// A full page when the browser navigated here, a fragment when htmx is asking
// for a swap. Getting that backwards is what broke this the first time: the
// connect POST answered with a bare fragment, so the page had no layout, no
// stylesheet, and no htmx. Nothing polled, and the only control that did
// anything was the submit button, which fell back to a native GET at the
// current URL and 404ed with the device code in the address bar.
//
// The device code itself rides in a hidden field and is posted back on each
// poll, so the server keeps no pending-login state. It is single use, expires
// in minutes, and is worthless without the sign-in that follows, so a table
// and an expiry sweep would be bought for nothing.
func (u *UI) ConnectPanel(w http.ResponseWriter, r *http.Request, code ConnectCode) {
	data := u.base(r, "Connect Planner")
	data.Connect = code
	if r.Header.Get("HX-Request") == "true" {
		u.renderFragment(w, "connect", data)
		return
	}
	u.render(w, "connect", data)
}

// ConnectPending renders the "still waiting" state, which htmx replaces
// itself with until the sign-in completes.
func (u *UI) ConnectPending(w http.ResponseWriter, r *http.Request, code ConnectCode, message string) {
	code.Pending = true
	code.Message = message
	u.ConnectPanel(w, r, code)
}

// ConnectDone sends the browser back to the settings page, which is where the
// newly connected state is rendered from.
//
// Two ways, because there are two kinds of caller. htmx swaps responses into
// the page and needs to be told to navigate; a browser that submitted a form
// needs an ordinary redirect. Sending only the header would leave somebody
// with JavaScript off looking at a blank fragment after a successful sign-in.
func (u *UI) ConnectDone(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", "/settings")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

// ConnectCode is what the connect fragment renders.
type ConnectCode struct {
	UserCode        string
	VerificationURI string
	DeviceCode      string
	TenantID        string
	ClientID        string
	// Interval is how many seconds htmx waits between polls, as Microsoft
	// asked for rather than as we guessed.
	Interval int
	Pending  bool
	Message  string
	Error    string
}

// renderFragment renders one template without the page around it, for htmx.
func (u *UI) renderFragment(w http.ResponseWriter, page string, data pageData) {
	t, ok := u.tmpl[page]
	if !ok {
		http.Error(w, "no such fragment", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, "body", data); err != nil {
		u.log.Error("render fragment", "page", page, "err", err)
	}
}

// suggestHandle proposes a free handle for somebody new.
//
// The whole reason most of these are unresolved is that their first name is
// already taken, so suggesting the first name again would be suggesting the
// thing that does not work. The full name is what distinguishes them:
// "Stacey Kennedy" becomes stacey-kennedy. A name with nothing usable in it
// falls back to the address, and a collision gets a number rather than an
// error somebody has to read and fix by hand.
func suggestHandle(name, email string, taken map[string]bool) string {
	base := slugHandle(name)
	if base == "" {
		base = slugHandle(localPart(email))
	}
	if base == "" {
		return ""
	}
	if !taken[base] {
		return base
	}
	for n := 2; n < 100; n++ {
		candidate := base + "-" + strconv.Itoa(n)
		if !taken[candidate] {
			return candidate
		}
	}
	return base
}

// slugHandle turns a display name into what you would type after @.
func slugHandle(name string) string {
	var parts []string
	for _, word := range strings.Fields(strings.ToLower(name)) {
		var b strings.Builder
		for _, r := range word {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
				b.WriteRune(r)
			}
		}
		if b.Len() > 0 {
			parts = append(parts, b.String())
		}
	}
	// Two parts at most. A middle name in a directory entry should not become
	// a handle nobody wants to type.
	if len(parts) > 2 {
		parts = parts[:2]
	}
	return strings.Join(parts, "-")
}

func localPart(email string) string {
	at := strings.IndexByte(email, '@')
	if at <= 0 {
		return ""
	}
	return email[:at]
}
