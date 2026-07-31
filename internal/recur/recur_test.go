package recur_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/harpchad/td/internal/recur"
)

// Every case here comes out of testdata/recurrence_cases.json, whose values
// were computed with python-dateutil and IANA tzdata rather than by hand. The
// UTC offsets in the expected strings are part of the assertion: preserving
// wall-clock time across a DST boundary is the behavior being locked in.

type fixture struct {
	FixedMode []struct {
		Name        string   `json:"name"`
		RRule       string   `json:"rrule"`
		Dtstart     string   `json:"dtstart"`
		Occurrences []string `json:"occurrences"`
		Note        string   `json:"note"`
	} `json:"fixed_mode"`

	Catchup []struct {
		Name                string   `json:"name"`
		RRule               string   `json:"rrule"`
		Dtstart             string   `json:"dtstart"`
		Mode                string   `json:"mode"`
		Catchup             string   `json:"catchup"`
		Now                 string   `json:"now"`
		ExpectNextDue       string   `json:"expect_next_due"`
		ExpectMissed        []string `json:"expect_missed"`
		ExpectOpenInstances int      `json:"expect_open_instances"`
		ExpectDueDates      []string `json:"expect_due_dates"`
		Note                string   `json:"note"`
	} `json:"catchup"`

	AfterCompletion []struct {
		Name          string `json:"name"`
		RRule         string `json:"rrule"`
		Mode          string `json:"mode"`
		PreviousDue   string `json:"previous_due"`
		CompletedAt   string `json:"completed_at"`
		ExpectNextDue string `json:"expect_next_due"`
		Note          string `json:"note"`
	} `json:"after_completion_mode"`

	DSTEdges []struct {
		Name          string `json:"name"`
		LocalTime     string `json:"local_time"`
		Date          string `json:"date"`
		ExpectInstant string `json:"expect_instant"`
	} `json:"dst_edges"`
}

func load(t *testing.T) (fixture, *time.Location) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "testdata", "recurrence_cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f fixture
	if err := json.Unmarshal(body, &f); err != nil {
		t.Fatal(err)
	}
	// Timezone is America/Chicago for every case in the file.
	loc, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Fatal(err)
	}
	return f, loc
}

func mustParse(t *testing.T, s string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return at
}

// TestFixedMode runs every fixed-mode case. The offsets matter: a naive
// implementation that adds 168 hours for a weekly rule produces 08:00 on the
// far side of a DST change and passes every other weekly test.
func TestFixedMode(t *testing.T) {
	f, loc := load(t)
	if len(f.FixedMode) == 0 {
		t.Fatal("no fixed_mode cases loaded")
	}

	for _, c := range f.FixedMode {
		t.Run(c.Name, func(t *testing.T) {
			rule, err := recur.Parse(c.RRule, mustParse(t, c.Dtstart), loc)
			if err != nil {
				t.Fatal(err)
			}
			got, err := rule.Occurrences(len(c.Occurrences))
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(c.Occurrences) {
				t.Fatalf("got %d occurrences, want %d", len(got), len(c.Occurrences))
			}
			for i, want := range c.Occurrences {
				if g := got[i].Format(time.RFC3339); g != want {
					t.Errorf("occurrence %d = %s, want %s\n%s", i, g, want, c.Note)
				}
			}
		})
	}
}

// TestAfterCompletionMode covers the mode where the interval counts from
// completion and the previous due is ignored.
func TestAfterCompletionMode(t *testing.T) {
	f, loc := load(t)
	if len(f.AfterCompletion) == 0 {
		t.Fatal("no after_completion cases loaded")
	}

	for _, c := range f.AfterCompletion {
		t.Run(c.Name, func(t *testing.T) {
			completed := mustParse(t, c.CompletedAt)

			// The rule is anchored on the completion time, since that is what
			// this mode measures from.
			rule, err := recur.Parse(c.RRule, completed, loc)
			if err != nil {
				t.Fatal(err)
			}
			next := rule.NextAfterCompletion(completed)

			// A date-only previous due keeps the result date-only.
			dateOnly := len(c.PreviousDue) == len(recur.DateLayout) && c.PreviousDue != ""
			got := next.Format(time.RFC3339)
			if dateOnly || len(c.ExpectNextDue) == len(recur.DateLayout) {
				got = next.Format(recur.DateLayout)
			}
			if got != c.ExpectNextDue {
				t.Errorf("next due = %s, want %s\n%s", got, c.ExpectNextDue, c.Note)
			}
		})
	}
}

// TestDSTEdges covers the two cases the fixture calls out by name.
func TestDSTEdges(t *testing.T) {
	f, loc := load(t)
	if len(f.DSTEdges) != 2 {
		t.Fatalf("%d dst_edges cases, want 2", len(f.DSTEdges))
	}

	for _, c := range f.DSTEdges {
		t.Run(c.Name, func(t *testing.T) {
			day, err := time.Parse(recur.DateLayout, c.Date)
			if err != nil {
				t.Fatal(err)
			}
			clock, err := time.Parse("15:04", c.LocalTime)
			if err != nil {
				t.Fatal(err)
			}
			y, m, d := day.Date()

			got := recur.ResolveLocal(y, m, d, clock.Hour(), clock.Minute(), 0, loc)
			if g := got.Format(time.RFC3339); g != c.ExpectInstant {
				t.Errorf("%s on %s = %s, want %s", c.LocalTime, c.Date, g, c.ExpectInstant)
			}
		})
	}
}

// TestAGapInARuleShiftsForward is the same edge reached the way it actually
// happens: through a rule that crosses a spring-forward day. rrule-go builds
// its results with time.Date, which normalizes a nonexistent local time
// backwards, so this only passes because the wall clock is re-imposed.
func TestAGapInARuleShiftsForward(t *testing.T) {
	_, loc := load(t)

	// 02:30 daily, across 2026-03-08 when 02:30 does not exist.
	start := time.Date(2026, 3, 6, 2, 30, 0, 0, loc)
	rule, err := recur.Parse("FREQ=DAILY", start, loc)
	if err != nil {
		t.Fatal(err)
	}
	got, err := rule.Occurrences(4)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"2026-03-06T02:30:00-06:00",
		"2026-03-07T02:30:00-06:00",
		"2026-03-08T03:30:00-05:00", // the gap: forward, not back to 01:30
		"2026-03-09T02:30:00-05:00",
	}
	for i, w := range want {
		if g := got[i].Format(time.RFC3339); g != w {
			t.Errorf("occurrence %d = %s, want %s", i, g, w)
		}
	}
}

// TestCatchup covers both policies. Skip rolls forward and logs the misses;
// pile creates the backlog. The default is skip, because anything that piles
// up silently gets ignored and then the whole list gets ignored.
func TestCatchup(t *testing.T) {
	f, loc := load(t)
	if len(f.Catchup) != 2 {
		t.Fatalf("%d catchup cases, want 2", len(f.Catchup))
	}

	for _, c := range f.Catchup {
		t.Run(c.Name, func(t *testing.T) {
			start := mustParse(t, c.Dtstart)
			now := mustParse(t, c.Now)

			rule, err := recur.Parse(c.RRule, start, loc)
			if err != nil {
				t.Fatal(err)
			}
			missed, err := rule.Between(start, now)
			if err != nil {
				t.Fatal(err)
			}

			switch c.Catchup {
			case recur.CatchupSkip:
				// The misses are logged as events, and exactly one instance
				// stays open.
				if len(missed) != len(c.ExpectMissed) {
					t.Fatalf("%d missed, want %d: %v", len(missed), len(c.ExpectMissed), format(missed, recur.DateLayout))
				}
				for i, want := range c.ExpectMissed {
					if g := missed[i].Format(recur.DateLayout); g != want {
						t.Errorf("miss %d = %s, want %s", i, g, want)
					}
				}
				next, err := rule.After(now)
				if err != nil {
					t.Fatal(err)
				}
				if g := next.Format(time.RFC3339); g != c.ExpectNextDue {
					t.Errorf("next due = %s, want %s\n%s", g, c.ExpectNextDue, c.Note)
				}

			case recur.CatchupPile:
				// Every missed occurrence becomes an open instance.
				if len(missed) != c.ExpectOpenInstances {
					t.Fatalf("%d open instances, want %d: %v",
						len(missed), c.ExpectOpenInstances, format(missed, recur.DateLayout))
				}
				for i, want := range c.ExpectDueDates {
					if g := missed[i].Format(recur.DateLayout); g != want {
						t.Errorf("instance %d due %s, want %s", i, g, want)
					}
				}
			}
		})
	}
}

func format(times []time.Time, layout string) []string {
	out := make([]string, 0, len(times))
	for _, t := range times {
		out = append(out, t.Format(layout))
	}
	return out
}
