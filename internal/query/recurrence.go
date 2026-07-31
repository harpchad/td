package query

import (
	"fmt"
	"strconv"
	"strings"
)

// ParseRecurrence turns what a person types into an RFC 5545 RRULE.
//
// The stored format is RRULE because inventing a recurrence syntax is how you
// end up unable to express "the last weekday of the month". But nobody types
// FREQ=WEEKLY;BYDAY=MO at a prompt, so this covers the shapes people actually
// say and passes anything that already looks like a rule straight through.
//
// It deliberately does not try to be clever. An input it does not recognize
// is an error naming what it did understand, rather than a guess that silently
// repeats the wrong thing for a year.
func ParseRecurrence(text string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(text))
	if s == "" {
		return "", fmt.Errorf("say how often: every day, every monday, every 2 weeks, monthly on the 1st")
	}

	// Already a rule. Uppercased so the stored form is canonical.
	if strings.HasPrefix(s, "rrule:") || strings.HasPrefix(s, "freq=") {
		return strings.ToUpper(strings.TrimPrefix(s, "rrule:")), nil
	}

	fields := strings.Fields(strings.NewReplacer(
		",", " ", "every ", "", "each ", "", "the ", "", "on ", "",
	).Replace(s))
	if len(fields) == 0 {
		return "", fmt.Errorf("say how often: every day, every monday, every 2 weeks")
	}

	// A leading count: "2 weeks", "3 days".
	interval := 1
	if n, err := strconv.Atoi(fields[0]); err == nil {
		if n < 1 {
			return "", fmt.Errorf("an interval of %d repeats nothing", n)
		}
		interval, fields = n, fields[1:]
	}
	if len(fields) == 0 {
		return "", fmt.Errorf("%q says how many but not of what", text)
	}

	var freq string
	var byDay []string
	var byMonthDay []string

	for _, f := range fields {
		switch {
		case isFreq(f) != "":
			freq = isFreq(f)
		case weekdayCode(f) != "":
			byDay = append(byDay, weekdayCode(f))
			if freq == "" {
				freq = "WEEKLY"
			}
		case f == "weekday" || f == "weekdays":
			byDay = append(byDay, "MO", "TU", "WE", "TH", "FR")
			if freq == "" {
				freq = "WEEKLY"
			}
		case ordinal(f) > 0:
			byMonthDay = append(byMonthDay, strconv.Itoa(ordinal(f)))
			if freq == "" {
				freq = "MONTHLY"
			}
		case f == "of" || f == "month" || f == "week" || f == "year" || f == "day":
			// Filler in "on the 1st of the month".
		default:
			return "", fmt.Errorf("cannot read %q in %q. Try: every day, every monday, every 2 weeks, monthly on the 1st, or an RRULE", f, text)
		}
	}
	if freq == "" {
		return "", fmt.Errorf("cannot tell how often from %q", text)
	}

	parts := []string{"FREQ=" + freq}
	if interval > 1 {
		parts = append(parts, "INTERVAL="+strconv.Itoa(interval))
	}
	if len(byDay) > 0 {
		parts = append(parts, "BYDAY="+strings.Join(byDay, ","))
	}
	if len(byMonthDay) > 0 {
		parts = append(parts, "BYMONTHDAY="+strings.Join(byMonthDay, ","))
	}
	return strings.Join(parts, ";"), nil
}

func isFreq(word string) string {
	switch word {
	case "day", "days", "daily":
		return "DAILY"
	case "week", "weeks", "weekly":
		return "WEEKLY"
	case "month", "months", "monthly":
		return "MONTHLY"
	case "year", "years", "yearly", "annually":
		return "YEARLY"
	case "hour", "hours", "hourly":
		return "HOURLY"
	}
	return ""
}

func weekdayCode(word string) string {
	switch word {
	case "mon", "monday", "mondays":
		return "MO"
	case "tue", "tues", "tuesday", "tuesdays":
		return "TU"
	case "wed", "weds", "wednesday", "wednesdays":
		return "WE"
	case "thu", "thur", "thurs", "thursday", "thursdays":
		return "TH"
	case "fri", "friday", "fridays":
		return "FR"
	case "sat", "saturday", "saturdays":
		return "SA"
	case "sun", "sunday", "sundays":
		return "SU"
	}
	return ""
}

// ordinal reads "1st", "15th", "31" as a day of the month. Zero means it is
// not one.
func ordinal(word string) int {
	trimmed := strings.TrimRight(word, "stndrh")
	n, err := strconv.Atoi(trimmed)
	if err != nil || n < 1 || n > 31 {
		return 0
	}
	return n
}
