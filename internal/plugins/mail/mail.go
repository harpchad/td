// Package mail captures flagged Outlook messages as inbox tasks.
//
// This is a capture plugin, not a mirror, and the difference is the whole
// design. The Planner plugin holds a mirror in step: upstream owns the title
// and the status, a task that vanishes upstream is marked gone, and every run
// re-posts everything it can see. None of that is right here.
//
// Flagging a mail is a person saying "this is a thing I have to do". Once td
// has made a task from it, the task is theirs: they will retitle it, give it a
// priority, complete it. So a message is posted exactly once and never again.
// Unflagging the mail does not remove the task, and completing the task does
// not survive only until the next run.
//
// Posting once is what buys all of that, and it is enforced by subtracting the
// ids td already holds before anything is sent, never sending a gone list, and
// the UNIQUE (source, external_id) on task underneath as a backstop.
package mail

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/recur"
	"github.com/harpchad/td/internal/sync"
)

// Source is the value stored on every task this plugin creates, and what
// src:mail matches in the filter grammar.
const Source = "mail"

// Actor is what the event log records for a capture, so a batch that arrived
// from a mailbox is separable from anything typed by a person.
const Actor = "plugin:mail"

// BatchSize bounds one POST to the sync endpoint.
const BatchSize = 200

// DefaultEndpoint is Graph v1.0. Overridable for a sovereign cloud, and for
// the tests, which point it at a file server.
const DefaultEndpoint = "https://graph.microsoft.com/v1.0"

// Scopes are what this plugin needs and nothing more.
//
// Mail.Read, not Mail.ReadWrite: section 8 says td never writes back to a
// source system, so it cannot clear the flag and must not hold the permission
// to. Read-only also means the worst case for a leaked mail credential is
// disclosure rather than somebody's mailbox being edited.
var Scopes = []string{
	"https://graph.microsoft.com/Mail.Read",
	"offline_access",
	"openid",
	"profile",
}

// GraphMessage is the part of Graph's message resource this plugin reads.
type GraphMessage struct {
	ID               string        `json:"id"`
	Subject          string        `json:"subject"`
	BodyPreview      string        `json:"bodyPreview"`
	WebLink          string        `json:"webLink"`
	ReceivedDateTime string        `json:"receivedDateTime"`
	From             GraphFrom     `json:"from"`
	Flag             GraphFlag     `json:"flag"`
	ParentFolderID   string        `json:"parentFolderId"`
	Categories       []string      `json:"categories"`
	Sender           GraphFrom     `json:"sender"`
	Recipients       []GraphFrom   `json:"toRecipients"`
	Internet         []GraphHeader `json:"internetMessageHeaders"`
}

// GraphFrom wraps the address, which Graph nests one level deeper than you
// would expect.
type GraphFrom struct {
	EmailAddress GraphAddress `json:"emailAddress"`
}

// GraphAddress is a display name and an address, either of which may be empty.
type GraphAddress struct {
	Name    string `json:"name"`
	Address string `json:"address"`
}

// GraphHeader is an internet message header, read for nothing today and kept
// because dropping it would mean changing the type to add it back.
type GraphHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// GraphFlag is followupFlag. flagStatus is the one field that decides whether
// a message is a task at all.
type GraphFlag struct {
	FlagStatus    string         `json:"flagStatus"`
	StartDateTime *GraphDateTime `json:"startDateTime"`
	DueDateTime   *GraphDateTime `json:"dueDateTime"`
	CompletedDate *GraphDateTime `json:"completedDateTime"`
}

// GraphDateTime is Graph's dateTimeTimeZone: an instant and the zone it should
// be read in, as two strings rather than one timestamp.
type GraphDateTime struct {
	DateTime string `json:"dateTime"`
	TimeZone string `json:"timeZone"`
}

// GraphMessageList is one page of messages.
type GraphMessageList struct {
	Value    []GraphMessage `json:"value"`
	NextLink string         `json:"@odata.nextLink"`
}

// Flagged reports whether a message is one somebody marked for follow up.
//
// Checked here rather than trusted to the $filter in the query. The filter is
// sent, but a server that ignores it, a proxy that rewrites it, or a future
// change to the query string would otherwise turn every message in the mailbox
// into a task, which is the kind of mistake that is very hard to undo by hand.
func (m GraphMessage) Flagged() bool {
	return strings.EqualFold(m.Flag.FlagStatus, "flagged")
}

// Config is what the settings page stores.
type Config struct {
	// Folders limits capture to specific mail folder ids. Empty means the
	// whole mailbox, which is the useful default: a flag is already an
	// explicit act, so filtering it again by location mostly surprises people
	// when a flag in the wrong folder does nothing.
	Folders []string `json:"folders,omitempty"`

	// Endpoint overrides the Graph base URL.
	Endpoint string `json:"endpoint,omitempty"`

	// GraphToken is the bearer for this run. It is never stored in settings:
	// the credential lives in its own column and is put here by the runner.
	GraphToken string `json:"-"`
}

// Client reads mail over Graph.
type Client struct {
	Config Config
	HTTP   *http.Client
}

// New builds a client.
func New(cfg Config) *Client {
	if cfg.Endpoint == "" {
		cfg.Endpoint = DefaultEndpoint
	}
	return &Client{Config: cfg, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// Flagged reads every flagged message, following pagination.
//
// The $filter is sent so the mailbox does the work rather than this process
// downloading everything, and $select keeps the response to the fields that
// are read. Neither is trusted: Flagged() checks each message again.
//
// Messages come back newest first. The sort happens here rather than in the
// query because Graph rejects $orderby on a property the $filter does not
// mention (InefficientFilter), and receivedDateTime is not in the filter.
func (c *Client) Flagged(ctx context.Context) ([]GraphMessage, error) {
	var out []GraphMessage
	if len(c.Config.Folders) == 0 {
		page, err := c.page(ctx, c.listURL(""))
		if err != nil {
			return nil, err
		}
		out = page
	} else {
		for _, folder := range c.Config.Folders {
			page, err := c.page(ctx, c.listURL(folder))
			if err != nil {
				return nil, fmt.Errorf("reading folder %s: %w", folder, err)
			}
			out = append(out, page...)
		}
	}

	// RFC 3339 UTC timestamps, so byte order is time order.
	slices.SortStableFunc(out, func(a, b GraphMessage) int {
		return strings.Compare(b.ReceivedDateTime, a.ReceivedDateTime)
	})
	return out, nil
}

// listURL builds the first request. A folder scopes it; empty is the mailbox.
func (c *Client) listURL(folder string) string {
	base := c.Config.Endpoint + "/me/messages"
	if folder != "" {
		base = c.Config.Endpoint + "/me/mailFolders/" + url.PathEscape(folder) + "/messages"
	}
	// No $orderby: Graph rejects a $filter on one property combined with a
	// $orderby on another as InefficientFilter, so Flagged() sorts instead.
	q := url.Values{
		"$filter": {"flag/flagStatus eq 'flagged'"},
		"$select": {"id,subject,bodyPreview,webLink,receivedDateTime,from,flag,parentFolderId"},
		"$top":    {"50"},
	}
	return base + "?" + q.Encode()
}

// page follows nextLink to the end.
func (c *Client) page(ctx context.Context, next string) ([]GraphMessage, error) {
	var out []GraphMessage
	// Bounded, because a nextLink loop from a confused proxy would otherwise
	// spin forever against somebody's mailbox.
	for page := 0; next != "" && page < 100; page++ {
		var body GraphMessageList
		if err := c.get(ctx, next, &body); err != nil {
			return nil, err
		}
		out = append(out, body.Value...)
		next = body.NextLink
	}
	return out, nil
}

func (c *Client) get(ctx context.Context, endpoint string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if c.Config.GraphToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.Config.GraphToken)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("graph answered %s for %s: %s",
			resp.Status, endpoint, strings.TrimSpace(string(detail)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Translate turns flagged messages into items for the sync endpoint.
//
// What is deliberately not translated:
//
//   - The body. A task list is not a mail client, and pasting a thread into
//     notes makes every list unreadable. The web link goes on the task and
//     one click is in the actual mail, with its attachments and its thread.
//   - The recipients. Who else was on the mail is not who owes you something,
//     and adding six people to a task teaches you to ignore the person field.
//   - Categories and folders. They are the mailbox's organization, not yours,
//     and tags in td are a thing you chose.
func Translate(messages []GraphMessage, loc *time.Location) []sync.Item {
	out := make([]sync.Item, 0, len(messages))
	for _, m := range messages {
		if !m.Flagged() {
			continue
		}
		item := sync.Item{
			ExternalID: m.ID,
			Title:      Title(m),
			// Captured, so it lands where everything captured lands and gets
			// sorted by a person. Nothing arrives already prioritized.
			Status: api.StatusInbox,
			URL:    m.WebLink,
			// The received time, not a hash: a message is immutable once it
			// has arrived, and this plugin never re-posts one anyway.
			Rev: m.ReceivedDateTime,
		}
		if due := FlagDue(m.Flag, loc); due != "" {
			item.DueAt = &due
		}
		if p, ok := sender(m); ok {
			item.People = []sync.ItemPerson{p}
		}
		out = append(out, item)
	}
	return out
}

// Title is what the task is called.
//
// An empty subject is legal in mail and common in replies from phones, so it
// falls back to the preview and then to the sender. A task called "" is a row
// you cannot act on and cannot search for.
func Title(m GraphMessage) string {
	if subject := strings.TrimSpace(m.Subject); subject != "" {
		return subject
	}
	if preview := strings.TrimSpace(firstLine(m.BodyPreview)); preview != "" {
		return truncate(preview, 120)
	}
	if from := strings.TrimSpace(m.From.EmailAddress.Name); from != "" {
		return "Message from " + from
	}
	return "Flagged message with no subject"
}

// FlagDue reads the due date somebody set on the flag, as a calendar date.
//
// The date is taken exactly as written and never converted between zones.
// That looks lazy and is the entire point: a flag due date is a day somebody
// picked out of a calendar, not an instant. Outlook stores the day it was
// given at around midnight in some reference zone, so Graph hands back
// "2026-08-07T04:00:00" for the 7th, and reading that as an instant and
// formatting it in the server's zone turns it into the 6th for everyone west
// of London. Measured before this was written that way: Chicago and Los
// Angeles both lost a day, UTC and Tokyo did not, which is the shape of a bug
// nobody would ever report as a timezone problem.
//
// Section 5's rule for date-only values says the same thing in the other
// direction: a date-only template keeps its instances date-only, because "pay
// the mortgage on the 1st" is a date and not an instant.
func FlagDue(flag GraphFlag, _ *time.Location) string {
	if flag.DueDateTime == nil {
		return ""
	}
	raw := strings.TrimSpace(flag.DueDateTime.DateTime)
	if raw == "" {
		return ""
	}

	// The leading YYYY-MM-DD, whatever follows it. Parsed rather than sliced,
	// so a string that is not a date at all produces nothing instead of ten
	// arbitrary characters.
	date, _, _ := strings.Cut(raw, "T")
	at, err := time.Parse(recur.DateLayout, date)
	if err != nil {
		return ""
	}
	return at.Format(recur.DateLayout)
}

// sender is the one person link a captured mail carries.
//
// Involved rather than assignee: they sent you something, which is not the
// same as owing you the work. The address is the evidence the server uses to
// recognize somebody it already knows, which is what makes "everything
// involving Stacey" span the mailbox and the board.
func sender(m GraphMessage) (sync.ItemPerson, bool) {
	addr := m.From.EmailAddress
	if addr.Address == "" {
		addr = m.Sender.EmailAddress
	}
	if strings.TrimSpace(addr.Address) == "" {
		return sync.ItemPerson{}, false
	}
	return sync.ItemPerson{
		Role: api.RoleInvolved,
		// The address is the identifier here. Graph gives no directory object
		// id on a message, and it is stable enough for the purpose: an address
		// that changes is a person td asks about once more.
		SourceUser: strings.ToLower(addr.Address),
		Name:       strings.TrimSpace(addr.Name),
		Email:      strings.ToLower(addr.Address),
	}, true
}

func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return s[:i]
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.TrimSpace(s[:n]) + "…"
}
