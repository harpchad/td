package mail

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/harpchad/td/internal/msgraph"
	"github.com/harpchad/td/internal/store"
	"github.com/harpchad/td/internal/sync"
)

// The server-side half. The plugin speaks the same section 8 contract a
// third-party one would; it just reaches the store directly instead of going
// out to localhost and back in through its own API. Both paths land in the
// same store.Sync, so the field-ownership rules cannot differ between them.

// Settings is the non-secret half of the configuration, the part the web UI
// edits and the part it is safe to show.
type Settings struct {
	// Folders limits capture to specific mail folder ids. Empty is the whole
	// mailbox, which is the default: a flag is already an explicit act, and
	// filtering it again by location mostly produces flags that quietly do
	// nothing.
	Folders []string `json:"folders,omitempty"`
	// Endpoint overrides the Graph base URL for a sovereign cloud, and is what
	// the tests point at a local server.
	Endpoint string `json:"endpoint,omitempty"`
}

// ParseSettings reads what the web UI stored.
func ParseSettings(raw json.RawMessage) (Settings, error) {
	out := Settings{}
	if len(raw) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("mail settings: %w", err)
	}
	return out, nil
}

// storePoster satisfies the plugin contract against the store.
type storePoster struct {
	store *store.Store
	now   time.Time
}

func (p storePoster) Sync(ctx context.Context, source string, req sync.Request) (sync.Result, error) {
	return p.store.Sync(ctx, Actor, source, req, p.now)
}

// Captured returns every message id td already holds for this source.
//
// The filter is the source alone, with no status term, and that matters more
// here than anywhere else. A completed task must still count as captured, or
// finishing something would make the next run capture the same mail again and
// the task would come back from the dead every fifteen minutes.
func (p storePoster) Captured(ctx context.Context, source string) ([]string, error) {
	tasks, err := p.store.List(ctx, "src:"+source, p.now)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(tasks))
	for _, task := range tasks {
		if task.ExternalID != nil && *task.ExternalID != "" {
			out = append(out, *task.ExternalID)
		}
	}
	return out, nil
}

// Runner is the server-side plugin.
type Runner struct {
	Store    *store.Store
	Identity *msgraph.Client
	Loc      *time.Location
}

// Name is the plugin's key in the configuration table and in the API path.
func (r *Runner) Name() string { return Source }

// Run captures whatever is flagged and records what happened.
//
// relink is accepted to satisfy the interface and ignored. It means "re-apply
// person links to tasks already mirrored", which only makes sense for a mirror
// that re-posts its window. This plugin posts a message once, so there is
// nothing to re-apply: a sender who becomes a known person is picked up on the
// next capture, and older tasks keep the links they were created with.
func (r *Runner) Run(ctx context.Context, cfg store.PluginConfig, _ bool, now time.Time) (sync.Result, error) {
	res, err := r.run(ctx, cfg, now)

	// The unresolved list is stored even on a failure, because a run that got
	// far enough to find senders and then fell over still learned something
	// worth acting on.
	var pending json.RawMessage
	if len(res.Unresolved) > 0 {
		if body, marshalErr := json.Marshal(res.Unresolved); marshalErr == nil {
			pending = body
		}
	}
	if recErr := r.Store.RecordPluginRun(ctx, Source, summarize(res), pending, err, now); recErr != nil && err == nil {
		err = recErr
	}
	return res, err
}

func (r *Runner) run(ctx context.Context, cfg store.PluginConfig, now time.Time) (sync.Result, error) {
	settings, err := ParseSettings(cfg.Settings)
	if err != nil {
		return sync.Result{}, err
	}

	if !cfg.Connected() {
		return sync.Result{}, fmt.Errorf("not connected to Microsoft: connect Mail in Settings")
	}
	var cred msgraph.Credential
	if err := json.Unmarshal(cfg.Credential, &cred); err != nil {
		return sync.Result{}, fmt.Errorf("stored credential: %w", err)
	}

	// The refresh normally hands back a new refresh token. Storing what comes
	// back is what keeps this working past the first rotation, so it is saved
	// before the capture rather than after: a run that fails must not cost the
	// token it just renewed.
	token, refreshed, err := r.Identity.AccessToken(ctx, cred, now)
	if err != nil {
		return sync.Result{}, err
	}
	if refreshed.RefreshToken != cred.RefreshToken || refreshed.AccessToken != cred.AccessToken {
		body, err := json.Marshal(refreshed)
		if err != nil {
			return sync.Result{}, err
		}
		if err := r.Store.SavePluginCredential(ctx, Source, body, now); err != nil {
			return sync.Result{}, err
		}
	}

	graph := New(Config{
		Folders:    settings.Folders,
		Endpoint:   settings.Endpoint,
		GraphToken: token,
	})
	return Run(ctx, graph, storePoster{store: r.Store, now: now}, now, r.Loc)
}

// summarize is what the settings page shows for the last run.
//
// Created and unresolved only. A mirror reports updated, unchanged and gone
// because those are the interesting outcomes when upstream owns the fields;
// here every run either captures something new or does nothing, and printing
// "0 updated, 0 gone" every fifteen minutes would be four numbers that are
// always zero.
func summarize(res sync.Result) string {
	parts := []string{fmt.Sprintf("%d captured", res.Created)}
	if n := len(res.Unresolved); n > 0 {
		parts = append(parts, fmt.Sprintf("%d unmatched %s", n, plural(n, "sender", "senders")))
	}
	return strings.Join(parts, ", ")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
