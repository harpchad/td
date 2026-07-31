package query_test

import (
	"strings"
	"testing"

	"github.com/harpchad/td/internal/query"
)

// TestParseRecurrence covers what people type. The stored form is RRULE
// because inventing a syntax cannot express "the last weekday of the month",
// but nobody types FREQ=WEEKLY;BYDAY=MO at a prompt.
func TestParseRecurrence(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"every day", "FREQ=DAILY"},
		{"daily", "FREQ=DAILY"},
		{"every 2 days", "FREQ=DAILY;INTERVAL=2"},
		{"every monday", "FREQ=WEEKLY;BYDAY=MO"},
		{"every mon, wed, fri", "FREQ=WEEKLY;BYDAY=MO,WE,FR"},
		{"every weekday", "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR"},
		{"every 2 weeks", "FREQ=WEEKLY;INTERVAL=2"},
		{"weekly", "FREQ=WEEKLY"},
		{"monthly on the 1st", "FREQ=MONTHLY;BYMONTHDAY=1"},
		{"every month on the 15th", "FREQ=MONTHLY;BYMONTHDAY=15"},
		{"the 31st of the month", "FREQ=MONTHLY;BYMONTHDAY=31"},
		{"yearly", "FREQ=YEARLY"},
		{"every 3 months", "FREQ=MONTHLY;INTERVAL=3"},

		// Anything that already looks like a rule goes through untouched, so
		// the expressive form is never out of reach.
		{"FREQ=MONTHLY;BYDAY=-1FR", "FREQ=MONTHLY;BYDAY=-1FR"},
		{"RRULE:FREQ=WEEKLY;BYDAY=TU", "FREQ=WEEKLY;BYDAY=TU"},
		{"freq=daily", "FREQ=DAILY"},
	}

	for _, c := range cases {
		got, err := query.ParseRecurrence(c.in)
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("%q = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestParseRecurrenceRefusesRatherThanGuesses matters more than the happy
// path: a wrong guess repeats the wrong thing for a year before anyone
// notices, and the error has to name what it could not read.
func TestParseRecurrenceRefusesRatherThanGuesses(t *testing.T) {
	for _, bad := range []string{
		"",
		"   ",
		"sometimes",
		"every fortnight",
		"every 0 days",
		"every second tuesday of the month",
	} {
		got, err := query.ParseRecurrence(bad)
		if err == nil {
			t.Errorf("%q was accepted as %q", bad, got)
		}
	}

	// The message points at the word it choked on.
	_, err := query.ParseRecurrence("every fortnight")
	if err == nil || !strings.Contains(err.Error(), "fortnight") {
		t.Errorf("error = %v, want it to name the word", err)
	}
}
