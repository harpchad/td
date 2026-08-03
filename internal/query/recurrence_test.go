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

// TestDescribeRecurrenceInvertsWhatParseProduces.
//
// RRULE is the right storage format and the wrong thing to show a person:
// "FREQ=WEEKLY;INTERVAL=2;BYDAY=MO" is a fact about a standard, not an answer
// to "how often does this happen". These are the shapes ParseRecurrence emits,
// so every one of them has to come back readable.
func TestDescribeRecurrenceInvertsWhatParseProduces(t *testing.T) {
	cases := []struct{ said, want string }{
		{"every day", "every day"},
		{"daily", "every day"},
		{"every 2 days", "every 2 days"},
		{"every week", "every week"},
		{"every monday", "every Monday"},
		{"every monday and friday", "every Monday and Friday"},
		{"every weekday", "every weekday"},
		{"every 2 weeks", "every 2 weeks"},
		{"monthly", "every month"},
		{"monthly on the 1st", "every month on the 1st"},
		{"every 3 months", "every 3 months"},
		{"yearly", "every year"},
	}
	for _, tc := range cases {
		rule, err := query.ParseRecurrence(tc.said)
		if err != nil {
			t.Errorf("%q did not parse: %v", tc.said, err)
			continue
		}
		if got := query.DescribeRecurrence(rule); got != tc.want {
			t.Errorf("%q -> %s -> %q, want %q", tc.said, rule, got, tc.want)
		}
	}
}

// TestDescribeRecurrenceRoundTrips. Whatever it says must parse back to the
// same rule, or the screen is describing something the server will not do.
func TestDescribeRecurrenceRoundTrips(t *testing.T) {
	for _, said := range []string{
		"every day", "every 2 days", "every monday", "every monday and friday",
		"every weekday", "every 2 weeks", "monthly on the 1st", "every 3 months",
		"yearly",
	} {
		rule, err := query.ParseRecurrence(said)
		if err != nil {
			t.Fatalf("%q: %v", said, err)
		}
		again, err := query.ParseRecurrence(query.DescribeRecurrence(rule))
		if err != nil {
			t.Errorf("%q described as %q, which does not parse: %v",
				rule, query.DescribeRecurrence(rule), err)
			continue
		}
		if again != rule {
			t.Errorf("%q -> %q -> %q, which is a different rule",
				rule, query.DescribeRecurrence(rule), again)
		}
	}
}

// TestARuleItCannotDescribeComesBackVerbatim.
//
// A wrong description of when something repeats is worse than an unreadable
// correct one, because only one of the two makes you go and check. RRULE is a
// large standard and td's own parser emits a small corner of it; anything
// outside that corner is shown as itself.
func TestARuleItCannotDescribeComesBackVerbatim(t *testing.T) {
	for _, rule := range []string{
		"FREQ=WEEKLY;BYDAY=2MO",             // the second Monday, an ordinal BYDAY
		"FREQ=MONTHLY;BYSETPOS=-1;BYDAY=FR", // last Friday of the month
		"FREQ=DAILY;COUNT=10",               // bounded
		"FREQ=DAILY;UNTIL=20261231T000000Z", // bounded
		"FREQ=HOURLY",                       // a frequency td never makes
		"FREQ=WEEKLY;INTERVAL=0",            // nonsense interval
		"not a rule at all",
	} {
		if got := query.DescribeRecurrence(rule); got != rule {
			t.Errorf("%q was described as %q rather than passed through", rule, got)
		}
	}
	if got := query.DescribeRecurrence(""); got != "" {
		t.Errorf("empty rule described as %q", got)
	}
}
