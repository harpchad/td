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
	"strconv"
	"strings"
	"time"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/auth"
	"github.com/harpchad/td/internal/query"
	"github.com/harpchad/td/internal/store"
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
	}, nil
}

// Handler returns the routed handler with the standard response headers
// applied.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", s.health)

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

	mux.HandleFunc("GET /api/v1/people", s.listPeople)
	mux.HandleFunc("GET /api/v1/filters", s.listFilters)
	mux.HandleFunc("POST /api/v1/filters", s.putFilter)
	mux.HandleFunc("GET /api/v1/events", s.listEvents)
	mux.HandleFunc("POST /api/v1/undo", s.undo)

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

	// No unsafe-inline anywhere. Phase 4 adds the htmx bundle's hash to
	// script-src; until there is a script to serve, 'self' is the whole of it.
	h.Set("Content-Security-Policy", strings.Join([]string{
		"default-src 'self'",
		"script-src 'self'",
		"style-src 'self'",
		"img-src 'self' data:",
		"connect-src 'self'",
		"font-src 'self'",
		"object-src 'none'",
		"base-uri 'none'",
		"form-action 'self'",
		"frame-ancestors 'none'",
	}, "; "))
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

func (s *Server) listPeople(w http.ResponseWriter, r *http.Request) {
	people, err := s.store.People(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, people)
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
