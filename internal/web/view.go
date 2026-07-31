package web

import (
	"time"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/query"
)

// Token is a filter token rendered as a link, so clicking a tag or a person
// filters by it. The web pointer and the TUI pointer do the same thing.
type Token struct {
	Label string
	Query string
}

// Row is one list line, prepared for the template so the template contains
// no logic beyond choosing what to draw.
type Row struct {
	Task api.Task

	Sub         bool
	Done        bool
	HasChildren bool
	Collapsed   bool
	// Indeterminate marks doing and waiting. The native checkbox has a third
	// state and this is what it is for.
	Indeterminate bool

	PriorityClass string
	PriorityLabel string
	Due           string
	Overdue       bool

	Tags   []Token
	People []Token

	// OpensGroup and ClosesGroup wrap a parent and its children in the
	// .td-group element the fold CSS keys off.
	OpensGroup  bool
	ClosesGroup bool
}

// buildRows turns the sorted tasks into display order and prepares each one.
//
// Sort order and display order are not the same thing: the comparator orders
// parents and a subtask is lifted under its parent. That arrangement is
// query.Arrange, shared with the TUI, so the two clients cannot disagree
// about what the list looks like.
func buildRows(tasks []api.Task, collapsed map[string]bool, now time.Time) []Row {
	arranged := query.Arrange(tasks)
	out := make([]Row, 0, len(arranged))

	for i, r := range arranged {
		row := prepareRow(r.Task, r.Depth > 0, collapsed[r.Task.ID], now)

		// A parent opens a group; the group closes after the last child that
		// follows it.
		if !row.Sub && row.HasChildren && i+1 < len(arranged) && arranged[i+1].Depth > 0 {
			row.OpensGroup = true
		}
		if row.Sub && (i+1 >= len(arranged) || arranged[i+1].Depth == 0) {
			row.ClosesGroup = true
		}
		out = append(out, row)
	}
	return out
}

func prepareRow(t api.Task, sub, collapsed bool, now time.Time) Row {
	row := Row{
		Task:          t,
		Sub:           sub,
		Done:          t.Status == api.StatusDone,
		HasChildren:   t.ChildrenTotal > 0,
		Collapsed:     collapsed,
		Indeterminate: t.Status == api.StatusDoing || t.Status == api.StatusWaiting,
		PriorityClass: priorityClass(t.Priority),
		PriorityLabel: priorityLabel(t.Priority),
	}
	row.Due, row.Overdue = dueLabel(t, now)

	for _, tag := range t.Tags {
		row.Tags = append(row.Tags, Token{Label: "#" + tag, Query: "#" + tag})
	}
	for _, p := range t.People {
		handle := firstWordLower(p.Name)
		row.People = append(row.People, Token{Label: "@" + handle, Query: "@" + handle})
	}
	return row
}

// priorityClass maps onto the ramp in tokens.css. Priority is encoded in
// weight and value, never in hue: both color slots are already committed.
func priorityClass(p *int) string {
	if p == nil {
		return ""
	}
	switch *p {
	case 1:
		return "td-prio--1"
	case 2:
		return "td-prio--2"
	case 3:
		return "td-prio--3"
	case 4:
		return "td-prio--4"
	}
	return ""
}

// priorityLabel matches the p: token in the filter bar so the row teaches the
// grammar. Unset renders blank.
func priorityLabel(p *int) string {
	if p == nil {
		return ""
	}
	return "p" + itoa(int64(*p))
}

// dueLabel renders the due date against the server's clock, which is the same
// clock the sort order used.
func dueLabel(t api.Task, now time.Time) (label string, overdue bool) {
	if t.DueAt == nil {
		// The mockup renders an em dash rather than an empty cell, so the
		// column reads as present-and-empty rather than as a layout slip.
		return "—", false
	}
	d := query.LocalDate(*t.DueAt, now.Location())
	if d == "" {
		return "—", false
	}
	parsed, err := time.Parse(query.DateLayout, d)
	if err != nil {
		return d, false
	}
	today := now.Format(query.DateLayout)

	switch {
	case d < today:
		return parsed.Format("Jan 2"), true
	case d == today:
		return "Today", false
	default:
		return parsed.Format("Jan 2"), false
	}
}

// Counts is the top bar summary. The inbox count is the entire point of
// hiding the inbox from the home view: it nags without cluttering.
type Counts struct {
	Inbox   int
	Waiting int
	Overdue int
}

func countOf(tasks []api.Task, now time.Time) Counts {
	var c Counts
	today := now.Format(query.DateLayout)
	loc := now.Location()

	for _, t := range tasks {
		switch t.Status {
		case api.StatusInbox:
			c.Inbox++
		case api.StatusWaiting:
			c.Waiting++
		}
		if t.DueAt != nil && t.Status != api.StatusDone && t.Status != api.StatusDropped {
			if d := query.LocalDate(*t.DueAt, loc); d != "" && d < today {
				c.Overdue++
			}
		}
	}
	return c
}

func firstWordLower(name string) string {
	for i, r := range name {
		if r == ' ' {
			return lower(name[:i])
		}
	}
	return lower(name)
}

func lower(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 32
		}
	}
	return string(b)
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}
