package notify

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/harpchad/td/internal/api"
)

// Notification is one push.
type Notification struct {
	Title string
	Body  string
	Tags  []string
	// Click is where tapping the notification goes: the task in the web UI.
	Click string
	// Actions are the buttons. Empty when no action token is configured,
	// which leaves the notification a click-through.
	Actions []Action
}

// Action is a button on the notification. ntfy calls the URL directly, so the
// credential travels in a header rather than the URL.
type Action struct {
	Label  string
	URL    string
	Method string
	Body   string
	Token  string
	// Clear closes the notification after the call succeeds.
	Clear bool
}

// Sender delivers a notification. The interface exists so tests never touch
// the network: CLAUDE.md is explicit that only the disposable dev topic may
// receive anything, and a test that could post to a real topic is one
// environment variable away from doing it.
type Sender interface {
	Send(ctx context.Context, n Notification) error
}

// HTTPSender posts to an ntfy topic.
type HTTPSender struct {
	Topic  string
	Client *http.Client
}

// NewHTTPSender builds a sender for a topic.
func NewHTTPSender(topic string) *HTTPSender {
	return &HTTPSender{
		Topic:  topic,
		Client: &http.Client{Timeout: 10 * time.Second},
	}
}

// Send posts one notification.
func (s *HTTPSender) Send(ctx context.Context, n Notification) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.Topic,
		bytes.NewReader([]byte(n.Body)))
	if err != nil {
		return err
	}
	req.Header.Set("Title", n.Title)
	if len(n.Tags) > 0 {
		req.Header.Set("Tags", strings.Join(n.Tags, ","))
	}
	if n.Click != "" {
		req.Header.Set("Click", n.Click)
	}
	if header := encodeActions(n.Actions); header != "" {
		req.Header.Set("Actions", header)
	}

	resp, err := s.Client.Do(req)
	if err != nil {
		return fmt.Errorf("posting to ntfy: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("ntfy answered %s", resp.Status)
	}
	return nil
}

// encodeActions renders the ntfy Actions header.
//
// The token goes in an Authorization header on the action rather than in the
// URL, because a token in a query string ends up in logs and history, and
// there is a test asserting no endpoint accepts one there.
func encodeActions(actions []Action) string {
	if len(actions) == 0 {
		return ""
	}
	parts := make([]string, 0, len(actions))
	for _, a := range actions {
		fields := []string{"http", a.Label, a.URL}
		if a.Method != "" {
			fields = append(fields, "method="+a.Method)
		}
		if a.Token != "" {
			fields = append(fields, "headers.Authorization=Bearer "+a.Token)
		}
		if a.Body != "" {
			fields = append(fields, "headers.Content-Type=application/json", "body="+a.Body)
		}
		if a.Clear {
			fields = append(fields, "clear=true")
		}
		parts = append(parts, strings.Join(fields, ", "))
	}
	return strings.Join(parts, "; ")
}

// Compose builds the notification for a task.
func Compose(t api.Task, baseURL, actionToken string, now time.Time, loc *time.Location) Notification {
	n := Notification{
		Title: t.Title,
		Body:  dueLine(t, now, loc),
		Tags:  []string{"spiral_calendar"},
		Click: strings.TrimSuffix(baseURL, "/") + "/t/" + itoa(t.Num),
	}
	if t.Priority != nil && *t.Priority <= 2 {
		n.Tags = append(n.Tags, "warning")
	}

	// Without a token the buttons cannot authenticate, so the notification is
	// a click-through rather than a set of buttons that fail.
	if actionToken == "" {
		return n
	}
	base := strings.TrimSuffix(baseURL, "/") + "/api/v1/tasks/" + t.ID
	n.Actions = []Action{
		{Label: "Done", URL: base + "/complete", Method: "POST", Token: actionToken, Clear: true},
		{
			Label: "Snooze 1h", URL: base + "/snooze", Method: "POST", Token: actionToken,
			Body: `{"duration":"1h"}`, Clear: true,
		},
	}
	return n
}

// dueLine is the notification body: what is due and when, in words.
func dueLine(t api.Task, now time.Time, loc *time.Location) string {
	if t.DueAt == nil {
		return "Due now"
	}
	if t.DueIsDate {
		day, err := time.ParseInLocation("2006-01-02", *t.DueAt, loc)
		if err != nil {
			return "Due " + *t.DueAt
		}
		if day.Format("2006-01-02") == now.In(loc).Format("2006-01-02") {
			return "Due today"
		}
		return "Due " + day.Format("Mon 2 Jan")
	}
	due, err := time.Parse(time.RFC3339, *t.DueAt)
	if err != nil {
		return "Due " + *t.DueAt
	}
	local := due.In(loc)
	if mins := int(local.Sub(now).Minutes()); mins > 0 && mins < 120 {
		return fmt.Sprintf("Due in %d minutes, at %s", mins, local.Format("15:04"))
	}
	return "Due at " + local.Format("15:04 on Mon 2 Jan")
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}
