// Package recur turns an RFC 5545 rule into instants.
//
// It exists because two things in recurrence are easy to get subtly wrong and
// hard to notice: what a repeating time means across a daylight saving
// transition, and what "next month" means from the 31st. Both are pinned by
// testdata/recurrence_cases.json, whose values came from python-dateutil and
// IANA tzdata rather than from arithmetic on paper.
package recur

import (
	"fmt"
	"strings"
	"time"

	"github.com/teambition/rrule-go"
)

// Mode is how the next instance is chosen.
const (
	// ModeFixed takes the next occurrence from the rule regardless of when
	// the last one was completed.
	ModeFixed = "fixed"
	// ModeAfterCompletion takes completion time plus the rule's interval, so
	// completing early pulls the whole series earlier.
	ModeAfterCompletion = "after_completion"
)

// Catchup is what happens to occurrences that were missed.
const (
	// CatchupSkip rolls forward to the next occurrence and logs the misses.
	// Anything that piles up silently gets ignored, and then the whole list
	// gets ignored.
	CatchupSkip = "skip"
	// CatchupPile creates the backlog.
	CatchupPile = "pile"
)

// DateLayout is a calendar date with no zone.
const DateLayout = "2006-01-02"

// ResolveLocal turns a wall-clock time into an instant, handling the two
// daylight saving edges.
//
// A local time that does not exist because of a spring-forward gap shifts
// forward by the length of the gap: 02:30 on 2026-03-08 becomes 03:30 CDT.
// Go's time.Date normalizes such a time backwards instead, to 01:30 CST, so
// this cannot be left to the standard library.
//
// A local time that happens twice because of a fall-back overlap takes the
// first occurrence. Go's time.Date already does that, and the test pins it so
// a future change to that behavior is caught rather than absorbed.
func ResolveLocal(year int, month time.Month, day, hour, minute, second int, loc *time.Location) time.Time {
	out := time.Date(year, month, day, hour, minute, second, 0, loc)
	if out.Hour() == hour && out.Minute() == minute {
		return out
	}

	// The wall clock came back different, so the requested time is inside a
	// gap. The gap is the difference between the offsets either side of the
	// transition.
	_, before := time.Date(year, month, day, 0, 0, 0, 0, loc).Zone()
	_, after := time.Date(year, month, day, 23, 0, 0, 0, loc).Zone()
	return out.Add(time.Duration(after-before) * time.Second)
}

// atWallClock re-imposes a wall-clock time on an occurrence's date.
//
// This is what keeps a recurring instant at the same local time across a
// daylight saving change, and it is also what fixes the gap: rrule-go hands
// back 01:30 for a 02:30 rule on a spring-forward day, because it builds the
// result with time.Date. Taking the date it chose and the time of day that
// was asked for, then resolving that pair, gives both edges the behavior the
// fixture requires.
func atWallClock(occurrence, anchor time.Time, loc *time.Location) time.Time {
	y, m, d := occurrence.In(loc).Date()
	return ResolveLocal(y, m, d, anchor.Hour(), anchor.Minute(), anchor.Second(), loc)
}

// Rule is a parsed recurrence.
type Rule struct {
	Text     string
	Freq     rrule.Frequency
	Interval int
	loc      *time.Location
	dtstart  time.Time
}

// Parse reads an RFC 5545 RRULE anchored on dtstart.
func Parse(text string, dtstart time.Time, loc *time.Location) (*Rule, error) {
	if loc == nil {
		loc = time.UTC
	}
	opt, err := rrule.StrToROptionInLocation(strings.TrimPrefix(text, "RRULE:"), loc)
	if err != nil {
		return nil, fmt.Errorf("rrule %q: %w", text, err)
	}
	opt.Dtstart = dtstart.In(loc)

	interval := opt.Interval
	if interval < 1 {
		interval = 1
	}
	return &Rule{
		Text: text, Freq: opt.Freq, Interval: interval,
		loc: loc, dtstart: dtstart.In(loc),
	}, nil
}

// Occurrences returns the first n instants the rule produces, starting at
// dtstart.
func (r *Rule) Occurrences(n int) ([]time.Time, error) {
	if n <= 0 {
		return nil, nil
	}
	opt, err := rrule.StrToROptionInLocation(strings.TrimPrefix(r.Text, "RRULE:"), r.loc)
	if err != nil {
		return nil, err
	}
	opt.Dtstart = r.dtstart

	rule, err := rrule.NewRRule(*opt)
	if err != nil {
		return nil, err
	}

	raw := rule.All()
	if len(raw) > n {
		raw = raw[:n]
	}
	out := make([]time.Time, 0, len(raw))
	for _, at := range raw {
		out = append(out, atWallClock(at, r.dtstart, r.loc))
	}
	return out, nil
}

// Between returns the occurrences strictly after from, up to and including
// to. It is what the catch-up logic walks.
func (r *Rule) Between(from, to time.Time) ([]time.Time, error) {
	opt, err := rrule.StrToROptionInLocation(strings.TrimPrefix(r.Text, "RRULE:"), r.loc)
	if err != nil {
		return nil, err
	}
	opt.Dtstart = r.dtstart

	rule, err := rrule.NewRRule(*opt)
	if err != nil {
		return nil, err
	}

	var out []time.Time
	for _, at := range rule.Between(from, to, true) {
		resolved := atWallClock(at, r.dtstart, r.loc)
		if resolved.After(from) && !resolved.After(to) {
			out = append(out, resolved)
		}
	}
	return out, nil
}

// After returns the first occurrence strictly after t.
func (r *Rule) After(t time.Time) (time.Time, error) {
	opt, err := rrule.StrToROptionInLocation(strings.TrimPrefix(r.Text, "RRULE:"), r.loc)
	if err != nil {
		return time.Time{}, err
	}
	opt.Dtstart = r.dtstart

	rule, err := rrule.NewRRule(*opt)
	if err != nil {
		return time.Time{}, err
	}
	next := rule.After(t, false)
	if next.IsZero() {
		return time.Time{}, nil
	}
	return atWallClock(next, r.dtstart, r.loc), nil
}

// NextAfterCompletion is the after_completion mode: the next due is the
// completion time plus the rule's interval, and the previous due is ignored.
//
// Completing eleven days early pulls the whole series eleven days earlier.
// That is the intended behavior of this mode, not an accident of it.
func (r *Rule) NextAfterCompletion(completedAt time.Time) time.Time {
	at := completedAt.In(r.loc)

	switch r.Freq {
	case rrule.DAILY:
		return at.AddDate(0, 0, r.Interval)
	case rrule.WEEKLY:
		return at.AddDate(0, 0, 7*r.Interval)
	case rrule.MONTHLY:
		return addMonthsClamped(at, r.Interval)
	case rrule.YEARLY:
		return addMonthsClamped(at, 12*r.Interval)
	case rrule.HOURLY:
		return at.Add(time.Duration(r.Interval) * time.Hour)
	case rrule.MINUTELY:
		return at.Add(time.Duration(r.Interval) * time.Minute)
	default:
		return at.AddDate(0, 0, r.Interval)
	}
}

// addMonthsClamped adds months and clamps the day to the last of the target
// month, so 31 January plus one month is 28 February.
//
// This is deliberately different from the fixed mode, where RFC 5545 says
// BYMONTHDAY=31 skips a month with no 31st rather than clamping. The two
// modes answer different questions: a rule says which days match, and a
// completion-relative interval says how long to wait.
func addMonthsClamped(at time.Time, months int) time.Time {
	y, m, d := at.Date()
	loc := at.Location()

	total := int(m) - 1 + months
	y += total / 12
	mo := total % 12
	if mo < 0 {
		mo += 12
		y--
	}
	target := time.Month(mo + 1)

	last := time.Date(y, target, 1, 0, 0, 0, 0, loc).AddDate(0, 1, -1).Day()
	if d > last {
		d = last
	}
	return ResolveLocal(y, target, d, at.Hour(), at.Minute(), at.Second(), loc)
}
