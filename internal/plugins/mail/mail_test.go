package mail_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/plugins/mail"
	"github.com/harpchad/td/internal/sync"
)

// The fixtures are hand written from Graph's published message and
// followupFlag documentation. No mailbox was contacted to produce them, and
// none can be: the addresses are example.invalid.

func chicago(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "plugins", "mail", name))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// graph serves the fixtures, following the paging the first one points at.
func graph(t *testing.T, pages ...string) *mail.Client {
	t.Helper()

	var srv *httptest.Server
	page := 0
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("graph called without the bearer: %q", got)
		}
		if page >= len(pages) {
			t.Errorf("unexpected extra request for %s", r.URL)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		body := fixture(t, pages[page])
		page++

		// The fixture's nextLink is an absolute graph.microsoft.com URL, which
		// the test server cannot serve. Rewritten so paging is exercised
		// against this handler rather than skipped.
		body = []byte(strings.ReplaceAll(string(body),
			"https://graph.microsoft.com/v1.0", srv.URL))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)

	c := mail.New(mail.Config{Endpoint: srv.URL, GraphToken: "test-token"})
	return c
}

// fakePoster records what a run posted and answers with what td already holds.
type fakePoster struct {
	captured []string
	posted   []sync.Request
}

func (f *fakePoster) Sync(_ context.Context, _ string, req sync.Request) (sync.Result, error) {
	f.posted = append(f.posted, req)
	return sync.Result{Cursor: req.Cursor, Created: len(req.Items)}, nil
}

func (f *fakePoster) Captured(_ context.Context, _ string) ([]string, error) {
	return f.captured, nil
}

func (f *fakePoster) items() []sync.Item {
	var out []sync.Item
	for _, req := range f.posted {
		out = append(out, req.Items...)
	}
	return out
}

// TestOnlyFlaggedMessagesBecomeTasks. The $filter is sent, and not trusted: a
// server that ignores it or a proxy that rewrites it would otherwise turn a
// whole mailbox into tasks, which is very hard to undo by hand.
func TestOnlyFlaggedMessagesBecomeTasks(t *testing.T) {
	c := graph(t, "flagged.json", "flagged_page2.json")
	poster := &fakePoster{}

	if _, err := mail.Run(t.Context(), c, poster, time.Now(), chicago(t)); err != nil {
		t.Fatal(err)
	}

	for _, item := range poster.items() {
		if item.ExternalID == "AAMkAG_COMPLETE" {
			t.Error("a message whose flag is complete was captured")
		}
	}
	if len(poster.items()) != 4 {
		t.Errorf("captured %d messages, want the 4 flagged ones across both pages", len(poster.items()))
	}
}

// TestPagingIsFollowed. A mailbox with more flags than a page would otherwise
// capture only the first page and look like it had worked.
func TestPagingIsFollowed(t *testing.T) {
	c := graph(t, "flagged.json", "flagged_page2.json")
	poster := &fakePoster{}

	if _, err := mail.Run(t.Context(), c, poster, time.Now(), chicago(t)); err != nil {
		t.Fatal(err)
	}

	var found bool
	for _, item := range poster.items() {
		if item.ExternalID == "AAMkAG_INVOICE" {
			found = true
		}
	}
	if !found {
		t.Error("the second page was never read")
	}
}

// TestAMessageIsCapturedOnce is the whole design.
//
// A mirror re-posts its window every run. This does not: the person owns the
// task the moment it exists, so a second post would overwrite the title they
// fixed and the status they set.
func TestAMessageIsCapturedOnce(t *testing.T) {
	// td already holds the renewal, as it would after the first run.
	poster := &fakePoster{captured: []string{"AAMkAG_RENEWAL"}}
	c := graph(t, "flagged_later.json")

	if _, err := mail.Run(t.Context(), c, poster, time.Now(), chicago(t)); err != nil {
		t.Fatal(err)
	}

	if len(poster.items()) != 1 {
		t.Fatalf("posted %d items, want only the new one", len(poster.items()))
	}
	if got := poster.items()[0].ExternalID; got != "AAMkAG_LATER" {
		t.Errorf("posted %q, want the message td had not seen", got)
	}
}

// TestNothingIsEverMarkedGone. Unflagging a mail is somebody tidying their
// inbox, not a statement that the task never happened.
func TestNothingIsEverMarkedGone(t *testing.T) {
	// Every message td holds is absent from this run's fixture.
	poster := &fakePoster{captured: []string{"AAMkAG_RENEWAL", "AAMkAG_GONE_FROM_MAILBOX"}}
	c := graph(t, "flagged_later.json")

	if _, err := mail.Run(t.Context(), c, poster, time.Now(), chicago(t)); err != nil {
		t.Fatal(err)
	}
	for _, req := range poster.posted {
		if len(req.Gone) > 0 {
			t.Errorf("the run sent a gone list: %v", req.Gone)
		}
	}
}

// TestAFlagDueDateLandsOnTheDayTheFlagNames.
//
// The fixture's flag says 2026-08-07T04:00:00Z, which is how Outlook stores
// the 7th. Read as an instant in Chicago that is 23:00 on the 6th, and the
// task arrives a day early. It was written that way first and measured:
// Chicago and Los Angeles lost a day, UTC and Tokyo did not.
func TestAFlagDueDateLandsOnTheDayTheFlagNames(t *testing.T) {
	c := graph(t, "flagged.json", "flagged_page2.json")
	poster := &fakePoster{}

	if _, err := mail.Run(t.Context(), c, poster, time.Now(), chicago(t)); err != nil {
		t.Fatal(err)
	}

	for _, item := range poster.items() {
		switch item.ExternalID {
		case "AAMkAG_CONTRACT":
			if item.DueAt == nil {
				t.Fatal("the flag's due date did not travel")
			}
			if *item.DueAt != "2026-08-07" {
				t.Errorf("due = %s, want 2026-08-07, the day the flag names", *item.DueAt)
			}
		case "AAMkAG_RENEWAL":
			if item.DueAt != nil {
				t.Errorf("a flag with no due date produced %s", *item.DueAt)
			}
		}
	}
}

// TestTheDueDateIsTheSameInEveryTimezone. The server's zone is not a fact
// about when somebody's contract is due.
func TestTheDueDateIsTheSameInEveryTimezone(t *testing.T) {
	flag := mail.GraphFlag{DueDateTime: &mail.GraphDateTime{
		DateTime: "2026-08-07T04:00:00.0000000", TimeZone: "UTC",
	}}
	for _, zone := range []string{
		"UTC", "America/Chicago", "America/Los_Angeles", "Europe/London",
		"Asia/Tokyo", "Pacific/Kiritimati",
	} {
		loc, err := time.LoadLocation(zone)
		if err != nil {
			t.Fatal(err)
		}
		if got := mail.FlagDue(flag, loc); got != "2026-08-07" {
			t.Errorf("a server in %s reads the flag as %s, want 2026-08-07", zone, got)
		}
	}
}

// TestFlagDueReadsGraphsShape covers the layouts directly, since the fixture
// only carries one of them and Graph emits several.
func TestFlagDueReadsGraphsShape(t *testing.T) {
	loc := chicago(t)

	for _, tc := range []struct {
		name  string
		flag  mail.GraphFlag
		want  string
		empty bool
	}{
		{
			name: "seven fractional digits, which RFC3339 will not read",
			flag: mail.GraphFlag{DueDateTime: &mail.GraphDateTime{
				DateTime: "2026-08-07T12:00:00.0000000", TimeZone: "UTC",
			}},
			want: "2026-08-07",
		},
		{
			name: "no fractional part at all",
			flag: mail.GraphFlag{DueDateTime: &mail.GraphDateTime{
				DateTime: "2026-08-07T12:00:00", TimeZone: "UTC",
			}},
			want: "2026-08-07",
		},
		{
			name:  "no due date on the flag",
			flag:  mail.GraphFlag{FlagStatus: "flagged"},
			empty: true,
		},
		{
			name: "something unparseable, rather than a wrong date",
			flag: mail.GraphFlag{DueDateTime: &mail.GraphDateTime{
				DateTime: "next tuesday", TimeZone: "UTC",
			}},
			empty: true,
		},
		{
			name: "a zone that is not UTC, where the date is already local",
			flag: mail.GraphFlag{DueDateTime: &mail.GraphDateTime{
				DateTime: "2026-08-07T00:00:00.0000000", TimeZone: "Central Standard Time",
			}},
			want: "2026-08-07",
		},
	} {
		got := mail.FlagDue(tc.flag, loc)
		if tc.empty {
			if got != "" {
				t.Errorf("%s: got %q, want nothing", tc.name, got)
			}
			continue
		}
		if got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestAMessageWithNoSubjectStillHasATitle. An empty subject is legal in mail
// and common in replies from phones. A task called "" cannot be acted on and
// cannot be searched for.
func TestAMessageWithNoSubjectStillHasATitle(t *testing.T) {
	c := graph(t, "flagged.json", "flagged_page2.json")
	poster := &fakePoster{}

	if _, err := mail.Run(t.Context(), c, poster, time.Now(), chicago(t)); err != nil {
		t.Fatal(err)
	}
	for _, item := range poster.items() {
		if strings.TrimSpace(item.Title) == "" {
			t.Errorf("%s produced a task with no title", item.ExternalID)
		}
		if item.ExternalID == "AAMkAG_NOSUBJECT" && !strings.Contains(item.Title, "numbers") {
			t.Errorf("the fallback title is %q, want the preview", item.Title)
		}
	}
}

// TestEverythingLandsInTheInbox. It is a capture, and what is captured gets
// sorted by a person. Nothing arrives already prioritized.
func TestEverythingLandsInTheInbox(t *testing.T) {
	c := graph(t, "flagged.json", "flagged_page2.json")
	poster := &fakePoster{}

	if _, err := mail.Run(t.Context(), c, poster, time.Now(), chicago(t)); err != nil {
		t.Fatal(err)
	}
	for _, item := range poster.items() {
		if item.Status != api.StatusInbox {
			t.Errorf("%s arrived as %s, want the inbox", item.ExternalID, item.Status)
		}
		if item.URL == "" {
			t.Errorf("%s has no link back to the mail", item.ExternalID)
		}
	}
}

// TestTheSenderTravelsAsAnAddress. A name match merges two different Staceys;
// an address is an identity, and it is what lets "everything involving
// Stacey" span the mailbox and the board.
func TestTheSenderTravelsAsAnAddress(t *testing.T) {
	c := graph(t, "flagged.json", "flagged_page2.json")
	poster := &fakePoster{}

	if _, err := mail.Run(t.Context(), c, poster, time.Now(), chicago(t)); err != nil {
		t.Fatal(err)
	}
	for _, item := range poster.items() {
		if item.ExternalID != "AAMkAG_RENEWAL" {
			continue
		}
		if len(item.People) != 1 {
			t.Fatalf("%d people on the task, want the sender", len(item.People))
		}
		p := item.People[0]
		if p.Email != "skennedy@example.invalid" {
			t.Errorf("email = %q", p.Email)
		}
		if p.Role != api.RoleInvolved {
			t.Errorf("role = %q, want involved: they sent you something, which is "+
				"not the same as owing you the work", p.Role)
		}
		if p.Name == "" {
			t.Error("no name, so the server cannot create the person if it is new")
		}
	}
}

// TestTheBodyIsNotCopiedIn. A task list is not a mail client, and pasting a
// thread into notes makes every list unreadable.
func TestTheBodyIsNotCopiedIn(t *testing.T) {
	c := graph(t, "flagged.json", "flagged_page2.json")
	poster := &fakePoster{}

	if _, err := mail.Run(t.Context(), c, poster, time.Now(), chicago(t)); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(poster.posted)
	if err != nil {
		t.Fatal(err)
	}
	// A phrase that appears only in a fixture's bodyPreview.
	if strings.Contains(string(encoded), "remains unpaid") {
		t.Error("a message body was copied into the task")
	}
}

// TestAnEmptyMailboxPostsNothing, rather than an empty batch that writes a
// cursor and an event for no reason.
func TestAnEmptyMailboxPostsNothing(t *testing.T) {
	poster := &fakePoster{captured: []string{"AAMkAG_RENEWAL", "AAMkAG_LATER"}}
	c := graph(t, "flagged_later.json")

	res, err := mail.Run(t.Context(), c, poster, time.Now(), chicago(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(poster.posted) != 0 {
		t.Errorf("posted %d batches with nothing new to say", len(poster.posted))
	}
	if res.Cursor == "" {
		t.Error("the run reported no cursor")
	}
}
