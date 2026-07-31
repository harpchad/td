// Package query holds the filter grammar and the default sort comparator.
// Both the client and the server import it: the client parses a filter string
// locally to report syntax errors before sending anything, and the server
// parses the same string to build SQL. One grammar, one comparator, no drift.
package query

import "encoding/json"

// Node is one node of a parsed filter expression.
type Node interface {
	node()
	json.Marshaler
}

// And matches when every child matches. Whitespace between terms builds one
// of these; it binds tighter than Or and looser than negation.
type And struct{ Nodes []Node }

// Or matches when any child matches. The `|` operator builds one of these.
type Or struct{ Nodes []Node }

// Not inverts its child. `-` binds tightest of the three operators.
type Not struct{ Node Node }

// Is is a status or view predicate: is:open, is:done, is:waiting, is:inbox,
// is:todo, is:doing, is:dropped, is:orphan, is:snoozed, is:deferred.
type Is struct{ Value string }

// Tag matches a task carrying the named tag.
type Tag struct{ Name string }

// Person matches a task linked to the named person. Role is nil when the
// handle was given bare, in which case every role matches, including the
// waiting_on link.
type Person struct {
	Handle string
	Role   *string
}

// Priority compares a task's priority. A task with no priority never matches,
// whatever the operator.
type Priority struct {
	Op    string
	Value int
}

// Date compares due_at or start_at against a resolved date. Special carries
// `none` or `overdue` when the value was one of those keywords rather than a
// date, and Value is empty in that case.
type Date struct {
	Field   string // "due" or "start"
	Op      string // "=", "<=", ">=", "<", ">"
	Value   string // YYYY-MM-DD, empty when Special is set
	Special string // "none", "overdue", or empty
}

// Src matches the system a task came from: local, jira, monday, planner.
type Src struct{ Name string }

// Has matches structural facts: attachment, notes, sub.
type Has struct{ What string }

// Notify matches the tri-state on a task: auto, on, off.
type Notify struct{ Mode string }

// Grp matches a direct task-to-group link or any task involving a member of
// that group.
type Grp struct{ Name string }

// Phrase is a quoted string, matched as an FTS5 phrase over title and notes.
type Phrase struct{ Text string }

// Word is bare free text, matched as an FTS5 prefix term over title and
// notes, so `cert` finds `certs`.
type Word struct{ Text string }

func (*And) node()      {}
func (*Or) node()       {}
func (*Not) node()      {}
func (*Is) node()       {}
func (*Tag) node()      {}
func (*Person) node()   {}
func (*Priority) node() {}
func (*Date) node()     {}
func (*Src) node()      {}
func (*Has) node()      {}
func (*Notify) node()   {}
func (*Grp) node()      {}
func (*Phrase) node()   {}
func (*Word) node()     {}

// MarshalJSON renders the node in the shape testdata/filter_cases.json uses
// for its ast cases.
func (n *And) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"and": n.Nodes})
}

// MarshalJSON renders the node in the shape testdata/filter_cases.json uses
// for its ast cases.
func (n *Or) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"or": n.Nodes})
}

// MarshalJSON renders the node in the shape testdata/filter_cases.json uses
// for its ast cases.
func (n *Not) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"not": n.Node})
}

// MarshalJSON renders the node in the shape testdata/filter_cases.json uses
// for its ast cases.
func (n *Is) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"is": n.Value})
}

// MarshalJSON renders the node in the shape testdata/filter_cases.json uses
// for its ast cases.
func (n *Tag) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"tag": n.Name})
}

// MarshalJSON renders the node in the shape testdata/filter_cases.json uses
// for its ast cases. Role is always present and is null for a bare handle.
func (n *Person) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"person": n.Handle, "role": n.Role})
}

// MarshalJSON renders the node in the shape testdata/filter_cases.json uses
// for its ast cases.
func (n *Priority) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{
		"priority": map[string]any{"op": n.Op, "value": n.Value},
	})
}

// MarshalJSON renders the node with the comparison spelled out under the
// field name, so due and start read the same way.
func (n *Date) MarshalJSON() ([]byte, error) {
	body := map[string]any{"op": n.Op}
	if n.Special != "" {
		body["value"] = n.Special
	} else {
		body["value"] = n.Value
	}
	return json.Marshal(map[string]any{n.Field: body})
}

// MarshalJSON renders the node in the shape testdata/filter_cases.json uses
// for its ast cases.
func (n *Src) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"src": n.Name})
}

// MarshalJSON renders the node in the shape testdata/filter_cases.json uses
// for its ast cases.
func (n *Has) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"has": n.What})
}

// MarshalJSON renders the node in the shape testdata/filter_cases.json uses
// for its ast cases.
func (n *Notify) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"notify": n.Mode})
}

// MarshalJSON renders the node in the shape testdata/filter_cases.json uses
// for its ast cases.
func (n *Grp) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"grp": n.Name})
}

// MarshalJSON renders the node in the shape testdata/filter_cases.json uses
// for its ast cases.
func (n *Phrase) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"phrase": n.Text})
}

// MarshalJSON renders the node in the shape testdata/filter_cases.json uses
// for its ast cases.
func (n *Word) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"word": n.Text})
}
