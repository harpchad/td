package client

import (
	"context"
	"net/http"
	"net/url"

	"github.com/harpchad/td/internal/api"
)

// Series is a recurrence rule and the template its instances are made from.
// It mirrors the server's shape without importing internal/store, which the
// client's import graph must never reach.
type Series struct {
	ID       string         `json:"id,omitempty"`
	RRule    string         `json:"rrule"`
	Mode     string         `json:"mode,omitempty"`
	Catchup  string         `json:"catchup,omitempty"`
	Anchor   string         `json:"anchor,omitempty"`
	TZ       string         `json:"tz,omitempty"`
	Template api.TaskCreate `json:"template"`

	NextAt        *string `json:"next_at,omitempty"`
	EndsAt        *string `json:"ends_at,omitempty"`
	LastFiredAt   *string `json:"last_fired_at,omitempty"`
	CurrentTaskID *string `json:"current_task_id,omitempty"`
}

// SeriesResult is a series plus, on creation, the instance it materialized.
type SeriesResult struct {
	Series Series    `json:"series"`
	Task   *api.Task `json:"task,omitempty"`
}

// CreateSeries stores a rule and returns it with its first instance.
func (c *Client) CreateSeries(ctx context.Context, in Series) (SeriesResult, error) {
	var out SeriesResult
	err := c.do(ctx, http.MethodPost, "/api/v1/series", in, nil, &out)
	return out, err
}

// Series fetches one series.
func (c *Client) Series(ctx context.Context, id string) (SeriesResult, error) {
	var out SeriesResult
	err := c.do(ctx, http.MethodGet, "/api/v1/series/"+url.PathEscape(id), nil, nil, &out)
	return out, err
}

// UpdateSeries is the explicit series edit. Editing an instance edits that
// instance; this changes the rule and the template, and leaves whatever is
// already in the list alone.
func (c *Client) UpdateSeries(ctx context.Context, id string, in Series) (SeriesResult, error) {
	var out SeriesResult
	err := c.do(ctx, http.MethodPatch, "/api/v1/series/"+url.PathEscape(id), in, nil, &out)
	return out, err
}
