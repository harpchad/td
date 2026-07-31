package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/query"
)

// newID generates the ULID a capture carries. The client generates it so
// quick-add can return before the server answers, and so a queued capture
// flushed twice cannot create two rows.
func newID() string { return ulid.Make().String() }

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// formatRow renders one list line in the same column order the TUI uses:
// checkbox, number, priority, title, tags and people, due date.
func formatRow(t api.Task) string {
	var b strings.Builder
	b.WriteString(checkbox(t.Status))
	fmt.Fprintf(&b, " %-4d %-2s %s", t.Num, priorityLabel(t.Priority), t.Title)

	if tokens := tokensOf(t); tokens != "" {
		b.WriteString("  " + tokens)
	}
	if t.ChildrenTotal > 0 {
		fmt.Fprintf(&b, "  %d/%d", t.ChildrenDone, t.ChildrenTotal)
	}
	if due := dueLabel(t); due != "" {
		b.WriteString("  " + due)
	}
	return b.String()
}

// checkbox draws the task state. A terminal has no other option, which is why
// the web UI uses a native control rather than this glyph.
func checkbox(status string) string {
	switch status {
	case api.StatusDone:
		return "[x]"
	case api.StatusDropped:
		return "[-]"
	case api.StatusDoing, api.StatusWaiting:
		return "[~]"
	default:
		return "[ ]"
	}
}

// priorityLabel matches the p: token in the filter grammar so a row teaches
// the grammar. Unset renders blank.
func priorityLabel(p *int) string {
	if p == nil {
		return ""
	}
	return fmt.Sprintf("p%d", *p)
}

func tokensOf(t api.Task) string {
	parts := make([]string, 0, len(t.People)+len(t.Tags))
	for _, p := range t.People {
		parts = append(parts, "@"+strings.ToLower(strings.Fields(p.Name)[0]))
	}
	for _, tag := range t.Tags {
		parts = append(parts, "#"+tag)
	}
	return strings.Join(parts, " ")
}

func dueLabel(t api.Task) string {
	if t.DueAt == nil {
		return ""
	}
	d := query.LocalDate(*t.DueAt, time.Local)
	if d == "" {
		return ""
	}
	parsed, err := time.Parse(query.DateLayout, d)
	if err != nil {
		return d
	}
	today := time.Now().Format(query.DateLayout)
	switch {
	case d == today:
		return "Today"
	case d < today:
		return parsed.Format("Jan 2") + " !"
	default:
		return parsed.Format("Jan 2")
	}
}

func printDetail(t api.Task) {
	if t.ExternalURL != nil && *t.ExternalURL != "" {
		// A mirrored task puts its source on the first line, so one keystroke
		// opens the real thing.
		fmt.Println(*t.ExternalURL)
	}
	fmt.Printf("%s %d  %s\n", checkbox(t.Status), t.Num, t.Title)
	fmt.Printf("     status   %s\n", t.Status)
	if t.Priority != nil {
		fmt.Printf("     priority p%d\n", *t.Priority)
	}
	if t.DueAt != nil {
		fmt.Printf("     due      %s\n", *t.DueAt)
	}
	if t.StartAt != nil {
		fmt.Printf("     start    %s\n", *t.StartAt)
	}
	if t.SnoozeUntil != nil {
		fmt.Printf("     snoozed  until %s\n", *t.SnoozeUntil)
	}
	if len(t.Tags) > 0 {
		fmt.Printf("     tags     #%s\n", strings.Join(t.Tags, " #"))
	}
	for _, p := range t.People {
		fmt.Printf("     %-8s %s\n", p.Role, p.Name)
	}
	if t.ChildrenTotal > 0 {
		fmt.Printf("     subtasks %d/%d done\n", t.ChildrenDone, t.ChildrenTotal)
	}
	if t.Attachments > 0 {
		fmt.Printf("     files    %d\n", t.Attachments)
	}
	fmt.Printf("     notify   %s\n", t.Notify)
	fmt.Printf("     source   %s\n", t.Source)
	fmt.Printf("     id       %s\n", t.ID)
	if t.Notes != "" {
		fmt.Printf("\n%s\n", t.Notes)
	}
}
