// Package server holds the HTTP surface: routing, JSON encoding, and the
// mapping from store errors onto status codes. It is server-only and imports
// internal/store, which is why it must never appear in the client's import
// graph.
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/auth"
	"github.com/harpchad/td/internal/blob"
	"github.com/harpchad/td/internal/mcpsrv"
	"github.com/harpchad/td/internal/oauth"
	"github.com/harpchad/td/internal/query"
	"github.com/harpchad/td/internal/store"
	"github.com/harpchad/td/internal/web"
)

// Server serves the REST API. The web UI, MCP, and auth arrive in later
// phases and hang off the same mux.
type Server struct {
	store *store.Store
	log   *slog.Logger

	// Now is the clock every handler reads. Injecting it is what lets the
	// tests evaluate against the fixed clock in testdata/seed.json.
	Now func() time.Time

	// TrustedProxies are the networks whose X-Forwarded-For header is
	// believed. Empty means none, so the login rate limit counts the immediate
	// peer. Behind a reverse proxy this must be set, or every request appears
	// to come from the proxy and the per-IP limit becomes one global limit.
	TrustedProxies []*net.IPNet

	// dummyHash is verified against when a username is unknown, so an
	// enumeration attempt cannot read the answer off the response time. It is
	// computed once because computing it is itself an argon2 call.
	dummyHash string

	// ui serves the browser interface. Nil leaves the server API-only, which
	// is what the API tests use.
	ui *web.UI

	// blobs holds attachment bytes. Nil turns the attachment routes into a
	// refusal rather than a panic, so a deployment with no writable data
	// directory still serves everything else.
	blobs *blob.Store

	// mcp serves the Model Context Protocol at /mcp. Nil leaves the route a
	// 404, which is what the API-only tests get.
	mcp *mcpsrv.Server

	// cimd resolves Client ID Metadata Documents, which is how a client with
	// no prior relationship to this server gets a client id under the
	// 2026-07-28 revision. It is the only outbound request td makes to a URL
	// somebody else chose, so its dialer refuses private addresses.
	cimd *oauth.Resolver

	// baseURL is the server's own public URL. Every OAuth and MCP discovery
	// document is built from it, and a wrong value fails in ways that look
	// like an application bug, which is why tdd refuses to start without one.
	baseURL string

	// plugins are the built-in sync plugins, keyed by name. They run on the
	// scheduler tick and are configured from the settings page, because a
	// mirror that needs a laptop and a CLI to stay current is not a mirror.
	plugins map[string]PluginRunner
}

// AttachBlobs mounts the attachment store. The bytes never go behind a static
// file handler: every download runs through the same authentication as the
// rest of /api/v1.
func (s *Server) AttachBlobs(b *blob.Store) { s.blobs = b }

// AttachWeb mounts the browser UI. The web routes go through the same store
// every other client's requests do.
func (s *Server) AttachWeb(assets *web.Assets, themeDir string) error {
	ui, err := web.New(s.store, assets, s.log, func() time.Time { return s.Now() })
	if err != nil {
		return err
	}
	ui.ThemeDir = themeDir
	s.ui = ui
	return nil
}

// New builds a Server over an open store.
func New(s *store.Store, log *slog.Logger) (*Server, error) {
	if log == nil {
		log = slog.Default()
	}
	dummy, err := auth.DummyHash(auth.DefaultParams)
	if err != nil {
		return nil, fmt.Errorf("preparing the login timing guard: %w", err)
	}
	loc := s.Location()
	return &Server{
		store:     s,
		log:       log,
		Now:       func() time.Time { return time.Now().In(loc) },
		dummyHash: dummy,
		cimd:      oauth.NewResolver(),
	}, nil
}

// Handler returns the routed handler with the standard response headers
// applied.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", s.health)

	// RFC 9728 Protected Resource Metadata, unauthenticated on purpose: a
	// client reads it precisely because it has no credential yet.
	mux.HandleFunc("GET "+PRMPath, s.protectedResource)

	// The protocol is stateless as of the 2026-07-28 revision, so POST is the
	// only method. GET and DELETE were the session transport, and there are
	// no sessions.
	mux.HandleFunc("POST "+MCPPath, s.mcpHandler)

	// The OAuth 2.1 authorization server. td is its own AS because claude.ai's
	// connector UI takes a client id and secret and has no field for a bearer
	// token, so a static token cannot be used there at all.
	//
	// The metadata, the JWKS, and the token and registration endpoints are
	// unauthenticated: a client reads them before it has anything. /authorize
	// is not, and sends a browser through the ordinary login first.
	mux.HandleFunc("GET "+ASMetadataPath, s.asMetadataDoc)
	mux.HandleFunc("GET "+JWKSPath, s.jwks)
	mux.HandleFunc("GET "+AuthorizePath, s.authorize)
	mux.HandleFunc("POST "+TokenPath, s.token)
	mux.HandleFunc("POST "+RegisterPath, s.register)
	mux.HandleFunc("POST "+RevokePath, s.revokeGrant)
	mux.HandleFunc("POST /w/approve", s.approve)

	// The only routes an unauthenticated request reaches. There is no
	// registration route and no password reset route: the one account is
	// created by a command on the server, and nothing over HTTP makes another.
	mux.HandleFunc("POST /login", s.login)
	mux.HandleFunc("POST /logout", s.logout)

	mux.HandleFunc("GET /api/v1/whoami", s.whoami)
	mux.HandleFunc("GET /api/v1/tokens", s.listTokens)
	mux.HandleFunc("POST /api/v1/tokens", s.createToken)
	mux.HandleFunc("DELETE /api/v1/tokens/{id}", s.revokeToken)

	mux.HandleFunc("GET /api/v1/tasks", s.listTasks)
	mux.HandleFunc("POST /api/v1/tasks", s.createTask)
	mux.HandleFunc("GET /api/v1/tasks/{id}", s.getTask)
	mux.HandleFunc("PATCH /api/v1/tasks/{id}", s.patchTask)
	mux.HandleFunc("DELETE /api/v1/tasks/{id}", s.dropTask)
	mux.HandleFunc("POST /api/v1/tasks/{id}/complete", s.completeTask)
	mux.HandleFunc("POST /api/v1/tasks/{id}/snooze", s.snoozeTask)

	mux.HandleFunc("GET /api/v1/tasks/{id}/attachments", s.listAttachments)
	mux.HandleFunc("POST /api/v1/tasks/{id}/attachments", s.addAttachment)
	mux.HandleFunc("GET /api/v1/tasks/{id}/attachments/{att}", s.getAttachment)
	mux.HandleFunc("DELETE /api/v1/tasks/{id}/attachments/{att}", s.deleteAttachment)

	mux.HandleFunc("POST /api/v1/series", s.createSeries)
	mux.HandleFunc("POST /api/v1/tasks/{id}/repeat", s.repeatTask)
	mux.HandleFunc("GET /api/v1/series/{id}", s.getSeries)
	mux.HandleFunc("PATCH /api/v1/series/{id}", s.updateSeries)

	mux.HandleFunc("GET /api/v1/people", s.listPeople)
	mux.HandleFunc("POST /api/v1/people", s.createPerson)
	mux.HandleFunc("GET /api/v1/people/{id}", s.getPerson)
	mux.HandleFunc("PATCH /api/v1/people/{id}", s.updatePerson)
	mux.HandleFunc("GET /api/v1/people/{id}/tasks", s.personTasks)
	mux.HandleFunc("POST /api/v1/people/{id}/identities", s.linkIdentity)
	mux.HandleFunc("GET /api/v1/groups", s.listGroups)
	mux.HandleFunc("POST /api/v1/groups", s.createGroup)
	mux.HandleFunc("PUT /api/v1/groups/{id}/members", s.setGroupMembers)
	mux.HandleFunc("POST /api/v1/tasks/{id}/people", s.linkPerson)
	mux.HandleFunc("DELETE /api/v1/tasks/{id}/people/{person}/{role}", s.unlinkPerson)
	mux.HandleFunc("GET /api/v1/filters", s.listFilters)
	mux.HandleFunc("POST /api/v1/filters", s.putFilter)
	mux.HandleFunc("GET /api/v1/ui/folds", s.listFolds)
	mux.HandleFunc("POST /api/v1/ui/folds/{id}", s.setFold)
	mux.HandleFunc("GET /api/v1/ui/filter", s.getViewFilter)
	mux.HandleFunc("PUT /api/v1/ui/filter", s.setViewFilter)
	mux.HandleFunc("GET /api/v1/events", s.listEvents)
	mux.HandleFunc("POST /api/v1/undo", s.undo)
	mux.HandleFunc("POST /api/v1/sync/{source}", s.syncSource)

	// The built-in plugins. /sync/{source} above stays exactly as it is: it
	// is the contract a third-party plugin posts to, and both paths land in
	// the same store.Sync so the ownership rules cannot differ.
	mux.HandleFunc("GET /api/v1/plugins/{name}", s.getPlugin)
	mux.HandleFunc("PUT /api/v1/plugins/{name}", s.savePlugin)
	mux.HandleFunc("POST /api/v1/plugins/{name}/connect", s.connectPlugin)
	mux.HandleFunc("POST /api/v1/plugins/{name}/poll", s.pollPlugin)
	mux.HandleFunc("POST /api/v1/plugins/{name}/disconnect", s.disconnectPlugin)
	mux.HandleFunc("POST /api/v1/plugins/{name}/run", s.runPlugin)

	// The browser side of connecting Planner. The device code flow is a
	// conversation with Microsoft, so the server drives it and hands the web
	// UI something to draw.
	mux.HandleFunc("POST /w/planner/connect", s.plannerConnect)
	mux.HandleFunc("POST /w/planner/poll", s.plannerPoll)
	// A refresh, a back button, or a bookmarked mid-flow URL lands on GET.
	// There is nothing to show without a fresh device code, so it goes back
	// to where the flow starts rather than answering 404.
	mux.HandleFunc("GET /w/planner/connect", s.plannerRestart)
	mux.HandleFunc("GET /w/planner/poll", s.plannerRestart)
	mux.HandleFunc("POST /w/planner/disconnect", s.plannerDisconnect)
	mux.HandleFunc("POST /w/planner/run", s.plannerRunNow)
	mux.HandleFunc("POST /w/planner/map", s.plannerMap)
	mux.HandleFunc("GET /api/v1/export", s.export)
	mux.HandleFunc("POST /api/v1/import", s.importAll)

	if s.ui != nil {
		s.ui.Routes(mux)
		// The login page is the only route that renders anything to an
		// unauthenticated request.
		mux.HandleFunc("GET /login", func(w http.ResponseWriter, r *http.Request) {
			s.ui.Login(w, r, r.URL.Query().Get("e"))
		})
	}

	// Outermost first: headers apply to every answer including a refusal, the
	// 503 gate runs before anything looks at a credential, and authentication
	// runs before any handler sees a request.
	return s.standardHeaders(s.requireAccount(s.authenticate(mux)))
}

// securityHeaders are sent on every response, including errors and the 401
// with the empty body. The server is on the public internet, so these are not
// conditional on anything.
func securityHeaders(h http.Header) {
	// Two years, which is the floor for preload lists. Harmless over plain
	// HTTP, since a browser ignores HSTS on an insecure origin.
	h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
	h.Set("X-Frame-Options", "DENY")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")

	h.Set("Content-Security-Policy", contentSecurityPolicy(""))
}

// contentSecurityPolicy builds the policy.
//
// No unsafe-inline and no unsafe-eval. htmx and the keymap are served as files
// rather than inlined, so 'self' covers both and the policy needs no per-page
// hash. htmx does evaluate hx- attributes, but it does not need eval to do it.
//
// formAction is the one part that is ever widened, and only on the consent
// screen. Everywhere else it is empty and the policy is 'self' alone.
func contentSecurityPolicy(formAction string) string {
	form := "form-action 'self'"
	if formAction != "" {
		form += " " + formAction
	}
	return strings.Join([]string{
		"default-src 'self'",
		"script-src 'self'",
		"style-src 'self'",
		"img-src 'self' data:",
		"connect-src 'self'",
		"font-src 'self'",
		"object-src 'none'",
		"base-uri 'none'",
		form,
		"frame-ancestors 'none'",
	}, "; ")
}

// allowConsentRedirect lets the consent form's approval reach the client.
//
// An OAuth consent screen ends in a cross-origin navigation by construction:
// you approve, and the browser carries an authorization code to the client's
// redirect URI. Under form-action 'self' Chrome kills that redirect and the
// approval silently goes nowhere, which is what happened the first time this
// was tried against a real connector. Firefox does not, which is worse: it
// works on one machine and not the next.
//
// Widened to the exact origin of the redirect URI and no further, and only
// after that URI has been matched against the client's registered list. It is
// the one origin the person is looking at a screen approving.
func allowConsentRedirect(h http.Header, redirectURI string) {
	u, err := url.Parse(redirectURI)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return
	}
	h.Set("Content-Security-Policy", contentSecurityPolicy(u.Scheme+"://"+u.Host))
}

// standardHeaders stamps every response with the server's API version and its
// current instant.
//
// The version is the answer to a container and a laptop updating on different
// schedules: the client compares it against its own and warns once when the
// major versions differ.
//
// The clock is the answer to a subtler one. Relative date labels ("Today",
// "Tomorrow") and the overdue bucket are computed against a calendar date in
// a timezone, and the server's configured zone is authoritative because that
// is what the sort order already used. A client rendering those labels from
// its own wall clock disagrees with the list it was handed the moment the two
// machines are in different zones, or the moment a development server pins
// its clock to a fixture.
func (s *Server) standardHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Td-Server", api.Version)
		w.Header().Set("X-Td-Now", s.Now().Format(time.RFC3339))
		securityHeaders(w.Header())
		next.ServeHTTP(w, r)
	})
}

// actorOf reads the calling principal's actor for the event log. Phase 1
// hardcoded "me"; a token now carries its own, so a bad agent batch stays
// separable from your own work and one /undo loop away from gone.
func (s *Server) actorOf(r *http.Request) string {
	if p := principalOf(r.Context()); p != nil {
		return p.Actor
	}
	return "me"
}

func (s *Server) listTokens(w http.ResponseWriter, r *http.Request) {
	tokens, err := s.store.Tokens(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tokens)
}

// createToken mints a token. The secret is in this response and nowhere else,
// ever again.
func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name   string   `json:"name"`
		Actor  string   `json:"actor"`
		Scopes []string `json:"scopes"`
	}
	if err := decode(r, &in); err != nil {
		s.fail(w, err)
		return
	}
	if in.Actor == "" {
		in.Actor = "me"
	}
	tok, err := s.store.CreateToken(r.Context(), in.Name, in.Actor, in.Scopes, s.Now())
	if err != nil {
		s.fail(w, &api.Error{Code: api.ErrBadRequest, Message: err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, tok)
}

func (s *Server) revokeToken(w http.ResponseWriter, r *http.Request) {
	if err := s.store.RevokeToken(r.Context(), r.PathValue("id"), s.Now()); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// health answers unauthenticated with no detail in the body.
func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

func (s *Server) listTasks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	tasks, err := s.store.List(r.Context(), q, s.Now())
	if err != nil {
		s.fail(w, err)
		return
	}

	// The task list does not paginate. A filtered list is meant to be read
	// whole, and an order computed in Go cannot produce a stable cursor
	// without encoding sort position into it. limit truncates the top of the
	// order for callers that want a top N, such as the MCP whats_next tool;
	// total still reports the untruncated count, so a caller can always tell
	// it got a slice rather than the answer.
	total := len(tasks)
	if l := r.URL.Query().Get("limit"); l != "" {
		n, err := strconv.Atoi(l)
		if err != nil || n <= 0 {
			s.fail(w, &api.Error{Code: api.ErrBadRequest, Message: "limit must be a positive integer"})
			return
		}
		if n < len(tasks) {
			tasks = tasks[:n]
		}
	}

	writeJSON(w, http.StatusOK, api.TaskList{Tasks: tasks, Total: total})
}

func (s *Server) createTask(w http.ResponseWriter, r *http.Request) {
	var in api.TaskCreate
	if err := decode(r, &in); err != nil {
		s.fail(w, err)
		return
	}
	task, err := s.store.Create(r.Context(), s.actorOf(r), in, s.Now())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, task)
}

func (s *Server) getTask(w http.ResponseWriter, r *http.Request) {
	id, ok := s.resolve(w, r)
	if !ok {
		return
	}
	task, err := s.store.Get(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (s *Server) patchTask(w http.ResponseWriter, r *http.Request) {
	id, ok := s.resolve(w, r)
	if !ok {
		return
	}
	var patch api.TaskPatch
	if err := decode(r, &patch); err != nil {
		s.fail(w, err)
		return
	}
	task, err := s.store.Patch(r.Context(), s.actorOf(r), id, patch,
		strings.Trim(r.Header.Get("If-Match"), `"`), s.Now())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, task)
}

// dropTask answers DELETE. It sets status to dropped: there is no hard delete
// anywhere in td.
func (s *Server) dropTask(w http.ResponseWriter, r *http.Request) {
	id, ok := s.resolve(w, r)
	if !ok {
		return
	}
	task, err := s.store.Drop(r.Context(), s.actorOf(r), id, s.Now())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (s *Server) completeTask(w http.ResponseWriter, r *http.Request) {
	id, ok := s.resolve(w, r)
	if !ok {
		return
	}
	res, err := s.store.Complete(r.Context(), s.actorOf(r), id, s.Now())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// snoozeTask hides a task until an instant. It takes a relative duration as
// well as an absolute time because the ntfy action button is composed when
// the reminder is sent and clicked some time later: "1h" has to mean an hour
// from the tap, not an hour from the push.
func (s *Server) snoozeTask(w http.ResponseWriter, r *http.Request) {
	id, ok := s.resolve(w, r)
	if !ok {
		return
	}
	var req api.SnoozeRequest
	if err := decode(r, &req); err != nil {
		s.fail(w, err)
		return
	}

	now := s.Now()
	var until time.Time
	switch {
	case req.Until != "":
		at, err := time.Parse(time.RFC3339, req.Until)
		if err != nil {
			s.fail(w, &api.Error{Code: api.ErrBadRequest, Message: "until must be an RFC3339 instant"})
			return
		}
		until = at
	case req.Duration != "":
		d, err := time.ParseDuration(req.Duration)
		if err != nil || d <= 0 {
			s.fail(w, &api.Error{Code: api.ErrBadRequest, Message: `duration must be positive, like "1h" or "30m"`})
			return
		}
		until = now.Add(d)
	default:
		s.fail(w, &api.Error{Code: api.ErrBadRequest, Message: "say how long, with duration or until"})
		return
	}

	task, err := s.store.Snooze(r.Context(), s.actorOf(r), id, until, now)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (s *Server) listPeople(w http.ResponseWriter, r *http.Request) {
	people, err := s.store.People(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, people)
}

func (s *Server) createPerson(w http.ResponseWriter, r *http.Request) {
	var in api.Person
	if err := decode(r, &in); err != nil {
		s.fail(w, err)
		return
	}
	person, err := s.store.CreatePerson(r.Context(), in, s.Now())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, person)
}

func (s *Server) getPerson(w http.ResponseWriter, r *http.Request) {
	person, err := s.store.ResolvePerson(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, person)
}

func (s *Server) updatePerson(w http.ResponseWriter, r *http.Request) {
	person, err := s.store.ResolvePerson(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	var in api.Person
	if err := decode(r, &in); err != nil {
		s.fail(w, err)
		return
	}
	updated, err := s.store.UpdatePerson(r.Context(), person.ID, in)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// personTasks is the person page: a first-class screen rather than a filter
// preset, in the order section 5 fixes.
func (s *Server) personTasks(w http.ResponseWriter, r *http.Request) {
	person, err := s.store.ResolvePerson(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	page, err := s.store.PersonPage(r.Context(), person.ID, s.Now())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

// linkIdentity maps an external account onto a person, which is what lets a
// query span Jira, monday, and Planner instead of finding three Brandisses.
func (s *Server) linkIdentity(w http.ResponseWriter, r *http.Request) {
	person, err := s.store.ResolvePerson(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	var in api.Identity
	if err := decode(r, &in); err != nil {
		s.fail(w, err)
		return
	}
	if err := s.store.LinkIdentity(r.Context(), person.ID, in.Source, in.ExternalID); err != nil {
		s.fail(w, err)
		return
	}
	identities, err := s.store.Identities(r.Context(), person.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, identities)
}

func (s *Server) listGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := s.store.Groups(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, groups)
}

func (s *Server) createGroup(w http.ResponseWriter, r *http.Request) {
	var in api.Group
	if err := decode(r, &in); err != nil {
		s.fail(w, err)
		return
	}
	group, err := s.store.CreateGroup(r.Context(), in)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, group)
}

func (s *Server) setGroupMembers(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Members []string `json:"members"`
	}
	if err := decode(r, &in); err != nil {
		s.fail(w, err)
		return
	}
	if err := s.store.SetGroupMembers(r.Context(), r.PathValue("id"), in.Members); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) linkPerson(w http.ResponseWriter, r *http.Request) {
	id, ok := s.resolve(w, r)
	if !ok {
		return
	}
	var in struct {
		Person string `json:"person"`
		Role   string `json:"role"`
	}
	if err := decode(r, &in); err != nil {
		s.fail(w, err)
		return
	}
	person, err := s.store.ResolvePerson(r.Context(), in.Person)
	if err != nil {
		s.fail(w, err)
		return
	}
	if err := s.store.LinkPerson(r.Context(), s.actorOf(r), id, person.ID, in.Role, s.Now()); err != nil {
		s.fail(w, err)
		return
	}
	task, err := s.store.Get(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (s *Server) unlinkPerson(w http.ResponseWriter, r *http.Request) {
	id, ok := s.resolve(w, r)
	if !ok {
		return
	}
	person, err := s.store.ResolvePerson(r.Context(), r.PathValue("person"))
	if err != nil {
		s.fail(w, err)
		return
	}
	if err := s.store.UnlinkPerson(r.Context(), s.actorOf(r), id, person.ID, r.PathValue("role"), s.Now()); err != nil {
		s.fail(w, err)
		return
	}
	task, err := s.store.Get(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (s *Server) listFilters(w http.ResponseWriter, r *http.Request) {
	filters, err := s.store.SavedFilters(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, filters)
}

func (s *Server) putFilter(w http.ResponseWriter, r *http.Request) {
	var f api.SavedFilter
	if err := decode(r, &f); err != nil {
		s.fail(w, err)
		return
	}
	if f.Slot < 1 || f.Slot > 9 {
		s.fail(w, &api.Error{Code: api.ErrBadRequest, Message: "slot must be 1 through 9"})
		return
	}
	if _, err := s.store.List(r.Context(), f.Query, s.Now()); err != nil {
		s.fail(w, err)
		return
	}
	saved, err := s.store.PutSavedFilter(r.Context(), f)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, saved)
}

// listFolds returns which parents are folded. It is per-task view state that
// follows the user between clients, which is the whole reason it lives on the
// server rather than in a dotfile.
func (s *Server) listFolds(w http.ResponseWriter, r *http.Request) {
	ids, err := s.store.CollapsedTasks(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, api.Folds{Collapsed: ids})
}

func (s *Server) setFold(w http.ResponseWriter, r *http.Request) {
	id, ok := s.resolve(w, r)
	if !ok {
		return
	}
	var req api.FoldRequest
	if err := decode(r, &req); err != nil {
		s.fail(w, err)
		return
	}
	if err := s.store.SetCollapsed(r.Context(), id, req.Collapsed); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// getViewFilter returns the list a client should open on.
//
// Alongside the folds above for the same reason: it is view state, it follows
// you between clients, and a dotfile could not do either. It is also what
// makes a bare "/" in the web UI land on the list you were reading rather than
// on the default, which is why a back link needs no query string.
func (s *Server) getViewFilter(w http.ResponseWriter, r *http.Request) {
	filter, chosen, err := s.store.CurrentFilter(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, api.ViewFilter{Filter: filter, Chosen: chosen})
}

// setViewFilter records what is being read.
//
// PUT rather than POST: there is one of these and writing it twice with the
// same body changes nothing. It parses the filter first, because a stored
// query that does not parse is a home page nobody can open.
func (s *Server) setViewFilter(w http.ResponseWriter, r *http.Request) {
	var req api.ViewFilter
	if err := decode(r, &req); err != nil {
		s.fail(w, err)
		return
	}
	if strings.TrimSpace(req.Filter) != "" {
		if _, err := query.Parse(req.Filter); err != nil {
			s.fail(w, &api.Error{Code: api.ErrBadRequest, Message: err.Error()})
			return
		}
	}
	if err := s.store.SetCurrentFilter(r.Context(), req.Filter); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listEvents(w http.ResponseWriter, r *http.Request) {
	since := int64(0)
	if v := r.URL.Query().Get("since"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			s.fail(w, &api.Error{Code: api.ErrBadRequest, Message: "since must be a non-negative integer"})
			return
		}
		since = n
	}
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			s.fail(w, &api.Error{Code: api.ErrBadRequest, Message: "limit must be a positive integer"})
			return
		}
		limit = n
	}
	events, err := s.store.Events(r.Context(), since, limit)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, events)
}

func (s *Server) undo(w http.ResponseWriter, r *http.Request) {
	res, err := s.store.Undo(r.Context(), s.actorOf(r), s.Now())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// resolve turns the {id} path value into a task id, accepting either a ULID
// or the short number you type in `td done 412`.
func (s *Server) resolve(w http.ResponseWriter, r *http.Request) (string, bool) {
	id, err := s.store.Resolve(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return "", false
	}
	return id, true
}

func decode(r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return &api.Error{Code: api.ErrBadRequest, Message: err.Error()}
	}
	return nil
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Default().Error("write response", "err", err)
	}
}

// fail maps an error onto a status code and the error body. Every failure
// says what went wrong in a way a client can branch on and a human can read.
func (s *Server) fail(w http.ResponseWriter, err error) {
	var apiErr *api.Error
	if errors.As(err, &apiErr) {
		writeJSON(w, statusFor(apiErr.Code), apiErr)
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, &api.Error{
			Code: api.ErrNotFound, Message: "no task with that id or number",
		})
		return
	}
	// A filter that does not parse is the user's typo, not a server fault,
	// and the parser's message already names the problem.
	var parseErr *query.ParseError
	if errors.As(err, &parseErr) {
		writeJSON(w, http.StatusBadRequest, &api.Error{
			Code: api.ErrBadRequest, Message: parseErr.Msg,
		})
		return
	}
	s.log.Error("request failed", "err", err)
	writeJSON(w, http.StatusInternalServerError, &api.Error{
		Code: "internal", Message: "the server could not complete that",
	})
}

func statusFor(code string) int {
	switch code {
	case api.ErrNotFound, api.ErrNothingToUndo:
		return http.StatusNotFound
	case api.ErrBadRequest:
		return http.StatusBadRequest
	case api.ErrIllegalTransition, api.ErrConflict:
		return http.StatusConflict
	case api.ErrInboxIncomplete, api.ErrWaitingNeedsPerson, api.ErrNestingTooDeep:
		return http.StatusUnprocessableEntity
	default:
		return http.StatusInternalServerError
	}
}
