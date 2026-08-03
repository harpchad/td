package query_test

import (
	"strings"
	"testing"
	"time"

	"github.com/harpchad/td/internal/query"
)

// Numeric dates. Month first, decided once and written down: 8/1/26 is
// August 1st here and 8 January in most of the world, and no amount of parsing
// can tell which was meant.

// TestNumericDatesAreMonthFirst covers the forms people actually type.
func TestNumericDatesAreMonthFirst(t *testing.T) {
	// A Monday, so the weekday cases elsewhere stay readable.
	now := time.Date(2026, 8, 3, 10, 30, 0, 0, chicago(t))

	for _, tc := range []struct{ in, want string }{
		{"8/1/26", "2026-08-01"},
		{"08/01/2026", "2026-08-01"},
		{"8/1/2026", "2026-08-01"},
		{"08-01-2026", "2026-08-01"},
		{"8-1-26", "2026-08-01"},
		{"12/25/26", "2026-12-25"},
		{"2026/08/01", "2026-08-01"},
		// Already supported, and still is.
		{"2026-08-01", "2026-08-01"},
	} {
		got, err := query.ResolveDate(tc.in, now)
		if err != nil {
			t.Errorf("%q: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%q -> %s, want %s", tc.in, got, tc.want)
		}
	}
}

// TestABareMonthAndDayRollsForward. "12/25" in January is this December and
// "1/5" in December is next January. The alternative puts a due date eleven
// months in the past, which is never what somebody meant.
func TestABareMonthAndDayRollsForward(t *testing.T) {
	loc := chicago(t)

	august := time.Date(2026, 8, 3, 10, 30, 0, 0, loc)
	for _, tc := range []struct{ in, want string }{
		{"12/25", "2026-12-25"}, // later this year
		{"8/3", "2026-08-03"},   // today counts, like a bare weekday
		{"1/5", "2027-01-05"},   // already past, so next year
		{"8-4", "2026-08-04"},
	} {
		got, err := query.ResolveDate(tc.in, august)
		if err != nil {
			t.Errorf("%q: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("from 2026-08-03, %q -> %s, want %s", tc.in, got, tc.want)
		}
	}

	// And the rollover works across a year boundary.
	december := time.Date(2026, 12, 20, 10, 0, 0, 0, loc)
	if got, err := query.ResolveDate("1/5", december); err != nil || got != "2027-01-05" {
		t.Errorf("from 2026-12-20, 1/5 -> %q, %v; want 2027-01-05", got, err)
	}
}

// TestADayFirstDateSaysWhyItFailed. Somebody typing 31/03/2026 does not have a
// typo, they have a different convention, and "unrecognized date" tells them
// nothing. This is the one failure mode the policy creates, so it gets the
// message that explains the policy.
func TestADayFirstDateSaysWhyItFailed(t *testing.T) {
	now := time.Date(2026, 8, 3, 10, 30, 0, 0, chicago(t))

	_, err := query.ResolveDate("31/03/2026", now)
	if err == nil {
		t.Fatal("31/03/2026 was accepted")
	}
	if !strings.Contains(err.Error(), "month first") {
		t.Errorf("the error does not explain the convention: %v", err)
	}
}

// TestAnImpossibleNumericDateIsRefused, rather than normalized into whatever
// month 13 rolls over to.
func TestAnImpossibleNumericDateIsRefused(t *testing.T) {
	now := time.Date(2026, 8, 3, 10, 30, 0, 0, chicago(t))

	for _, in := range []string{"13/1/26", "2/30/26", "0/1/26", "8/0/26", "99/99/99", "8//26", "-", "1/2/3/4"} {
		if got, err := query.ResolveDate(in, now); err == nil {
			t.Errorf("%q was accepted as %s", in, got)
		}
	}
}

// TestExistingDateWordsStillWork. The numeric branch sits after every keyword,
// so nothing it added can shadow one.
func TestExistingDateWordsStillWork(t *testing.T) {
	now := time.Date(2026, 8, 3, 10, 30, 0, 0, chicago(t))

	for _, tc := range []struct{ in, want string }{
		{"today", "2026-08-03"},
		{"tomorrow", "2026-08-04"},
		{"yesterday", "2026-08-02"},
		{"friday", "2026-08-07"},
		{"+2d", "2026-08-05"},
		{"-1w", "2026-07-27"},
	} {
		got, err := query.ResolveDate(tc.in, now)
		if err != nil {
			t.Errorf("%q: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%q -> %s, want %s", tc.in, got, tc.want)
		}
	}
}

func chicago(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}
