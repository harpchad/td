package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/msgraph"
	"github.com/harpchad/td/internal/plugins/mail"
	"github.com/harpchad/td/internal/web"
)

// The browser side of connecting a plugin.
//
// These live here rather than in internal/web because the device code flow is
// a conversation with Microsoft, and internal/web renders. The server drives
// the protocol and hands the web UI something to draw, which is the same
// division the rest of the pair already has.

// plannerConnect starts a device code sign-in and renders the code.
// pluginName reads the plugin off the path and refuses one nothing serves.
//
// A configuration row is created on first save, so an unchecked name here
// would let a form action invent a plugin no runner will ever run: a section
// on the settings page that stays broken and cannot be removed from a browser.
func (s *Server) pluginName(w http.ResponseWriter, r *http.Request) string {
	name := strings.TrimSpace(r.PathValue("name"))
	if s.plugin(name) == nil {
		s.pluginBack(w, r, "there is no plugin called "+name+" on this server")
		return ""
	}
	return name
}

// pluginScopes is what each plugin's sign-in asks Microsoft for.
//
// Per plugin rather than one union, so the mail credential cannot read the
// board and the board credential cannot read the mail. The cost is signing in
// once per plugin, which is the honest price of least privilege.
func pluginScopes(name string) []string {
	switch name {
	case mail.Source:
		return mail.Scopes
	default:
		return nil // nil means msgraph's default, which is the Planner set
	}
}

func (s *Server) pluginConnect(w http.ResponseWriter, r *http.Request) {
	if s.ui == nil {
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.pluginBack(w, r, "could not read that form")
		return
	}

	cfg := msgraph.Config{
		TenantID:  strings.TrimSpace(r.PostFormValue("tenant_id")),
		ClientID:  strings.TrimSpace(r.PostFormValue("client_id")),
		Authority: strings.TrimSpace(r.PostFormValue("authority")),
	}
	if cfg.ClientID == "" {
		s.pluginBack(w, r, "an app registration client id is required")
		return
	}

	name := s.pluginName(w, r)
	if name == "" {
		return
	}
	cfg.Scopes = pluginScopes(name)

	code, err := msgraph.New().StartDeviceCode(r.Context(), cfg)
	if err != nil {
		s.pluginBack(w, r, err.Error())
		return
	}
	s.ui.ConnectPanel(w, r, web.ConnectCode{
		UserCode: code.UserCode, VerificationURI: code.VerificationURI,
		DeviceCode: code.DeviceCode, TenantID: cfg.TenantID, ClientID: cfg.ClientID,
		Authority: cfg.Authority, Interval: code.Interval, Plugin: name,
	})
}

// plannerPoll asks once whether the sign-in has finished.
//
// One attempt per request. The browser is the thing that waits, on an htmx
// timer, because a handler that looped for ten minutes would hold a
// connection open for ten minutes.
func (s *Server) pluginPoll(w http.ResponseWriter, r *http.Request) {
	if s.ui == nil {
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.pluginBack(w, r, "could not read that form")
		return
	}

	// The plugin rides in the form rather than the path, because the panel
	// posts its own fields and the server keeps no pending-login state.
	name := strings.TrimSpace(r.PostFormValue("plugin"))
	if name == "" {
		name = strings.TrimSpace(r.PathValue("name"))
	}
	if s.plugin(name) == nil {
		s.pluginBack(w, r, "there is no plugin called "+name+" on this server")
		return
	}

	cfg := msgraph.Config{
		TenantID:  r.PostFormValue("tenant_id"),
		ClientID:  r.PostFormValue("client_id"),
		Authority: r.PostFormValue("authority"),
		// The same scopes the sign-in started with, or the poll would ask the
		// identity platform for a different grant than the one being approved.
		Scopes: pluginScopes(name),
	}
	interval, _ := strconv.Atoi(r.PostFormValue("interval"))
	if interval <= 0 {
		interval = 5
	}
	// Every field the panel draws with comes back off the form, because each
	// poll re-renders the whole panel. Rebuilding it from only what the poll
	// strictly needs is what made the code and the link disappear five
	// seconds in.
	panel := web.ConnectCode{
		DeviceCode:      r.PostFormValue("device_code"),
		UserCode:        r.PostFormValue("user_code"),
		VerificationURI: r.PostFormValue("verification_uri"),
		TenantID:        cfg.TenantID, ClientID: cfg.ClientID,
		Authority: cfg.Authority, Interval: interval, Plugin: name,
	}

	cred, err := msgraph.New().PollDeviceCode(r.Context(), cfg, panel.DeviceCode)
	switch {
	case errors.Is(err, msgraph.ErrAuthorizationPending):
		s.ui.ConnectPending(w, r, panel, "Waiting for the sign-in…")
		return
	case errors.Is(err, msgraph.ErrSlowDown):
		// Microsoft asked for more space. Widening the interval here is the
		// only place that can honour it, since the browser reads it back off
		// what this renders.
		panel.Interval = interval + 5
		s.ui.ConnectPending(w, r, panel, "Waiting for the sign-in…")
		return
	case err != nil:
		panel.Error = err.Error()
		s.ui.ConnectPanel(w, r, panel)
		return
	}

	body, err := json.Marshal(cred)
	if err != nil {
		s.fail(w, err)
		return
	}
	if err := s.store.SavePluginCredential(r.Context(), name, body, s.Now()); err != nil {
		s.fail(w, err)
		return
	}
	s.logAuth(r, api.KindPluginConnected, s.actorOf(r), map[string]any{
		"plugin": name, "account": cred.Account,
	})
	s.ui.ConnectDone(w, r)
}

// plannerRestart handles a GET into the middle of the connect flow.
//
// A device code is single use and short lived, so there is nothing to render
// for one that arrived on a URL. Sending somebody back to where the flow
// starts is the only useful answer, and it beats the 404 this used to give.
func (s *Server) pluginRestart(w http.ResponseWriter, r *http.Request) {
	s.pluginBack(w, r, "that sign-in link has expired. Start the connection again.")
}

// plannerDisconnect drops the stored credential.
func (s *Server) pluginDisconnect(w http.ResponseWriter, r *http.Request) {
	name := s.pluginName(w, r)
	if name == "" {
		return
	}
	if err := s.store.SavePluginCredential(r.Context(), name, nil, s.Now()); err != nil {
		s.fail(w, err)
		return
	}
	s.logAuth(r, api.KindPluginDisconnected, s.actorOf(r), map[string]any{"plugin": name})
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

// plannerRunNow syncs on demand.
func (s *Server) pluginRunNow(w http.ResponseWriter, r *http.Request) {
	name := s.pluginName(w, r)
	if name == "" {
		return
	}
	runner := s.plugin(name)
	cfg, err := s.store.PluginConfigByName(r.Context(), name)
	if err != nil {
		s.fail(w, err)
		return
	}

	if _, err := runner.Run(r.Context(), cfg, r.URL.Query().Get("relink") != "", s.Now()); err != nil {
		// The run recorded its own failure, so the settings page will show it
		// in full. The redirect only has to get you back there.
		s.pluginBack(w, r, err.Error())
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

// plannerMap answers one unresolved identity, either by naming somebody td
// already knows or by adding them as a new person.
//
// The second half is not a nicety. The reason most of these rows exist is a
// handle collision, and a collision usually means this is a different person
// who happens to share a first name. Offering only "pick from the list" would
// be offering the one answer that is wrong.
func (s *Server) pluginMap(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.pluginBack(w, r, "could not read that form")
		return
	}
	handle := strings.TrimPrefix(strings.TrimSpace(r.PostFormValue("handle")), "@")
	source := r.PostFormValue("source")
	external := r.PostFormValue("source_user")

	var person api.Person
	var err error
	switch {
	case handle != "":
		person, err = s.store.ResolvePerson(r.Context(), handle)
		if err != nil {
			s.pluginBack(w, r, "no person @"+handle)
			return
		}

	case strings.TrimSpace(r.PostFormValue("new_handle")) != "":
		newHandle := strings.TrimPrefix(strings.TrimSpace(r.PostFormValue("new_handle")), "@")
		name := strings.TrimSpace(r.PostFormValue("name"))
		if name == "" {
			name = newHandle
		}
		person, err = s.store.CreatePerson(r.Context(), api.Person{
			Name: name, Handle: newHandle,
			// The address comes across too, so a second source reporting the
			// same person resolves on it rather than asking again.
			Email: strings.TrimSpace(r.PostFormValue("email")),
		}, s.Now())
		if err != nil {
			s.pluginBack(w, r, "could not add @"+newHandle+": "+message(err))
			return
		}

	default:
		s.pluginBack(w, r, "pick who that is, or give a handle to add them as somebody new")
		return
	}
	if err := s.store.LinkIdentity(r.Context(), person.ID, source, external); err != nil {
		s.fail(w, err)
		return
	}
	// The mapping is stored, but the links on existing tasks are not redrawn
	// until something re-applies them. Saying so beats leaving somebody to
	// wonder why the person page is still empty.
	s.pluginBack(w, r, "mapped to @"+person.Handle+". Re-apply everything to backfill the links.")
}

func (s *Server) pluginBack(w http.ResponseWriter, r *http.Request, message string) {
	http.Redirect(w, r, "/settings?e="+url.QueryEscape(message), http.StatusSeeOther)
}
