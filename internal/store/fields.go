package store

import (
	"fmt"
	"time"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/query"
)

// mutableFields are the task columns a mutation may change and an undo may
// restore. The list is closed on purpose: a column absent from it is either
// immutable (id, num, created_at) or derived (updated_at), and neither
// belongs in a patch.
var mutableFields = []string{
	"title", "notes", "status", "priority",
	"due_at", "due_is_date", "start_at", "snooze_until",
	"notify", "remind_before", "notified_at",
	"waiting_on", "waiting_since", "effort", "parent_id",
	"source", "external_id", "external_url", "external_rev", "upstream_gone",
	"completed_at",
}

// intFields carry SQLite INTEGER values. JSON decoding turns every number
// into a float64, so an undo has to coerce them back before writing.
var intFields = map[string]bool{
	"priority": true, "due_is_date": true, "remind_before": true,
	"effort": true, "upstream_gone": true,
}

// fieldValue reads one mutable field off a task as a plain JSON-shaped value.
func fieldValue(t *api.Task, name string) any {
	switch name {
	case "title":
		return t.Title
	case "notes":
		return t.Notes
	case "status":
		return t.Status
	case "priority":
		return intPtr(t.Priority)
	case "due_at":
		return strPtr(t.DueAt)
	case "due_is_date":
		return boolInt(t.DueIsDate)
	case "start_at":
		return strPtr(t.StartAt)
	case "snooze_until":
		return strPtr(t.SnoozeUntil)
	case "notify":
		return t.Notify
	case "remind_before":
		return intPtr(t.RemindBefore)
	case "notified_at":
		return strPtr(t.NotifiedAt)
	case "waiting_on":
		return strPtr(t.WaitingOn)
	case "waiting_since":
		return strPtr(t.WaitingSince)
	case "effort":
		return intPtr(t.Effort)
	case "parent_id":
		return strPtr(t.ParentID)
	case "source":
		return t.Source
	case "external_id":
		return strPtr(t.ExternalID)
	case "external_url":
		return strPtr(t.ExternalURL)
	case "external_rev":
		return strPtr(t.ExternalRev)
	case "upstream_gone":
		return boolInt(t.UpstreamGone)
	case "completed_at":
		return strPtr(t.CompletedAt)
	}
	return nil
}

func strPtr(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

func intPtr(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

func boolInt(b bool) any {
	if b {
		return 1
	}
	return 0
}

// bindValue coerces a value read back out of an event patch into something
// SQLite will store in the named column.
func bindValue(field string, v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	if !intFields[field] {
		if s, ok := v.(string); ok {
			return s, nil
		}
		return fmt.Sprintf("%v", v), nil
	}
	switch n := v.(type) {
	case int:
		return int64(n), nil
	case int64:
		return n, nil
	case float64:
		return int64(n), nil
	case bool:
		if n {
			return int64(1), nil
		}
		return int64(0), nil
	}
	return nil, fmt.Errorf("field %s: cannot bind %T", field, v)
}

// diff records every mutable field that differs between two versions of a
// task. It is what makes an event reversible: undo writes the from side back.
func diff(before, after *api.Task) map[string]api.Change {
	out := map[string]api.Change{}
	for _, f := range mutableFields {
		b, a := fieldValue(before, f), fieldValue(after, f)
		if !sameValue(b, a) {
			out[f] = api.Change{From: b, To: a}
		}
	}
	return out
}

func sameValue(a, b any) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
}

// normalizeDate canonicalizes a due, start, or snooze value. A bare
// YYYY-MM-DD stays as it is, because a calendar date has no zone to convert.
// Anything else is parsed as an instant and stored as UTC RFC3339, so string
// comparison in SQL orders instants correctly.
//
// The second return reports whether the value is date-only.
func normalizeDate(v string, loc *time.Location) (string, bool, error) {
	if v == "" {
		return "", true, nil
	}
	if len(v) == len(query.DateLayout) {
		if _, err := time.Parse(query.DateLayout, v); err == nil {
			return v, true, nil
		}
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t.UTC().Format(time.RFC3339), false, nil
	}
	// Accept a local datetime without a zone, which is what a TUI date picker
	// produces, and interpret it in the configured timezone.
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02T15:04", "2006-01-02 15:04"} {
		if t, err := time.ParseInLocation(layout, v, loc); err == nil {
			return t.UTC().Format(time.RFC3339), false, nil
		}
	}
	return "", false, fmt.Errorf("unrecognized date %q", v)
}
