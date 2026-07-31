package client

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/harpchad/td/internal/api"
)

// ExportDoc is the export as the client reads it.
//
// The task list is typed because the markdown writer walks it; everything
// else stays raw so the client does not have to know the server's schema in
// order to write it back out unchanged. A client that parsed every field
// would silently drop whatever a newer server added, which is the one thing
// a backup must not do.
type ExportDoc struct {
	Version    int        `json:"version"`
	ExportedAt string     `json:"exported_at"`
	Timezone   string     `json:"timezone"`
	Tasks      []api.Task `json:"tasks"`

	People      json.RawMessage `json:"people,omitempty"`
	Groups      json.RawMessage `json:"groups,omitempty"`
	Identities  json.RawMessage `json:"identities,omitempty"`
	Filters     json.RawMessage `json:"filters,omitempty"`
	Series      json.RawMessage `json:"series,omitempty"`
	Attachments json.RawMessage `json:"attachments,omitempty"`
	Events      json.RawMessage `json:"events,omitempty"`
}

// Export downloads the whole database.
func (c *Client) Export(ctx context.Context) (ExportDoc, error) {
	var out ExportDoc
	err := c.do(ctx, http.MethodGet, "/api/v1/export", nil, nil, &out)
	return out, err
}

// Import restores an export. The document is passed through as raw JSON
// rather than round-tripped through a Go type, so nothing the client does not
// understand is lost on the way in.
func (c *Client) Import(ctx context.Context, doc json.RawMessage) (map[string]int, error) {
	var out map[string]int
	err := c.do(ctx, http.MethodPost, "/api/v1/import", doc, nil, &out)
	return out, err
}
