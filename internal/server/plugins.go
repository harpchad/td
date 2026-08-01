package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/msgraph"
	"github.com/harpchad/td/internal/plugins/planner"
	"github.com/harpchad/td/internal/store"
	"github.com/harpchad/td/internal/sync"
)

// The plugin control surface. Configuration lives in the database rather than
// in config.toml so it can be edited from the browser, which is the whole
// reason this moved server-side: a mirror that needs a laptop and a CLI to
// stay current is not a mirror.

// PluginView is a plugin as the settings page sees it.
//
// It carries no credential and never will. Connected says whether one exists
// and Account says who it belongs to, which is everything a person needs to
// answer "is this working and as whom".
type PluginView struct {
	Name      string          `json:"name"`
	Enabled   bool            `json:"enabled"`
	Settings  json.RawMessage `json:"settings"`
	Interval  int             `json:"interval_minutes"`
	Connected bool            `json:"connected"`
	Account   string          `json:"account,omitempty"`

	LastRunAt  *string `json:"last_run_at,omitempty"`
	LastResult *string `json:"last_result,omitempty"`
	LastError  *string `json:"last_error,omitempty"`
	// LastUnresolved is who the last run would not guess at. It is on the API
	// as well as the settings page because the answer is a person's to give
	// and they may not be in a browser.
	LastUnresolved json.RawMessage `json:"last_unresolved,omitempty"`
}

func (s *Server) pluginView(cfg store.PluginConfig) PluginView {
	out := PluginView{
		Name: cfg.Name, Enabled: cfg.Enabled, Settings: cfg.Settings,
		Interval: cfg.IntervalMinutes, Connected: cfg.Connected(),
		LastRunAt: cfg.LastRunAt, LastResult: cfg.LastResult, LastError: cfg.LastError,
		LastUnresolved: cfg.LastUnresolved,
	}
	if out.Settings == nil {
		out.Settings = json.RawMessage("{}")
	}
	// Only the display name comes out of the credential. Everything else in
	// there is the credential.
	if cfg.Connected() {
		var cred msgraph.Credential
		if json.Unmarshal(cfg.Credential, &cred) == nil {
			out.Account = cred.Account
		}
	}
	return out
}

func (s *Server) getPlugin(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.store.PluginConfigByName(r.Context(), r.PathValue("name"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.pluginView(cfg))
}

// pluginSettings is the body of a save. The credential is deliberately absent:
// a settings form that could blank a stored refresh token by omitting a field
// is a settings form that eventually will.
type pluginSettings struct {
	Enabled  bool            `json:"enabled"`
	Settings json.RawMessage `json:"settings"`
	Interval int             `json:"interval_minutes"`
}

func (s *Server) savePlugin(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if s.plugin(name) == nil {
		s.fail(w, &api.Error{Code: api.ErrNotFound, Message: "no plugin called " + name})
		return
	}

	var in pluginSettings
	if err := decode(r, &in); err != nil {
		s.fail(w, err)
		return
	}
	// Parsed before it is stored, so a typo is a 400 now rather than a failed
	// run in fifteen minutes.
	if _, err := planner.ParseSettings(in.Settings); err != nil {
		s.fail(w, &api.Error{Code: api.ErrBadRequest, Message: err.Error()})
		return
	}

	if err := s.store.SavePluginSettings(r.Context(), name, in.Enabled, in.Settings, in.Interval, s.Now()); err != nil {
		s.fail(w, err)
		return
	}
	cfg, err := s.store.PluginConfigByName(r.Context(), name)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.pluginView(cfg))
}

// connectRequest starts a device code sign-in.
type connectRequest struct {
	TenantID  string `json:"tenant_id"`
	ClientID  string `json:"client_id"`
	Authority string `json:"authority,omitempty"`
}

// connectResponse is what to show the person, plus the handle to poll with.
//
// The device code is returned to the browser rather than kept server-side.
// It is single-use, expires in minutes, and is worthless without the sign-in
// that follows, so holding pending-login state on the server would be a table
// and an expiry sweep bought for nothing.
type connectResponse struct {
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	Message         string `json:"message"`
	DeviceCode      string `json:"device_code"`
	Interval        int    `json:"interval"`
	ExpiresIn       int    `json:"expires_in"`
}

func (s *Server) connectPlugin(w http.ResponseWriter, r *http.Request) {
	var in connectRequest
	if err := decode(r, &in); err != nil {
		s.fail(w, err)
		return
	}
	if in.ClientID == "" {
		s.fail(w, &api.Error{Code: api.ErrBadRequest, Message: "an app registration client id is required"})
		return
	}

	code, err := msgraph.New().StartDeviceCode(r.Context(), msgraph.Config{
		TenantID: in.TenantID, ClientID: in.ClientID, Authority: in.Authority,
	})
	if err != nil {
		s.fail(w, &api.Error{Code: api.ErrBadRequest, Message: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, connectResponse{
		UserCode: code.UserCode, VerificationURI: code.VerificationURI,
		Message: code.Message, DeviceCode: code.DeviceCode,
		Interval: code.Interval, ExpiresIn: code.ExpiresIn,
	})
}

// pollRequest finishes a device code sign-in.
type pollRequest struct {
	TenantID   string `json:"tenant_id"`
	ClientID   string `json:"client_id"`
	Authority  string `json:"authority,omitempty"`
	DeviceCode string `json:"device_code"`
}

func (s *Server) pollPlugin(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	var in pollRequest
	if err := decode(r, &in); err != nil {
		s.fail(w, err)
		return
	}

	cfg := msgraph.Config{TenantID: in.TenantID, ClientID: in.ClientID, Authority: in.Authority}
	cred, err := msgraph.New().PollDeviceCode(r.Context(), cfg, in.DeviceCode)
	switch {
	case errors.Is(err, msgraph.ErrAuthorizationPending), errors.Is(err, msgraph.ErrSlowDown):
		// Not a failure. 202 says "keep asking" without the browser having to
		// pattern-match on an error string.
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "pending"})
		return
	case err != nil:
		s.fail(w, &api.Error{Code: api.ErrBadRequest, Message: err.Error()})
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

	stored, err := s.store.PluginConfigByName(r.Context(), name)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.pluginView(stored))
}

// disconnectPlugin drops the stored credential.
func (s *Server) disconnectPlugin(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.store.SavePluginCredential(r.Context(), name, nil, s.Now()); err != nil {
		s.fail(w, err)
		return
	}
	s.logAuth(r, api.KindPluginDisconnected, s.actorOf(r), map[string]any{"plugin": name})
	w.WriteHeader(http.StatusNoContent)
}

// runPlugin syncs now, rather than waiting for the schedule.
func (s *Server) runPlugin(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	runner := s.plugin(name)
	if runner == nil {
		s.fail(w, &api.Error{Code: api.ErrNotFound, Message: "no plugin called " + name})
		return
	}
	cfg, err := s.store.PluginConfigByName(r.Context(), name)
	if err != nil {
		s.fail(w, err)
		return
	}

	res, err := runner.Run(r.Context(), cfg, r.URL.Query().Get("relink") != "", s.Now())
	if err != nil {
		// The run recorded its own failure, so this only has to say it.
		s.fail(w, &api.Error{Code: api.ErrBadRequest, Message: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// PluginRunner is what the server needs from a plugin. One method, because
// the plugin reads its own configuration out of what it is handed and the
// server has no business knowing what a plan id is.
type PluginRunner interface {
	Name() string
	Run(ctx context.Context, cfg store.PluginConfig, relink bool, now time.Time) (sync.Result, error)
}

// AttachPlugins registers the built-in sync plugins.
func (s *Server) AttachPlugins(runners ...PluginRunner) {
	if s.plugins == nil {
		s.plugins = map[string]PluginRunner{}
	}
	for _, r := range runners {
		s.plugins[r.Name()] = r
	}
}

func (s *Server) plugin(name string) PluginRunner {
	if s.plugins == nil {
		return nil
	}
	return s.plugins[name]
}
