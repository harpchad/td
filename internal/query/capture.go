package query

import (
	"strconv"
	"strings"
	"time"
)

// Capture is a quick-add line split into a title and the structured values
// that were written inline.
type Capture struct {
	Title    string
	Tags     []string
	People   []string
	Priority *int
	Due      *string
	Start    *string
}

// ParseCapture reads a quick-add line using the same tokens as the filter
// grammar, so `a "renew wildcard cert" #certs @stacey p:2 due:friday` needs no
// second syntax to learn.
//
// It is deliberately lenient where ParseAt is strict: anything the parser does
// not recognize stays in the title. A filter with a typo should say so, but a
// captured thought should never be refused, and `foo:bar` in a task title is
// far more likely to be part of the thought than a mistyped predicate.
func ParseCapture(line string, now time.Time) Capture {
	var c Capture
	var title []string

	for _, field := range strings.Fields(line) {
		if consumeCaptureToken(&c, field, now) {
			continue
		}
		title = append(title, field)
	}

	c.Title = strings.TrimSpace(strings.Join(title, " "))
	c.Title = strings.Trim(c.Title, `"`)
	return c
}

func consumeCaptureToken(c *Capture, field string, now time.Time) bool {
	switch {
	case strings.HasPrefix(field, "#") && len(field) > 1:
		c.Tags = append(c.Tags, strings.ToLower(field[1:]))
		return true

	case strings.HasPrefix(field, "@") && len(field) > 1:
		handle, role, hasRole := strings.Cut(strings.ToLower(field[1:]), ":")
		if hasRole && !contains(knownRoles, role) {
			return false
		}
		c.People = append(c.People, strings.TrimSuffix(handle+":"+role, ":"))
		return true
	}

	key, value, hasColon := strings.Cut(strings.ToLower(field), ":")
	if !hasColon || value == "" {
		return false
	}

	switch key {
	case "p":
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > 4 {
			return false
		}
		c.Priority = &n
		return true
	case "due", "start":
		resolved, err := ResolveDate(value, now)
		if err != nil {
			return false
		}
		if key == "due" {
			c.Due = &resolved
		} else {
			c.Start = &resolved
		}
		return true
	}
	return false
}
