package query

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// DateLayout is the wire and storage format for a calendar date with no zone.
const DateLayout = "2006-01-02"

// weekdayNames covers the three-letter forms the fixture pins and the full
// names the spec writes in its own examples (`due:friday`). Both spell the
// same thing and refusing one of them would be a papercut with no upside.
var weekdayNames = map[string]time.Weekday{
	"sun": time.Sunday, "sunday": time.Sunday,
	"mon": time.Monday, "monday": time.Monday,
	"tue": time.Tuesday, "tues": time.Tuesday, "tuesday": time.Tuesday,
	"wed": time.Wednesday, "weds": time.Wednesday, "wednesday": time.Wednesday,
	"thu": time.Thursday, "thur": time.Thursday, "thurs": time.Thursday, "thursday": time.Thursday,
	"fri": time.Friday, "friday": time.Friday,
	"sat": time.Saturday, "saturday": time.Saturday,
}

// ResolveDate turns a date keyword from the filter grammar into a calendar
// date in now's location, formatted YYYY-MM-DD.
//
// The vocabulary is today, tomorrow, yesterday, the three-letter weekday
// names, eow, eom, +Nd / +Nw / +Nm relative offsets, and an absolute
// YYYY-MM-DD. Resolution happens in now's location rather than UTC: at
// 23:30 Central the server clock is already tomorrow in UTC and "today" is
// still today.
func ResolveDate(tok string, now time.Time) (string, error) {
	tok = strings.ToLower(strings.TrimSpace(tok))
	y, m, d := now.Date()
	loc := now.Location()
	base := time.Date(y, m, d, 0, 0, 0, 0, loc)

	switch tok {
	case "today":
		return base.Format(DateLayout), nil
	case "tomorrow":
		return base.AddDate(0, 0, 1).Format(DateLayout), nil
	case "yesterday":
		return base.AddDate(0, 0, -1).Format(DateLayout), nil
	case "eow":
		// The week starts Monday, so end of week is the following Sunday.
		offset := (int(base.Weekday()) + 6) % 7
		monday := base.AddDate(0, 0, -offset)
		return monday.AddDate(0, 0, 6).Format(DateLayout), nil
	case "eom":
		firstNext := time.Date(y, m, 1, 0, 0, 0, 0, loc).AddDate(0, 1, 0)
		return firstNext.AddDate(0, 0, -1).Format(DateLayout), nil
	}

	// A bare weekday name means its next occurrence strictly after today,
	// except when today already is that weekday, in which case it means
	// today. Rolling forward a full week here is the common wrong answer.
	if wd, ok := weekdayNames[tok]; ok {
		delta := (int(wd) - int(base.Weekday()) + 7) % 7
		return base.AddDate(0, 0, delta).Format(DateLayout), nil
	}

	if strings.HasPrefix(tok, "+") || strings.HasPrefix(tok, "-") {
		if out, ok := resolveOffset(tok, base); ok {
			return out, nil
		}
	}

	if len(tok) == 10 && tok[4] == '-' && tok[7] == '-' {
		if t, err := time.ParseInLocation(DateLayout, tok, loc); err == nil {
			return t.Format(DateLayout), nil
		}
	}

	return "", fmt.Errorf("unrecognized date %q", tok)
}

// resolveOffset handles +Nd, +Nw, and +Nm, and their negative forms. Month
// arithmetic is calendar arithmetic with clamping rather than a 30 day
// addition: one month from 31 January is 28 February.
func resolveOffset(tok string, base time.Time) (string, bool) {
	if len(tok) < 3 {
		return "", false
	}
	sign := 1
	if tok[0] == '-' {
		sign = -1
	}
	unit := tok[len(tok)-1]
	n, err := strconv.Atoi(tok[1 : len(tok)-1])
	if err != nil || n < 0 {
		return "", false
	}
	n *= sign

	switch unit {
	case 'd':
		return base.AddDate(0, 0, n).Format(DateLayout), true
	case 'w':
		return base.AddDate(0, 0, 7*n).Format(DateLayout), true
	case 'm':
		return addMonthsClamped(base, n).Format(DateLayout), true
	}
	return "", false
}

// addMonthsClamped adds n months and clamps the day to the last day of the
// target month. Go's AddDate normalizes instead, turning 31 January plus one
// month into 3 March.
func addMonthsClamped(base time.Time, n int) time.Time {
	y, m, d := base.Date()
	loc := base.Location()

	total := int(m) - 1 + n
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
	return time.Date(y, target, d, 0, 0, 0, 0, loc)
}
