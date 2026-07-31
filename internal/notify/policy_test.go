package notify_test

import (
	"strings"
	"testing"
	"time"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/notify"
)

func chicago(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func task(due string, dateOnly bool, notifyMode string) api.Task {
	return api.Task{DueAt: &due, DueIsDate: dateOnly, Notify: notifyMode}
}

// TestDateOnlyDueFiresAtTheConfiguredHour covers the rule that a date-only
// due is a calendar date rather than an instant: "pay the mortgage on the
// 1st" fires at date_only_at on that day.
func TestDateOnlyDueFiresAtTheConfiguredHour(t *testing.T) {
	loc := chicago(t)
	p := notify.DefaultPolicy

	at, ok := p.FireAt(task("2026-08-04", true, api.NotifyAuto), loc)
	if !ok {
		t.Fatal("a date-only due did not produce a fire time")
	}
	if want := "2026-08-04T08:00:00-05:00"; at.Format(time.RFC3339) != want {
		t.Errorf("fire at %s, want %s", at.Format(time.RFC3339), want)
	}

	// The lead time does not apply to a date-only due: it is not an instant
	// to be early for.
	p.LeadMinutes = 120
	again, _ := p.FireAt(task("2026-08-04", true, api.NotifyAuto), loc)
	if !again.Equal(at) {
		t.Errorf("lead_minutes moved a date-only due to %s", again.Format(time.RFC3339))
	}
}

// TestDatetimeDueFiresEarlyByTheLeadTime covers the other half.
func TestDatetimeDueFiresEarlyByTheLeadTime(t *testing.T) {
	loc := chicago(t)
	p := notify.DefaultPolicy // 30 minutes

	// Stored as UTC, as everything instant-shaped is.
	at, ok := p.FireAt(task("2026-08-04T14:00:00Z", false, api.NotifyAuto), loc)
	if !ok {
		t.Fatal("no fire time")
	}
	if want := "2026-08-04T08:30:00-05:00"; at.Format(time.RFC3339) != want {
		t.Errorf("fire at %s, want %s (30 minutes before 09:00 Central)",
			at.Format(time.RFC3339), want)
	}
}

func TestATaskWithNoDueDateNeverFires(t *testing.T) {
	loc := chicago(t)
	if _, ok := notify.DefaultPolicy.FireAt(api.Task{Notify: api.NotifyOn}, loc); ok {
		t.Error("a task with no due date produced a fire time")
	}
}

// TestQuietHoursHoldRatherThanDrop covers the stated rule. A push that lands
// inside the window goes out when the window opens; it is not lost.
func TestQuietHoursHoldRatherThanDrop(t *testing.T) {
	loc := chicago(t)
	window, err := notify.ParseQuietHours("22:00-06:00")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		at      string
		quiet   bool
		release string
	}{
		{name: "before the window", at: "2026-08-03T21:59:00-05:00", quiet: false},
		{name: "just inside", at: "2026-08-03T22:00:00-05:00", quiet: true, release: "2026-08-04T06:00:00-05:00"},
		{name: "after midnight", at: "2026-08-04T02:30:00-05:00", quiet: true, release: "2026-08-04T06:00:00-05:00"},
		{name: "the moment it opens", at: "2026-08-04T06:00:00-05:00", quiet: false},
		{name: "the middle of the day", at: "2026-08-04T13:00:00-05:00", quiet: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			at, err := time.ParseInLocation(time.RFC3339, tc.at, loc)
			if err != nil {
				t.Fatal(err)
			}
			if got := window.Contains(at); got != tc.quiet {
				t.Fatalf("Contains(%s) = %v, want %v", tc.at, got, tc.quiet)
			}

			release := window.Release(at)
			if !tc.quiet {
				if !release.Equal(at) {
					t.Errorf("a push outside the window was held until %s", release)
				}
				return
			}
			if got := release.In(loc).Format(time.RFC3339); got != tc.release {
				t.Errorf("released at %s, want %s", got, tc.release)
			}
		})
	}
}

func TestNoQuietHoursHoldsNothing(t *testing.T) {
	window, err := notify.ParseQuietHours("")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 8, 4, 3, 0, 0, 0, chicago(t))
	if window.Contains(at) {
		t.Error("an empty quiet_hours held a push at 03:00")
	}
	if !window.Release(at).Equal(at) {
		t.Error("an empty quiet_hours delayed a push")
	}
}

// TestTheTriStateIsNotACheckbox covers why notify has three values: on and
// off are the task's own answer, auto defers to the policy.
func TestTheTriStateIsNotACheckbox(t *testing.T) {
	p := notify.DefaultPolicy

	tests := []struct {
		mode        string
		matchesRule bool
		want        bool
	}{
		{api.NotifyOn, false, true}, // on overrides a rule that says no
		{api.NotifyOn, true, true},
		{api.NotifyOff, true, false}, // off overrides a rule that says yes
		{api.NotifyOff, false, false},
		{api.NotifyAuto, true, true}, // auto is whatever the default says
		{api.NotifyAuto, false, false},
	}
	for _, tc := range tests {
		got := p.Resolve(api.Task{Notify: tc.mode}, tc.matchesRule)
		if got != tc.want {
			t.Errorf("notify=%s with rule match %v = %v, want %v",
				tc.mode, tc.matchesRule, got, tc.want)
		}
	}
}

// TestRuleLiterals covers "*" for always on and "" for always off, which are
// the two cases a filter expression cannot state.
func TestRuleLiterals(t *testing.T) {
	always := notify.Policy{DefaultRule: "*"}
	if _, on, off := always.RuleQuery(); !on || off {
		t.Error(`"*" did not resolve to always on`)
	}
	never := notify.Policy{DefaultRule: ""}
	if _, on, off := never.RuleQuery(); on || !off {
		t.Error(`"" did not resolve to always off`)
	}
	rule := notify.Policy{DefaultRule: "p:<=2 -#someday"}
	filter, on, off := rule.RuleQuery()
	if on || off || filter != "p:<=2 -#someday" {
		t.Errorf("a real rule resolved to %q on=%v off=%v", filter, on, off)
	}
}

// TestPolicyValidation covers the config errors, which have to say what is
// wrong rather than failing at the first push.
func TestPolicyValidation(t *testing.T) {
	if err := notify.DefaultPolicy.Validate(); err != nil {
		t.Fatalf("the default policy does not validate: %v", err)
	}

	bad := []struct {
		name   string
		policy notify.Policy
		want   string
	}{
		{"a rule that does not parse", notify.Policy{DefaultRule: "foo:bar"}, "default_rule"},
		{"a broken quiet range", notify.Policy{QuietHours: "22:00", DefaultRule: "*"}, "quiet_hours"},
		{"a broken clock time", notify.Policy{DateOnlyAt: "8am", DefaultRule: "*"}, "date_only_at"},
		{"a negative lead", notify.Policy{LeadMinutes: -1, DefaultRule: "*"}, "lead_minutes"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.policy.Validate()
			if err == nil {
				t.Fatal("accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not name %q", err, tc.want)
			}
		})
	}
}

// TestRemindersAreOffUntilATopicIsSet covers the safe default. A server that
// has not been told where to push does not guess, and CLAUDE.md is explicit
// that only the disposable dev topic may ever receive anything.
func TestRemindersAreOffUntilATopicIsSet(t *testing.T) {
	if notify.DefaultPolicy.Enabled() {
		t.Error("the default policy would send somewhere")
	}
	if (notify.Policy{Topic: "  "}).Enabled() {
		t.Error("a blank topic counts as configured")
	}
	if !(notify.Policy{Topic: "https://ntfy.sh/td-dev"}).Enabled() {
		t.Error("a real topic does not count as configured")
	}
}
