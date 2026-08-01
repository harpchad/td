package notify

import (
	"context"
	"log/slog"
	"time"

	"github.com/harpchad/td/internal/store"
	"github.com/harpchad/td/internal/sync"
)

// Plugins runs the sync mirrors on the scheduler tick.
//
// Each plugin carries its own interval, so a sixty second tick does not mean
// hammering somebody's Graph tenant sixty times an hour. The store decides
// which are due; this only runs them.
type Plugins struct {
	Store   PluginStore
	Runners map[string]Runner
}

// Runner is one plugin. The same interface the server exposes, restated here
// so this package does not import internal/server.
type Runner interface {
	Name() string
	Run(ctx context.Context, cfg store.PluginConfig, relink bool, now time.Time) (sync.Result, error)
}

// PluginStore is what the tick needs from the database.
type PluginStore interface {
	DuePlugins(ctx context.Context, now time.Time) ([]store.PluginConfig, error)
}

// Once runs whatever is due.
//
// A plugin that fails is logged and the tick carries on. One misconfigured
// mirror must not stop reminders from going out, and the failure is recorded
// against the plugin itself so the settings page can show it without anybody
// reading a log.
func (p *Plugins) Once(ctx context.Context, now time.Time, log *slog.Logger) {
	if p == nil || p.Store == nil || len(p.Runners) == 0 {
		return
	}
	due, err := p.Store.DuePlugins(ctx, now)
	if err != nil {
		log.Error("looking for due plugins", "err", err)
		return
	}

	for _, cfg := range due {
		runner, ok := p.Runners[cfg.Name]
		if !ok {
			continue
		}
		res, err := runner.Run(ctx, cfg, false, now)
		if err != nil {
			log.Error("sync plugin", "plugin", cfg.Name, "err", err)
			continue
		}
		if res.Created+res.Updated+res.Gone > 0 {
			log.Info("sync plugin ran", "plugin", cfg.Name,
				"created", res.Created, "updated", res.Updated,
				"gone", res.Gone, "unchanged", res.Unchanged)
		}
	}
}
