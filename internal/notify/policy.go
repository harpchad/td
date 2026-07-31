// Package notify decides when a reminder fires and sends it to ntfy. It is
// server-only: the TUI and the web UI never send anything, so reminders work
// when nothing is running.
package notify

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/query"
)

// Policy is the [notify] block of config.toml.
//
// The default rule is a filter query rather than a screen full of dropdowns,
// because section 6 already gave the product a grammar and one expression
// covers every case: "*" for always on, "" for always off, "p:<=2 -#someday"
// for anything between.
type Policy struct {
	// Topic is the full ntfy URL. Empty turns reminders off entirely, which
	// is the default: a server that has not been told where to push does not
	// guess.
	Topic string `toml:"topic"`
	// DefaultRule resolves notify=auto.
	DefaultRule string `toml:"default_rule"`
	// LeadMinutes is how far before a datetime due the push fires.
	LeadMinutes int `toml:"lead_minutes"`
	// QuietHours holds a push until the window opens, as "22:00-06:00".
	QuietHours string `toml:"quiet_hours"`
	// DateOnlyAt is the wall time a date-only due fires on its day.
	DateOnlyAt string `toml:"date_only_at"`
	// ActionToken authenticates the Done and Snooze buttons. Each client gets
	// its own token; this is the notification's. Empty leaves the push a
	// click-through rather than buttons that cannot authenticate.
	ActionToken string `toml:"action_token"`
}

// DefaultPolicy is what a config file with no [notify] block behaves as.
// Topic empty means nothing is sent.
var DefaultPolicy = Policy{
	DefaultRule: "p:<=2",
	LeadMinutes: 30,
	QuietHours:  "22:00-06:00",
	DateOnlyAt:  "08:00",
}

// RuleAlways and RuleNever are the two literals the default rule takes
// instead of a filter expression.
const (
	RuleAlways = "*"
	RuleNever  = ""
)

// Validate checks the policy and reports what is wrong in a sentence.
func (p Policy) Validate() error {
	if p.LeadMinutes < 0 {
		return errors.New("notify.lead_minutes cannot be negative")
	}
	if _, err := ParseClockTime(p.DateOnlyAt); p.DateOnlyAt != "" && err != nil {
		return fmt.Errorf("notify.date_only_at: %w", err)
	}
	if _, err := ParseQuietHours(p.QuietHours); err != nil {
		return fmt.Errorf("notify.quiet_hours: %w", err)
	}
	switch p.DefaultRule {
	case RuleAlways, RuleNever:
	default:
		if _, err := query.Parse(p.DefaultRule); err != nil {
			return fmt.Errorf("notify.default_rule: %w", err)
		}
	}
	return nil
}

// Enabled reports whether anything will be sent.
func (p Policy) Enabled() bool { return strings.TrimSpace(p.Topic) != "" }

// ClockTime is a wall time with no date, as "08:00".
type ClockTime struct{ Hour, Minute int }

// ParseClockTime reads "HH:MM".
func ParseClockTime(s string) (ClockTime, error) {
	s = strings.TrimSpace(s)
	hh, mm, ok := strings.Cut(s, ":")
	if !ok {
		return ClockTime{}, fmt.Errorf("%q is not a HH:MM time", s)
	}
	hour, err := strconv.Atoi(hh)
	if err != nil || hour < 0 || hour > 23 {
		return ClockTime{}, fmt.Errorf("%q is not a HH:MM time", s)
	}
	minute, err := strconv.Atoi(mm)
	if err != nil || minute < 0 || minute > 59 {
		return ClockTime{}, fmt.Errorf("%q is not a HH:MM time", s)
	}
	return ClockTime{Hour: hour, Minute: minute}, nil
}

// On returns this wall time on the given day, in that day's location.
func (c ClockTime) On(day time.Time) time.Time {
	y, m, d := day.Date()
	return time.Date(y, m, d, c.Hour, c.Minute, 0, 0, day.Location())
}

// QuietWindow holds a push until it opens. It may wrap midnight, which is the
// normal case: 22:00 to 06:00.
type QuietWindow struct {
	Start, End ClockTime
	set        bool
}

// ParseQuietHours reads "22:00-06:00". An empty string is no quiet hours.
func ParseQuietHours(s string) (QuietWindow, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return QuietWindow{}, nil
	}
	from, to, ok := strings.Cut(s, "-")
	if !ok {
		return QuietWindow{}, fmt.Errorf("%q is not a HH:MM-HH:MM range", s)
	}
	start, err := ParseClockTime(from)
	if err != nil {
		return QuietWindow{}, err
	}
	end, err := ParseClockTime(to)
	if err != nil {
		return QuietWindow{}, err
	}
	return QuietWindow{Start: start, End: end, set: true}, nil
}

// Contains reports whether an instant falls inside the quiet window.
func (q QuietWindow) Contains(at time.Time) bool {
	if !q.set {
		return false
	}
	minutes := at.Hour()*60 + at.Minute()
	start := q.Start.Hour*60 + q.Start.Minute
	end := q.End.Hour*60 + q.End.Minute

	if start == end {
		return false
	}
	if start < end {
		return minutes >= start && minutes < end
	}
	// Wraps midnight.
	return minutes >= start || minutes < end
}

// Release returns when a push held by the window may go out. Quiet hours hold
// a push, they do not drop it.
func (q QuietWindow) Release(at time.Time) time.Time {
	if !q.Contains(at) {
		return at
	}
	opens := q.End.On(at)
	if !opens.After(at) {
		// The window wrapped midnight and we are on the late side of it, so
		// it opens tomorrow morning.
		opens = q.End.On(at.AddDate(0, 0, 1))
	}
	return opens
}

// FireAt returns the instant a task's reminder is due to go out, or false
// when the task has no due date to fire on.
//
// A date-only due fires at DateOnlyAt on that day, because "pay the mortgage
// on the 1st" is a date rather than an instant. A datetime due fires
// LeadMinutes before it.
func (p Policy) FireAt(t api.Task, loc *time.Location) (time.Time, bool) {
	if t.DueAt == nil || *t.DueAt == "" {
		return time.Time{}, false
	}

	if t.DueIsDate {
		day, err := time.ParseInLocation(query.DateLayout, *t.DueAt, loc)
		if err != nil {
			return time.Time{}, false
		}
		at, err := ParseClockTime(p.DateOnlyAt)
		if err != nil {
			at = ClockTime{Hour: 8}
		}
		return at.On(day), true
	}

	due, err := time.Parse(time.RFC3339, *t.DueAt)
	if err != nil {
		return time.Time{}, false
	}
	return due.In(loc).Add(-time.Duration(p.LeadMinutes) * time.Minute), true
}

// Resolve decides whether a task wants a reminder at all.
//
// The tri-state is not a checkbox because a checkbox cannot express "whatever
// the default says". on and off are the task's own answer; auto defers to the
// policy, and matchesRule is whether the default rule selected this task.
func (p Policy) Resolve(t api.Task, matchesRule bool) bool {
	switch t.Notify {
	case api.NotifyOn:
		return true
	case api.NotifyOff:
		return false
	default:
		return matchesRule
	}
}

// RuleQuery returns the filter to evaluate for notify=auto, and whether it
// needs evaluating at all. The two literals short-circuit.
func (p Policy) RuleQuery() (filter string, always, never bool) {
	switch strings.TrimSpace(p.DefaultRule) {
	case RuleAlways:
		return "", true, false
	case RuleNever:
		return "", false, true
	default:
		return p.DefaultRule, false, false
	}
}
