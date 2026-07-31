package client_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/client"
)

func TestListSendsTheClientVersion(t *testing.T) {
	var seen string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("X-Td-Client")
		w.Header().Set("X-Td-Server", api.Version)
		_ = json.NewEncoder(w).Encode(api.TaskList{Tasks: []api.Task{}})
	}))
	defer ts.Close()

	c := client.New(client.Config{Server: ts.URL})
	if _, err := c.List(context.Background(), "is:open", 0); err != nil {
		t.Fatal(err)
	}
	if seen != api.Version {
		t.Errorf("X-Td-Client = %q, want %q", seen, api.Version)
	}
}

// TestMajorVersionSkewWarnsOnce covers the answer to the container and the
// laptop updating on different schedules.
func TestMajorVersionSkewWarnsOnce(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Td-Server", "9.0.0")
		_ = json.NewEncoder(w).Encode(api.TaskList{Tasks: []api.Task{}})
	}))
	defer ts.Close()

	var warnings []string
	c := client.New(client.Config{Server: ts.URL})
	c.Warn = func(msg string) { warnings = append(warnings, msg) }

	for range 3 {
		if _, err := c.List(context.Background(), "", 0); err != nil {
			t.Fatal(err)
		}
	}
	if len(warnings) != 1 {
		t.Fatalf("got %d warnings, want exactly one: %v", len(warnings), warnings)
	}
}

// TestClientTakesItsClockFromTheServer covers the fix for a client and a
// server disagreeing about what "today" is. The server's configured zone is
// authoritative, because it is what the sort order already used.
func TestClientTakesItsClockFromTheServer(t *testing.T) {
	const pinned = "2026-08-03T10:30:00-05:00"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Td-Server", api.Version)
		w.Header().Set("X-Td-Now", pinned)
		_ = json.NewEncoder(w).Encode(api.TaskList{Tasks: []api.Task{}})
	}))
	defer ts.Close()

	c := client.New(client.Config{Server: ts.URL})

	// Before any response there is nothing to go on, so the local clock
	// stands in rather than a zero time.
	if c.Now().IsZero() {
		t.Error("Now() before the first request should fall back to local time")
	}

	if _, err := c.List(context.Background(), "", 0); err != nil {
		t.Fatal(err)
	}
	if got := c.Now().Format(time.RFC3339); got != pinned {
		t.Errorf("Now() = %s, want the server's %s", got, pinned)
	}
}

// TestAMissingOrJunkClockHeaderIsIgnored keeps a client usable against a
// server that does not send the header, or sends something unparseable.
func TestAMissingOrJunkClockHeaderIsIgnored(t *testing.T) {
	for _, header := range []string{"", "yesterday afternoon"} {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-Td-Server", api.Version)
			if header != "" {
				w.Header().Set("X-Td-Now", header)
			}
			_ = json.NewEncoder(w).Encode(api.TaskList{Tasks: []api.Task{}})
		}))

		c := client.New(client.Config{Server: ts.URL})
		if _, err := c.List(context.Background(), "", 0); err != nil {
			t.Fatal(err)
		}
		if c.Now().IsZero() {
			t.Errorf("header %q left the clock unusable", header)
		}
		ts.Close()
	}
}

func TestMatchingMajorVersionIsSilent(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Td-Server", "1.7.3")
		_ = json.NewEncoder(w).Encode(api.TaskList{Tasks: []api.Task{}})
	}))
	defer ts.Close()

	var warnings []string
	c := client.New(client.Config{Server: ts.URL})
	c.Warn = func(msg string) { warnings = append(warnings, msg) }

	if _, err := c.List(context.Background(), "", 0); err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Errorf("a minor version difference must not warn: %v", warnings)
	}
}

// TestServerErrorsDecodeIntoAPIError checks that a client can branch on the
// code rather than on the message text.
func TestServerErrorsDecodeIntoAPIError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Td-Server", api.Version)
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(api.Error{
			Code: api.ErrIllegalTransition, From: "done", To: "doing",
			Message: "reopen to todo first",
		})
	}))
	defer ts.Close()

	c := client.New(client.Config{Server: ts.URL})
	_, err := c.Complete(context.Background(), "111")
	var apiErr *api.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("got %T, want *api.Error", err)
	}
	if apiErr.Code != api.ErrIllegalTransition || apiErr.From != "done" {
		t.Errorf("error = %+v", apiErr)
	}
}

// TestUnreachableServerIsOfflineNotAFailure covers the state that lets `td a`
// queue instead of losing a capture.
func TestUnreachableServerIsOfflineNotAFailure(t *testing.T) {
	c := client.New(client.Config{Server: "http://127.0.0.1:1"})
	_, err := c.List(context.Background(), "", 0)
	if !errors.Is(err, client.ErrOffline) {
		t.Errorf("got %v, want an offline error", err)
	}
}

// TestQueueRoundTrip covers the offline path end to end: a capture queues
// while the server is down and flushes once it is back.
func TestQueueRoundTrip(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	queued := []api.TaskCreate{
		{ID: "01J0000000000000000000000A", Title: "first"},
		{ID: "01J0000000000000000000000B", Title: "second"},
	}
	for _, in := range queued {
		if err := client.Enqueue(in); err != nil {
			t.Fatal(err)
		}
	}
	depth, err := client.QueueDepth()
	if err != nil {
		t.Fatal(err)
	}
	if depth != 2 {
		t.Fatalf("depth = %d, want 2", depth)
	}

	var received []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in api.TaskCreate
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Error(err)
		}
		received = append(received, in.ID)
		w.Header().Set("X-Td-Server", api.Version)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(api.Task{ID: in.ID, Title: in.Title})
	}))
	defer ts.Close()

	c := client.New(client.Config{Server: ts.URL})
	sent, err := c.Flush(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if sent != 2 {
		t.Errorf("sent = %d, want 2", sent)
	}
	// The queued ids reach the server unchanged, which is what makes a
	// double flush harmless.
	if len(received) != 2 || received[0] != queued[0].ID || received[1] != queued[1].ID {
		t.Errorf("received = %v, want the queued ids in order", received)
	}

	depth, err = client.QueueDepth()
	if err != nil {
		t.Fatal(err)
	}
	if depth != 0 {
		t.Errorf("depth = %d after a clean flush, want 0", depth)
	}
}

// TestFlushKeepsTheUnsentRemainder covers a flush that dies part way: what did
// not go must still be on disk.
func TestFlushKeepsTheUnsentRemainder(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	for _, in := range []api.TaskCreate{{ID: "a", Title: "first"}, {ID: "b", Title: "second"}} {
		if err := client.Enqueue(in); err != nil {
			t.Fatal(err)
		}
	}

	calls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Td-Server", api.Version)
		calls++
		if calls > 1 {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(api.Error{Code: "internal"})
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(api.Task{})
	}))
	defer ts.Close()

	c := client.New(client.Config{Server: ts.URL})
	sent, err := c.Flush(context.Background())
	if err == nil {
		t.Fatal("expected the second create to fail")
	}
	if sent != 1 {
		t.Errorf("sent = %d, want 1", sent)
	}
	depth, err := client.QueueDepth()
	if err != nil {
		t.Fatal(err)
	}
	if depth != 1 {
		t.Errorf("depth = %d, want the unsent remainder still queued", depth)
	}
}

// TestLoadConfigWritesACommentedDefault covers first run.
func TestLoadConfigWritesACommentedDefault(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	cfg, err := client.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server == "" {
		t.Error("a default config must name a server")
	}

	// A second load reads what the first wrote rather than rewriting it.
	again, err := client.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if again.Server != cfg.Server {
		t.Errorf("server drifted between loads: %q then %q", cfg.Server, again.Server)
	}
}

func TestEnvironmentOverridesTheFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("TD_SERVER", "https://td.example.com")
	t.Setenv("TD_TOKEN", "td_abc123")

	cfg, err := client.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server != "https://td.example.com" || cfg.Token != "td_abc123" {
		t.Errorf("cfg = %+v, want the environment applied", cfg)
	}
}
