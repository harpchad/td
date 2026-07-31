package client

import (
	"context"
	"net/http"
	"net/url"

	"github.com/harpchad/td/internal/sync"
)

// Sync posts one plugin batch.
func (c *Client) Sync(ctx context.Context, source string, req sync.Request) (sync.Result, error) {
	var out sync.Result
	err := c.do(ctx, http.MethodPost,
		"/api/v1/sync/"+url.PathEscape(source), req, nil, &out)
	return out, err
}

// Mirrored returns every external id td already holds for a source.
//
// It is how a plugin works out what disappeared upstream when the source has
// no tombstones and no delta query, which is the case for Planner. The filter
// is deliberately bare: a mirrored task that is done, dropped, or already
// marked gone still has to be in the set, or a task you completed locally
// would be reported gone on every run.
func (c *Client) Mirrored(ctx context.Context, source string) ([]string, error) {
	list, err := c.List(ctx, "src:"+source, 0)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(list.Tasks))
	for _, task := range list.Tasks {
		if task.ExternalID != nil && *task.ExternalID != "" {
			out = append(out, *task.ExternalID)
		}
	}
	return out, nil
}
