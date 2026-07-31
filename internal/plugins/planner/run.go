package planner

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/harpchad/td/internal/sync"
)

// Poster is what a run needs from the td API. An interface so the tests never
// need a running server, and so nothing in this package holds a store.
type Poster interface {
	// Sync posts one batch to POST /api/v1/sync/planner.
	Sync(ctx context.Context, source string, req sync.Request) (sync.Result, error)
	// Mirrored returns the external ids td already holds for this source,
	// which is how the plugin works out what disappeared.
	Mirrored(ctx context.Context, source string) ([]string, error)
}

// Run mirrors every configured plan and returns the totals.
//
// The order matters. Everything present is posted first, in batches, and the
// gone list goes last in a batch of its own. Doing it the other way round
// would mark a task gone and then immediately un-mark it, writing two events
// for no change.
func Run(ctx context.Context, c *Client, api Poster, now time.Time, loc *time.Location) (sync.Result, error) {
	var total sync.Result
	if !c.Config.Enabled() {
		return total, errors.New("no plans configured: set planner.plans in config.toml")
	}

	var everything []GraphTask
	for _, planID := range c.Config.PlanIDs {
		tasks, err := c.Tasks(ctx, planID)
		if err != nil {
			return total, fmt.Errorf("reading plan %s: %w", planID, err)
		}
		everything = append(everything, tasks...)
	}

	users, err := c.Users(ctx, UserIDs(everything))
	if err != nil {
		return total, err
	}
	items := Translate(everything, users, c.Config.TaskURLTemplate, loc)

	cursor := now.UTC().Format(time.RFC3339)
	for start := 0; start < len(items); start += BatchSize {
		end := min(start+BatchSize, len(items))
		res, err := api.Sync(ctx, Source, sync.Request{
			Cursor: cursor, Items: items[start:end],
		})
		if err != nil {
			return total, err
		}
		total.Created += res.Created
		total.Updated += res.Updated
		total.Unchanged += res.Unchanged
	}

	// The gone pass is skipped when the read produced nothing.
	//
	// Planner has no tombstones, so "gone" is computed by subtraction from a
	// read this plugin has to assume was complete. A read that returned zero
	// tasks is far more likely to be an expired token or a plan id typo than
	// a plan somebody genuinely emptied, and acting on it would mark every
	// mirrored task upstream_gone in one pass. Refusing to subtract from
	// nothing is the cheapest guard against the most destructive mistake this
	// plugin can make.
	if len(everything) == 0 {
		total.Cursor = cursor
		return total, nil
	}

	mirrored, err := api.Mirrored(ctx, Source)
	if err != nil {
		return total, err
	}
	if gone := Gone(mirrored, everything); len(gone) > 0 {
		res, err := api.Sync(ctx, Source, sync.Request{Cursor: cursor, Gone: gone})
		if err != nil {
			return total, err
		}
		total.Gone += res.Gone
	}

	total.Cursor = cursor
	return total, nil
}
