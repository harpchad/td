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
		case f == "of" || f == "month" || f == "week" || f == "year" || f == "day" || f == "and":
			// Filler in "on the 1st of the month" and "monday and friday".
			// "and" earns its place twice over: people type it, and it is what
			// DescribeRecurrence emits, so without it a rule this package
			// produced could not be read back by the same package.
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

// DescribeRecurrence turns a stored RRULE back into the phrase somebody would
// say, for showing on a screen.
//
// The inverse of ParseRecurrence for every shape that function produces, and a
// best effort for anything else. RRULE is the right storage format and the
// wrong thing to put in front of a person: "FREQ=WEEKLY;INTERVAL=2;BYDAY=MO"
// is a fact about a standard, not an answer to "how often does this happen".
//
// A rule it cannot describe comes back verbatim rather than approximated. A
// wrong description of when something repeats is worse than an unreadable
// correct one, because only one of the two makes you go and check.
func DescribeRecurrence(rule string) string {
	raw := strings.TrimSpace(rule)
	if raw == "" {
		return ""
	}

	parts := map[string]string{}
	for _, field := range strings.Split(strings.TrimPrefix(strings.ToUpper(raw), "RRULE:"), ";") {
		key, value, ok := strings.Cut(field, "=")
		if !ok {
			return raw
		}
		parts[key] = value
	}

	freq := parts["FREQ"]
	if freq == "" {
		return raw
	}
	// Anything td's own parser cannot produce is passed through: describing
	// half a rule is how somebody ends up trusting the wrong half.
	for key := range parts {
		switch key {
		case "FREQ", "INTERVAL", "BYDAY", "BYMONTHDAY":
		default:
			return raw
		}
	}

	interval := 1
	if v := parts["INTERVAL"]; v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return raw
		}
		interval = n
	}

	unit, ok := map[string]string{
		"DAILY": "day", "WEEKLY": "week", "MONTHLY": "month", "YEARLY": "year",
	}[freq]
	if !ok {
		return raw
	}

	days, err := describeDays(parts["BYDAY"])
	if err != nil {
		return raw
	}
	months, err := describeMonthDays(parts["BYMONTHDAY"])
	if err != nil {
		return raw
	}

	// "every weekday" reads better than "every week on Monday, Tuesday, ...".
	if freq == "WEEKLY" && interval == 1 && parts["BYDAY"] == "MO,TU,WE,TH,FR" && months == "" {
		return "every weekday"
	}

	out := "every " + unit
	if interval > 1 {
		out = "every " + strconv.Itoa(interval) + " " + unit + "s"
	}
	// With named days the unit is redundant: "every Monday", not "every week
	// on Monday". It reappears when the interval makes it load-bearing.
	if days != "" {
		if interval == 1 && freq == "WEEKLY" {
			out = "every " + days
		} else {
			out += " on " + days
		}
	}
	if months != "" {
		out += " on the " + months
	}
	return out
}

// describeDays turns BYDAY into names. It reports an error rather than a guess
// on a code it does not know, including the ordinal forms like 2MO that
// ParseRecurrence never emits.
func describeDays(byDay string) (string, error) {
	if byDay == "" {
		return "", nil
	}
	names := map[string]string{
		"MO": "Monday", "TU": "Tuesday", "WE": "Wednesday", "TH": "Thursday",
		"FR": "Friday", "SA": "Saturday", "SU": "Sunday",
	}
	codes := strings.Split(byDay, ",")
	out := make([]string, 0, len(codes))
	for _, code := range codes {
		name, ok := names[code]
		if !ok {
			return "", fmt.Errorf("unknown weekday %q", code)
		}
		out = append(out, name)
	}
	return join(out), nil
}

// describeMonthDays turns BYMONTHDAY into ordinals: 1 becomes 1st.
func describeMonthDays(byMonthDay string) (string, error) {
	if byMonthDay == "" {
		return "", nil
	}
	fields := strings.Split(byMonthDay, ",")
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		n, err := strconv.Atoi(field)
		if err != nil || n < 1 || n > 31 {
			return "", fmt.Errorf("unusable day of month %q", field)
		}
		out = append(out, strconv.Itoa(n)+ordinalSuffix(n))
	}
	return join(out), nil
}

func ordinalSuffix(n int) string {
	if n%100 >= 11 && n%100 <= 13 {
		return "th"
	}
	switch n % 10 {
	case 1:
		return "st"
	case 2:
		return "nd"
	case 3:
		return "rd"
	}
	return "th"
}

// join reads like a sentence rather than a list: "Monday and Friday", not
// "Monday, Friday".
func join(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	case 2:
		return items[0] + " and " + items[1]
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}
