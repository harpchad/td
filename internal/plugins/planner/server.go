package planner

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

// The server-side half. The plugin still speaks the section 8 contract
// exactly; it just reaches the store directly instead of going out to
// localhost and back in through its own API. A third-party plugin still posts
// to POST /api/v1/sync/planner with a scoped token, and both paths land in
// the same store.Sync, so the field-ownership rules cannot differ between
// them.

// Actor is what a server-side run writes to the event log, so a bad import is
// separable from your own work and one undo loop away.
const Actor = "plugin:planner"

// Settings is the non-secret half of the configuration, the part the web UI
// edits and the part it is safe to show.
type Settings struct {
	// PlanIDs are the plans to mirror. Empty mirrors nothing, which is the
	// default: guessing which plans somebody meant would import a board they
	// never asked for.
	PlanIDs []string `json:"plans"`
	// TaskURLTemplate builds the deep link a mirrored task carries. Planner's
	// web address has moved once already.
	TaskURLTemplate string `json:"task_url_template,omitempty"`
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
		return out, fmt.Errorf("planner settings: %w", err)
	}
	return out, nil
}

// storePoster satisfies the plugin contract against the store.
//
// It is the same Poster interface the HTTP client implements, so Run cannot
// tell the difference and there is one code path rather than two that drift.
type storePoster struct {
	store *store.Store
	now   time.Time
}

func (p storePoster) Sync(ctx context.Context, source string, req sync.Request) (sync.Result, error) {
	return p.store.Sync(ctx, Actor, source, req, p.now)
}

func (p storePoster) Mirrored(ctx context.Context, source string) ([]string, error) {
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

// Runner mirrors Planner from inside the server.
type Runner struct {
	Store    *store.Store
	Identity *msgraph.Client
	Loc      *time.Location
}

// Name is the plugin's key in plugin_config.
func (r *Runner) Name() string { return Source }

// Run does one sync and records what happened.
//
// Every exit stores a result, including the failures. A settings page that
// can only say "it worked" is a settings page that says nothing when it
// matters, and the whole reason this moved onto the server is that nobody is
// watching a terminal.
func (r *Runner) Run(ctx context.Context, cfg store.PluginConfig, relink bool, now time.Time) (sync.Result, error) {
	res, err := r.run(ctx, cfg, relink, now)

	// The unresolved list is stored even on a failure, because a run that got
	// far enough to find people and then fell over still learned something
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

func (r *Runner) run(ctx context.Context, cfg store.PluginConfig, relink bool, now time.Time) (sync.Result, error) {
	settings, err := ParseSettings(cfg.Settings)
	if err != nil {
		return sync.Result{}, err
	}
	if len(settings.PlanIDs) == 0 {
		return sync.Result{}, fmt.Errorf("no plans configured")
	}

	var cred msgraph.Credential
	if !cfg.Connected() {
		return sync.Result{}, fmt.Errorf("not connected to Microsoft: connect Planner in Settings")
	}
	if err := json.Unmarshal(cfg.Credential, &cred); err != nil {
		return sync.Result{}, fmt.Errorf("stored credential: %w", err)
	}

	// The refresh normally hands back a new refresh token. Storing what comes
	// back is what keeps a mirror working past the first rotation, so it is
	// saved before the sync rather than after: a sync that fails must not
	// cost the token it just renewed.
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
		PlanIDs:         settings.PlanIDs,
		GraphToken:      token,
		TaskURLTemplate: settings.TaskURLTemplate,
		Endpoint:        settings.Endpoint,
		Relink:          relink,
	})
	return Run(ctx, graph, storePoster{store: r.Store, now: now}, now, r.Loc)
}

// summarize is the one-line status the settings page shows.
func summarize(res sync.Result) string {
	parts := []string{
		fmt.Sprintf("%d created", res.Created),
		fmt.Sprintf("%d updated", res.Updated),
		fmt.Sprintf("%d unchanged", res.Unchanged),
		fmt.Sprintf("%d gone", res.Gone),
	}
	if n := len(res.Unresolved); n > 0 {
		parts = append(parts, fmt.Sprintf("%d unmatched %s", n, plural(n, "person", "people")))
	}
	return strings.Join(parts, ", ")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
