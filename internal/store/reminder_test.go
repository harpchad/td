package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/notify"
)

// recorder is a Sender that keeps what it was given. Nothing in the test
// suite may reach a real ntfy topic: CLAUDE.md says only the disposable dev
// topic ever receives anything, and a test that could post to one is one
// environment variable away from doing it.
type recorder struct {
	sent []notify.Notification
	fail error
}

func (r *recorder) Send(_ context.Context, n notify.Notification) error {
	if r.fail != nil {
		return r.fail
	}
	r.sent = append(r.sent, n)
	return nil
}

func (r *recorder) titles() []string {
	out := make([]string, 0, len(r.sent))
	for _, n := range r.sent {
		out = append(out, n.Title)
	}
	return out
}

func scheduler(t *testing.T, policy notify.Policy) (*notify.Scheduler, *recorder) {
	t.Helper()
	s, now := seeded(t)
	rec := &recorder{}

	return &notify.Scheduler{
		Policy: policy, Store: s, Sender: rec,
		BaseURL: "https://td.example.com", Loc: now.Location(),
		Log: discardLogger(), Now: func() time.Time { return now },
	}, rec
}

// TestOnlyDueDatesFire covers the v1 scope: no morning digest, no waiting-on
// nags, no inbox threshold.
func TestOnlyDueDatesFire(t *testing.T) {
	policy := notify.DefaultPolicy
	policy.Topic = "https://ntfy.invalid/td-test"
	policy.DefaultRule = "*" // everything, so nothing is filtered out
	policy.QuietHours = ""

	sched, rec := scheduler(t, policy)
	// The fixture clock is 2026-08-03 10:30. Tasks due before that with a
	// date-only due fire at 08:00 on their day, so anything due 08-03 or
	// earlier is past its fire time.
	if _, err := sched.Deliver(context.Background(), sched.Now()); err != nil {
		t.Fatal(err)
	}

	for _, title := range rec.titles() {
		switch title {
		case "Order tires", "Ask Brandiss about headcount", "Waiting on VPN quote":
			t.Errorf("%q has no due date and got a reminder", title)
		}
	}
	if len(rec.sent) == 0 {
		t.Fatal("nothing fired at all")
	}
}

// TestNotifiedAtStopsRepeats covers the rule that one push goes out per task
// per due value, and that overdue does not re-push.
func TestNotifiedAtStopsRepeats(t *testing.T) {
	policy := notify.DefaultPolicy
	policy.Topic = "https://ntfy.invalid/td-test"
	policy.DefaultRule = "*"
	policy.QuietHours = ""

	sched, rec := scheduler(t, policy)
	ctx := context.Background()

	first, err := sched.Deliver(ctx, sched.Now())
	if err != nil {
		t.Fatal(err)
	}
	if first == 0 {
		t.Fatal("the first pass sent nothing")
	}

	// A second pass, and a third an hour later. An overdue task nagging every
	// hour teaches you to swipe it away without reading it.
	again, err := sched.Deliver(ctx, sched.Now())
	if err != nil {
		t.Fatal(err)
	}
	later, err := sched.Deliver(ctx, sched.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if again != 0 || later != 0 {
		t.Errorf("re-pushed %d then %d times", again, later)
	}
	if len(rec.sent) != first {
		t.Errorf("%d notifications for %d eligible tasks", len(rec.sent), first)
	}
}

// TestChangingTheDueDateMakesItEligibleAgain covers the other half of that
// rule: notified_at is cleared by a due change, which is what the transition
// fixture requires.
func TestChangingTheDueDateMakesItEligibleAgain(t *testing.T) {
	policy := notify.DefaultPolicy
	policy.Topic = "https://ntfy.invalid/td-test"
	policy.DefaultRule = "*"
	policy.QuietHours = ""

	s, now := seeded(t)
	rec := &recorder{}
	sched := &notify.Scheduler{
		Policy: policy, Store: s, Sender: rec,
		BaseURL: "https://td.example.com", Loc: now.Location(),
		Log: discardLogger(), Now: func() time.Time { return now },
	}
	ctx := context.Background()

	if _, err := sched.Deliver(ctx, now); err != nil {
		t.Fatal(err)
	}
	before := len(rec.sent)

	task, err := s.GetByNum(ctx, 102)
	if err != nil {
		t.Fatal(err)
	}
	due := "2026-08-03"
	if _, err := s.Patch(ctx, actor, task.ID,
		api.TaskPatch{DueAt: &due, Presence: map[string]bool{"due_at": true}}, "", now); err != nil {
		t.Fatal(err)
	}

	if _, err := sched.Deliver(ctx, now); err != nil {
		t.Fatal(err)
	}
	if len(rec.sent) != before+1 {
		t.Errorf("a due date change did not make the task eligible again: %d then %d",
			before, len(rec.sent))
	}
}

// TestTheTriStateOverridesTheRule covers notify=on and notify=off beating the
// default rule in both directions.
func TestTheTriStateOverridesTheRule(t *testing.T) {
	policy := notify.DefaultPolicy
	policy.Topic = "https://ntfy.invalid/td-test"
	policy.QuietHours = ""
	// A rule that selects nothing, so only notify=on can fire.
	policy.DefaultRule = "#nosuchtag"

	s, now := seeded(t)
	rec := &recorder{}
	sched := &notify.Scheduler{
		Policy: policy, Store: s, Sender: rec,
		BaseURL: "https://td.example.com", Loc: now.Location(),
		Log: discardLogger(), Now: func() time.Time { return now },
	}
	ctx := context.Background()

	if n, err := sched.Deliver(ctx, now); err != nil {
		t.Fatal(err)
	} else if n != 0 {
		t.Fatalf("a rule matching nothing still sent %d", n)
	}

	// Turn one on explicitly.
	task, err := s.GetByNum(ctx, 104)
	if err != nil {
		t.Fatal(err)
	}
	on := api.NotifyOn
	if _, err := s.Patch(ctx, actor, task.ID,
		api.TaskPatch{Notify: &on, Presence: map[string]bool{"notify": true}}, "", now); err != nil {
		t.Fatal(err)
	}
	if n, err := sched.Deliver(ctx, now); err != nil {
		t.Fatal(err)
	} else if n != 1 {
		t.Errorf("notify=on sent %d, want 1", n)
	}

	// And one off against a rule that would select it.
	policy.DefaultRule = "*"
	sched.Policy = policy
	off := api.NotifyOff
	other, err := s.GetByNum(ctx, 114)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Patch(ctx, actor, other.ID,
		api.TaskPatch{Notify: &off, Presence: map[string]bool{"notify": true}}, "", now); err != nil {
		t.Fatal(err)
	}
	rec.sent = nil
	if _, err := sched.Deliver(ctx, now); err != nil {
		t.Fatal(err)
	}
	for _, n := range rec.sent {
		if n.Title == "Upload SOC2 evidence" {
			t.Error("notify=off still fired")
		}
	}
}

// TestQuietHoursDelayRatherThanDrop covers the rule end to end: a push that
// would land in the window goes out when the window opens.
func TestQuietHoursDelayRatherThanDrop(t *testing.T) {
	policy := notify.DefaultPolicy
	policy.Topic = "https://ntfy.invalid/td-test"
	// Narrowed to #monday, which is task 102 due on the fixture's today and
	// task 108 due later. A rule of "*" would also select the tasks that were
	// already overdue when the fixture was written, whose held release time
	// is days in the past, and those fire correctly and drown out the case
	// under test.
	policy.DefaultRule = "#monday"
	policy.QuietHours = "22:00-06:00"
	// Fire date-only dues at 23:00, which is inside the window.
	policy.DateOnlyAt = "23:00"

	s, now := seeded(t)
	rec := &recorder{}
	sched := &notify.Scheduler{
		Policy: policy, Store: s, Sender: rec,
		BaseURL: "https://td.example.com", Loc: now.Location(),
		Log: discardLogger(), Now: func() time.Time { return now },
	}
	ctx := context.Background()

	// 23:30 on the day task 102 is due. Its fire time is 23:00, inside the
	// window, so it is held.
	quiet := time.Date(2026, 8, 3, 23, 30, 0, 0, now.Location())
	if n, err := sched.Deliver(ctx, quiet); err != nil {
		t.Fatal(err)
	} else if n != 0 {
		t.Errorf("%d pushes went out during quiet hours", n)
	}

	// 06:00 the next morning: the window opens and the held push goes.
	open := time.Date(2026, 8, 4, 6, 0, 0, 0, now.Location())
	n, err := sched.Deliver(ctx, open)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("%d pushes after the window opened, want the one that was held", n)
	}
	if len(rec.sent) != 1 || rec.sent[0].Title != "Follow up on monday forms" {
		t.Errorf("held push = %v, want the task due that day", rec.titles())
	}
}

// TestAFailedSendIsRetried covers the choice not to mark a task notified
// until the push actually went, so ntfy being down is a delay rather than a
// lost reminder.
func TestAFailedSendIsRetried(t *testing.T) {
	policy := notify.DefaultPolicy
	policy.Topic = "https://ntfy.invalid/td-test"
	policy.DefaultRule = "*"
	policy.QuietHours = ""

	s, now := seeded(t)
	rec := &recorder{fail: context.DeadlineExceeded}
	sched := &notify.Scheduler{
		Policy: policy, Store: s, Sender: rec,
		BaseURL: "https://td.example.com", Loc: now.Location(),
		Log: discardLogger(), Now: func() time.Time { return now },
	}
	ctx := context.Background()

	if n, err := sched.Deliver(ctx, now); err != nil {
		t.Fatal(err)
	} else if n != 0 {
		t.Fatalf("a failing sender reported %d sent", n)
	}

	// Nothing was marked, so the next pass tries again.
	rec.fail = nil
	n, err := sched.Deliver(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Error("a task whose push failed was marked notified anyway, so the reminder is lost")
	}
}

// TestTheNotificationCarriesActionsAndAClickThrough covers what the push
// actually contains.
func TestTheNotificationCarriesActionsAndAClickThrough(t *testing.T) {
	s, now := seeded(t)
	task, err := s.GetByNum(context.Background(), 104)
	if err != nil {
		t.Fatal(err)
	}

	n := notify.Compose(task, "https://td.example.com/", "td_secret", now, now.Location())

	if n.Title != task.Title {
		t.Errorf("title = %q", n.Title)
	}
	if n.Click != "https://td.example.com/t/104" {
		t.Errorf("click = %q, want the task in the web UI", n.Click)
	}
	if len(n.Actions) != 2 {
		t.Fatalf("%d actions, want Done and Snooze 1h", len(n.Actions))
	}
	for _, a := range n.Actions {
		if a.Token != "td_secret" {
			t.Errorf("action %q carries no token", a.Label)
		}
		// A token in a query string ends up in logs and history, and there is
		// a test asserting no endpoint accepts one there.
		if contains(a.URL, "td_secret") {
			t.Errorf("action %q puts the token in the URL: %s", a.Label, a.URL)
		}
	}

	// Without a token there are no buttons, rather than buttons that 401.
	bare := notify.Compose(task, "https://td.example.com", "", now, now.Location())
	if len(bare.Actions) != 0 {
		t.Error("actions were composed with no token to authenticate them")
	}
	if bare.Click == "" {
		t.Error("the click-through went away with the actions")
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
