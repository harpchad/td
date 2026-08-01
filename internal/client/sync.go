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

// MapIdentity attaches an external account to a person, so a sync stops
// having to guess who they are.
func (c *Client) MapIdentity(ctx context.Context, personRef, source, externalID string) error {
	person, err := c.Person(ctx, personRef)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost,
		"/api/v1/people/"+url.PathEscape(person.ID)+"/identities",
		map[string]string{"source": source, "external_id": externalID}, nil, nil)
}

// RunSync asks the server to run a plugin now.
//
// The plugin and its credentials live on the server, so this carries nothing
// but the instruction. It is the button for somebody at a terminal; the
// schedule is what keeps the mirror current.
func (c *Client) RunSync(ctx context.Context, source string, relink bool) (sync.Result, error) {
	path := "/api/v1/plugins/" + url.PathEscape(source) + "/run"
	if relink {
		path += "?relink=1"
	}
	var out sync.Result
	err := c.do(ctx, http.MethodPost, path, nil, nil, &out)
	return out, err
}
