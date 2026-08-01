// Package memos posts finished work to a Memos instance so the journal fills
// itself.
//
// One direction only. Section 17 fixes this as an outbound webhook on
// task.completed with no read path from Memos into tasks: a journal that can
// create work is a second inbox, and there is already one inbox.
//
// It is not a sync plugin and does not use the plugin contract. There is no
// external_id, no cursor exchange, and no reconciliation. A memo is a note
// about something that happened, and if one fails to post the task is still
// done.
package memos

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/query"
)

// Consumer is the outbox cursor this webhook follows. It is a constant
// because the name is a key in the database, and a typo would silently
// restart delivery from the newest event.
const Consumer = "memos"

// Config is the [memos] block of config.toml.
type Config struct {
	// URL is the Memos instance, without a path. Empty turns the webhook off,
	// which is the default: nothing posts anywhere until it is configured.
	URL string `toml:"url"`
	// Token is a Memos access token. Required when URL is set.
	Token string `toml:"token"`
	// Visibility is PRIVATE, PROTECTED, or PUBLIC. Private by default,
	// because a task manager's contents are not a blog.
	Visibility string `toml:"visibility"`
	// Tag is prepended to every memo so the journal entries are filterable
	// inside Memos. Empty omits it.
	Tag string `toml:"tag"`
}

// DefaultConfig is what a first start writes.
var DefaultConfig = Config{Visibility: "PRIVATE", Tag: "td"}

// Enabled reports whether anything will be posted.
func (c Config) Enabled() bool { return strings.TrimSpace(c.URL) != "" }

// Validate refuses a configuration that would fail at the first completion
// rather than at startup.
func (c Config) Validate() error {
	if !c.Enabled() {
		return nil
	}
	if strings.TrimSpace(c.Token) == "" {
		return fmt.Errorf("memos.url is set but memos.token is empty")
	}
	switch strings.ToUpper(c.Visibility) {
	case "", "PRIVATE", "PROTECTED", "PUBLIC":
		return nil
	default:
		return fmt.Errorf("memos.visibility is PRIVATE, PROTECTED, or PUBLIC, got %q", c.Visibility)
	}
}

// Memo is what gets posted.
type Memo struct {
	Content    string `json:"content"`
	Visibility string `json:"visibility,omitempty"`
}

// Poster is what the deliverer needs. It is an interface so no test can
// reach a real Memos instance, the same reason the ntfy sender is one.
type Poster interface {
	Post(ctx context.Context, m Memo) error
}

// HTTPPoster posts over the Memos v1 API.
type HTTPPoster struct {
	Config Config
	Client *http.Client
}

// NewHTTPPoster builds a poster with a short timeout. A journal entry is not
// worth holding the scheduler tick open for.
func NewHTTPPoster(cfg Config) *HTTPPoster {
	return &HTTPPoster{Config: cfg, Client: &http.Client{Timeout: 10 * time.Second}}
}

// Post creates one memo.
func (p *HTTPPoster) Post(ctx context.Context, m Memo) error {
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}
	endpoint := strings.TrimRight(p.Config.URL, "/") + "/api/v1/memos"

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.Config.Token)

	resp, err := p.Client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 300 {
		// The body is read for the message and then discarded. A Memos error
		// says what was wrong with the memo, which is worth having in the log
		// exactly once.
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("memos answered %s: %s", resp.Status, strings.TrimSpace(string(detail)))
	}
	return nil
}

// Compose turns a finished task into a memo.
//
// Markdown, because Memos renders it, and deliberately plain: the memo says
// what was finished and when, and links back rather than restating the task.
// A journal entry that duplicates the task is two things to keep in sync.
// The completion time comes off the task rather than from the clock at
// posting time. If Memos was down for a day the memo arrives a day late, and
// a journal entry dated by when it was delivered would be wrong about the one
// fact it exists to record.
func Compose(cfg Config, t api.Task, baseURL string, loc *time.Location) Memo {
	var b strings.Builder

	if tag := strings.TrimSpace(cfg.Tag); tag != "" {
		b.WriteString("#" + strings.TrimPrefix(tag, "#") + " ")
	}
	b.WriteString("Done: " + t.Title + "\n")

	// The details worth keeping in a journal are the ones that will not be
	// obvious in six months: who it involved, what it was about, and when.
	facts := make([]string, 0, 4+len(t.People))
	if t.Priority != nil {
		facts = append(facts, fmt.Sprintf("p%d", *t.Priority))
	}
	if len(t.Tags) > 0 {
		facts = append(facts, "#"+strings.Join(t.Tags, " #"))
	}
	for _, p := range t.People {
		facts = append(facts, "@"+firstWordLower(p.Name))
	}
	if t.DueAt != nil && *t.DueAt != "" {
		facts = append(facts, "due "+query.LocalDate(*t.DueAt, loc))
	}
	if t.CompletedAt != nil && *t.CompletedAt != "" {
		facts = append(facts, "finished "+query.LocalDate(*t.CompletedAt, loc))
	}
	if len(facts) > 0 {
		b.WriteString("\n" + strings.Join(facts, "  ") + "\n")
	}

	if notes := strings.TrimSpace(t.Notes); notes != "" {
		b.WriteString("\n" + notes + "\n")
	}

	// A mirrored task links to its source as well, since that is where the
	// conversation about it lives.
	if t.ExternalURL != nil && *t.ExternalURL != "" {
		b.WriteString("\n" + *t.ExternalURL + "\n")
	}
	if baseURL != "" {
		fmt.Fprintf(&b, "\n%s/t/%d\n", strings.TrimRight(baseURL, "/"), t.Num)
	}

	visibility := strings.ToUpper(strings.TrimSpace(cfg.Visibility))
	if visibility == "" {
		visibility = "PRIVATE"
	}
	return Memo{Content: b.String(), Visibility: visibility}
}

// firstWordLower is the handle you would type after @.
func firstWordLower(name string) string {
	fields := strings.Fields(name)
	if len(fields) == 0 {
		return ""
	}
	return strings.ToLower(fields[0])
}
