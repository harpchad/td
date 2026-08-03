package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/harpchad/td/internal/api"
)

// ErrOffline is returned when the server could not be reached at all. It is a
// state rather than a failure: reads fall back to cache and `td a` queues.
var ErrOffline = errors.New("server unreachable")

// Client talks to tdd over HTTP. It never opens the database file, which is
// what lets the TUI run anywhere rather than only on the box holding it.
type Client struct {
	base  string
	token string
	http  *http.Client

	warnOnce sync.Once
	// Warn receives the one version-skew line. Tests replace it; the CLI
	// points it at stderr.
	Warn func(string)

	mu        sync.Mutex
	serverNow time.Time
}

// Now reports the server's clock as of the last response, in the server's
// configured timezone. Relative date labels and the overdue bucket are
// computed against it rather than against the local wall clock, so a client
// in another zone renders the same list it was handed.
//
// Falls back to local time before the first response.
func (c *Client) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.serverNow.IsZero() {
		return time.Now()
	}
	return c.serverNow
}

func (c *Client) noteServerClock(header string) {
	if header == "" {
		return
	}
	at, err := time.Parse(time.RFC3339, header)
	if err != nil {
		return
	}
	c.mu.Lock()
	c.serverNow = at
	c.mu.Unlock()
}

// New builds a Client from a resolved config.
func New(cfg Config) *Client {
	return &Client{
		base:  strings.TrimSuffix(cfg.Server, "/"),
		token: cfg.Token,
		http:  &http.Client{Timeout: 10 * time.Second},
		Warn: func(msg string) {
			fmt.Fprintln(os.Stderr, msg)
		},
	}
}

// SyncClock learns the server's clock before anything is resolved against it.
//
// Date keywords have to resolve in the server's timezone, for the same reason
// relative labels render against it: the server is what computed the order
// and what will store the result. Without this, "due:friday" typed on a
// laptop an hour ahead of the server lands on a different day than the same
// word typed in the web box.
//
// The health check is the cheapest way to ask. It needs no credential, has no
// side effects, and carries X-Td-Now like every other response.
func (c *Client) SyncClock(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "/healthz", nil, nil, nil)
}

// List runs a filter and returns the matching tasks in the default order.
func (c *Client) List(ctx context.Context, filter string, limit int) (api.TaskList, error) {
	q := url.Values{}
	if filter != "" {
		q.Set("q", filter)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out api.TaskList
	err := c.do(ctx, http.MethodGet, "/api/v1/tasks?"+q.Encode(), nil, nil, &out)
	return out, err
}

// Create adds a task.
func (c *Client) Create(ctx context.Context, in api.TaskCreate) (api.Task, error) {
	var out api.Task
	err := c.do(ctx, http.MethodPost, "/api/v1/tasks", in, nil, &out)
	return out, err
}

// Get fetches one task by ULID or by its short number.
func (c *Client) Get(ctx context.Context, ref string) (api.Task, error) {
	var out api.Task
	err := c.do(ctx, http.MethodGet, "/api/v1/tasks/"+url.PathEscape(ref), nil, nil, &out)
	return out, err
}

// Patch applies a partial update. When ifMatch is set the server refuses the
// write if the task changed underneath you.
func (c *Client) Patch(ctx context.Context, ref string, patch any, ifMatch string) (api.Task, error) {
	var headers map[string]string
	if ifMatch != "" {
		headers = map[string]string{"If-Match": ifMatch}
	}
	var out api.Task
	err := c.do(ctx, http.MethodPatch, "/api/v1/tasks/"+url.PathEscape(ref), patch, headers, &out)
	return out, err
}

// Complete marks a task done and reports how many children are still open.
func (c *Client) Complete(ctx context.Context, ref string) (api.CompleteResult, error) {
	var out api.CompleteResult
	err := c.do(ctx, http.MethodPost, "/api/v1/tasks/"+url.PathEscape(ref)+"/complete", nil, nil, &out)
	return out, err
}

// Drop moves a task to dropped. Nothing in td hard-deletes.
func (c *Client) Drop(ctx context.Context, ref string) (api.Task, error) {
	var out api.Task
	err := c.do(ctx, http.MethodDelete, "/api/v1/tasks/"+url.PathEscape(ref), nil, nil, &out)
	return out, err
}

// Person looks someone up by id or handle.
func (c *Client) Person(ctx context.Context, ref string) (api.Person, error) {
	var out api.Person
	err := c.do(ctx, http.MethodGet, "/api/v1/people/"+url.PathEscape(ref), nil, nil, &out)
	return out, err
}

// PersonPage is the screen you open before a 1:1.
func (c *Client) PersonPage(ctx context.Context, ref string) (api.PersonPage, error) {
	var out api.PersonPage
	err := c.do(ctx, http.MethodGet, "/api/v1/people/"+url.PathEscape(ref)+"/tasks", nil, nil, &out)
	return out, err
}

// LinkPerson attaches a person to a task in a role.
func (c *Client) LinkPerson(ctx context.Context, ref, person, role string) (api.Task, error) {
	var out api.Task
	body := map[string]string{"person": person, "role": role}
	err := c.do(ctx, http.MethodPost, "/api/v1/tasks/"+url.PathEscape(ref)+"/people", body, nil, &out)
	return out, err
}

// PatchTyped is Patch with the typed body, for callers that have one.
func (c *Client) PatchTyped(ctx context.Context, ref string, patch api.TaskPatch, ifMatch string) (api.Task, error) {
	body := map[string]any{}
	if patch.Title != nil {
		body["title"] = *patch.Title
	}
	if patch.Notes != nil {
		body["notes"] = *patch.Notes
	}
	if patch.Status != nil {
		body["status"] = *patch.Status
	}
	if patch.Presence["waiting_on"] {
		body["waiting_on"] = patch.WaitingOn
	}
	if patch.Presence["notify"] && patch.Notify != nil {
		body["notify"] = *patch.Notify
	}
	// The pointer-plus-presence pair is what keeps an absent key and an
	// explicit null different: absent leaves the field alone, null clears it.
	// A nil pointer with presence set is how a field is cleared.
	if patch.Presence["priority"] {
		body["priority"] = patch.Priority
	}
	if patch.Presence["effort"] {
		body["effort"] = patch.Effort
	}
	if patch.Presence["due_at"] {
		body["due_at"] = patch.DueAt
	}
	if patch.Presence["start_at"] {
		body["start_at"] = patch.StartAt
	}
	if patch.Presence["snooze_until"] {
		body["snooze_until"] = patch.SnoozeUntil
	}
	if patch.Tags != nil {
		body["tags"] = *patch.Tags
	}
	return c.Patch(ctx, ref, body, ifMatch)
}

// Snooze hides a task until an instant. duration is relative ("1h"), until is
// absolute; give one.
func (c *Client) Snooze(ctx context.Context, ref string, req api.SnoozeRequest) (api.Task, error) {
	var out api.Task
	err := c.do(ctx, http.MethodPost, "/api/v1/tasks/"+url.PathEscape(ref)+"/snooze", req, nil, &out)
	return out, err
}

// Undo reverses your newest reversible change.
func (c *Client) Undo(ctx context.Context) (api.UndoResult, error) {
	var out api.UndoResult
	err := c.do(ctx, http.MethodPost, "/api/v1/undo", nil, nil, &out)
	return out, err
}

// People lists everyone tasks can refer to.
func (c *Client) People(ctx context.Context) ([]api.Person, error) {
	var out []api.Person
	err := c.do(ctx, http.MethodGet, "/api/v1/people", nil, nil, &out)
	return out, err
}

// Filters lists the saved filters bound to the number keys.
func (c *Client) Filters(ctx context.Context) ([]api.SavedFilter, error) {
	var out []api.SavedFilter
	err := c.do(ctx, http.MethodGet, "/api/v1/filters", nil, nil, &out)
	return out, err
}

// WhoAmI reports which credential this client is using and what it may do.
func (c *Client) WhoAmI(ctx context.Context) (api.SessionInfo, error) {
	var out api.SessionInfo
	err := c.do(ctx, http.MethodGet, "/api/v1/whoami", nil, nil, &out)
	return out, err
}

// Folds returns the ids of every folded parent. Fold state is stored
// server-side so it follows you between the TUI and the web UI.
func (c *Client) Folds(ctx context.Context) (api.Folds, error) {
	var out api.Folds
	err := c.do(ctx, http.MethodGet, "/api/v1/ui/folds", nil, nil, &out)
	return out, err
}

// SetFold folds or unfolds one parent.
func (c *Client) SetFold(ctx context.Context, ref string, collapsed bool) error {
	return c.do(ctx, http.MethodPost, "/api/v1/ui/folds/"+url.PathEscape(ref),
		api.FoldRequest{Collapsed: collapsed}, nil, nil)
}

// RepeatTask makes an existing task the first instance of a new series.
//
// Not CreateSeries: that one materializes a fresh task from the template,
// which from a task you are looking at leaves a duplicate beside it.
func (c *Client) RepeatTask(ctx context.Context, ref string, in Series) (SeriesResult, error) {
	var out SeriesResult
	err := c.do(ctx, http.MethodPost,
		"/api/v1/tasks/"+url.PathEscape(ref)+"/repeat", in, nil, &out)
	return out, err
}

// ViewFilter returns the list this account was last reading, and whether
// anybody has ever chosen one. Server-side for the same reason folds are: it
// follows you between the TUI and the web UI.
func (c *Client) ViewFilter(ctx context.Context) (api.ViewFilter, error) {
	var out api.ViewFilter
	err := c.do(ctx, http.MethodGet, "/api/v1/ui/filter", nil, nil, &out)
	return out, err
}

// SetViewFilter remembers what is being read.
func (c *Client) SetViewFilter(ctx context.Context, filter string) error {
	return c.do(ctx, http.MethodPut, "/api/v1/ui/filter",
		api.ViewFilter{Filter: filter, Chosen: true}, nil, nil)
}

// Events reads the change feed from seq onwards.
func (c *Client) Events(ctx context.Context, since int64) ([]api.Event, error) {
	var out []api.Event
	err := c.do(ctx, http.MethodGet,
		"/api/v1/events?since="+strconv.FormatInt(since, 10), nil, nil, &out)
	return out, err
}

func (c *Client) do(ctx context.Context, method, path string, body any, headers map[string]string, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("X-Td-Client", api.Version)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrOffline, err)
	}
	defer func() { _ = resp.Body.Close() }()

	c.checkVersion(resp.Header.Get("X-Td-Server"))
	c.noteServerClock(resp.Header.Get("X-Td-Now"))

	if resp.StatusCode >= 400 {
		return decodeError(resp)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func decodeError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var apiErr api.Error
	if err := json.Unmarshal(body, &apiErr); err == nil && apiErr.Code != "" {
		return &apiErr
	}

	// A 401 from the API carries no body by design, so there is nothing to
	// decode and the status alone would tell the user nothing actionable.
	if resp.StatusCode == http.StatusUnauthorized {
		return &api.Error{
			Code:    api.ErrUnauthorized,
			Message: "the server did not accept this token. Check `token` in config.toml, or mint one with `tdd token create`",
		}
	}
	return fmt.Errorf("server answered %s", resp.Status)
}

// checkVersion prints one warning line when the client and the server differ
// in major version. The container and the laptop update on different
// schedules, so this is stated rather than discovered.
func (c *Client) checkVersion(serverVersion string) {
	if serverVersion == "" || c.Warn == nil {
		return
	}
	if majorOf(serverVersion) == majorOf(api.Version) {
		return
	}
	c.warnOnce.Do(func() {
		c.Warn(fmt.Sprintf("td: client speaks API %s, server speaks %s. Update the one that is behind.",
			api.Version, serverVersion))
	})
}

func majorOf(version string) string {
	major, _, _ := strings.Cut(version, ".")
	return major
}
