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
	"github.com/harpchad/td/internal/web"
)

// The browser side of connecting Planner.
//
// These live here rather than in internal/web because the device code flow is
// a conversation with Microsoft, and internal/web renders. The server drives
// the protocol and hands the web UI something to draw, which is the same
// division the rest of the pair already has.

// plannerConnect starts a device code sign-in and renders the code.
func (s *Server) plannerConnect(w http.ResponseWriter, r *http.Request) {
	if s.ui == nil {
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.plannerBack(w, r, "could not read that form")
		return
	}

	cfg := msgraph.Config{
		TenantID: strings.TrimSpace(r.PostFormValue("tenant_id")),
		ClientID: strings.TrimSpace(r.PostFormValue("client_id")),
	}
	if cfg.ClientID == "" {
		s.plannerBack(w, r, "an app registration client id is required")
		return
	}

	code, err := msgraph.New().StartDeviceCode(r.Context(), cfg)
	if err != nil {
		s.plannerBack(w, r, err.Error())
		return
	}
	s.ui.ConnectPanel(w, r, web.ConnectCode{
		UserCode: code.UserCode, VerificationURI: code.VerificationURI,
		DeviceCode: code.DeviceCode, TenantID: cfg.TenantID, ClientID: cfg.ClientID,
		Interval: code.Interval,
	})
}

// plannerPoll asks once whether the sign-in has finished.
//
// One attempt per request. The browser is the thing that waits, on an htmx
// timer, because a handler that looped for ten minutes would hold a
// connection open for ten minutes.
func (s *Server) plannerPoll(w http.ResponseWriter, r *http.Request) {
	if s.ui == nil {
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.plannerBack(w, r, "could not read that form")
		return
	}

	cfg := msgraph.Config{
		TenantID: r.PostFormValue("tenant_id"),
		ClientID: r.PostFormValue("client_id"),
	}
	interval, _ := strconv.Atoi(r.PostFormValue("interval"))
	if interval <= 0 {
		interval = 5
	}
	panel := web.ConnectCode{
		DeviceCode: r.PostFormValue("device_code"),
		TenantID:   cfg.TenantID, ClientID: cfg.ClientID, Interval: interval,
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
	if err := s.store.SavePluginCredential(r.Context(), "planner", body, s.Now()); err != nil {
		s.fail(w, err)
		return
	}
	s.logAuth(r, api.KindPluginConnected, s.actorOf(r), map[string]any{
		"plugin": "planner", "account": cred.Account,
	})
	s.ui.ConnectDone(w, r)
}

// plannerRestart handles a GET into the middle of the connect flow.
//
// A device code is single use and short lived, so there is nothing to render
// for one that arrived on a URL. Sending somebody back to where the flow
// starts is the only useful answer, and it beats the 404 this used to give.
func (s *Server) plannerRestart(w http.ResponseWriter, r *http.Request) {
	s.plannerBack(w, r, "that sign-in link has expired. Start the connection again.")
}

// plannerDisconnect drops the stored credential.
func (s *Server) plannerDisconnect(w http.ResponseWriter, r *http.Request) {
	if err := s.store.SavePluginCredential(r.Context(), "planner", nil, s.Now()); err != nil {
		s.fail(w, err)
		return
	}
	s.logAuth(r, api.KindPluginDisconnected, s.actorOf(r), map[string]any{"plugin": "planner"})
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

// plannerRunNow syncs on demand.
func (s *Server) plannerRunNow(w http.ResponseWriter, r *http.Request) {
	runner := s.plugin("planner")
	if runner == nil {
		s.plannerBack(w, r, "the Planner mirror is not available on this server")
		return
	}
	cfg, err := s.store.PluginConfigByName(r.Context(), "planner")
	if err != nil {
		s.fail(w, err)
		return
	}

	if _, err := runner.Run(r.Context(), cfg, r.URL.Query().Get("relink") != "", s.Now()); err != nil {
		// The run recorded its own failure, so the settings page will show it
		// in full. The redirect only has to get you back there.
		s.plannerBack(w, r, err.Error())
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
func (s *Server) plannerMap(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.plannerBack(w, r, "could not read that form")
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
			s.plannerBack(w, r, "no person @"+handle)
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
			s.plannerBack(w, r, "could not add @"+newHandle+": "+message(err))
			return
		}

	default:
		s.plannerBack(w, r, "pick who that is, or give a handle to add them as somebody new")
		return
	}
	if err := s.store.LinkIdentity(r.Context(), person.ID, source, external); err != nil {
		s.fail(w, err)
		return
	}
	// The mapping is stored, but the links on existing tasks are not redrawn
	// until something re-applies them. Saying so beats leaving somebody to
	// wonder why the person page is still empty.
	s.plannerBack(w, r, "mapped to @"+person.Handle+". Re-apply everything to backfill the links.")
}

func (s *Server) plannerBack(w http.ResponseWriter, r *http.Request, message string) {
	http.Redirect(w, r, "/settings?e="+url.QueryEscape(message), http.StatusSeeOther)
}
