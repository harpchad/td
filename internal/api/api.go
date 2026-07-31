// Package api holds the request and response types exchanged between the td
// client and the tdd server, plus the API version constant both sides use for
// the skew handshake. It links no database code and no UI code, so both
// binaries can depend on it.
package api

import "encoding/json"

// Version is the API version this build speaks. The client sends it in
// X-Td-Client and the server returns its own in X-Td-Server on every
// response. The client warns once when the major versions differ.
const Version = "1.0.0"

// Status values a task may hold. This list is closed; see
// testdata/transition_cases.json for the state machine that governs moves
// between them.
const (
	StatusInbox   = "inbox"
	StatusTodo    = "todo"
	StatusDoing   = "doing"
	StatusWaiting = "waiting"
	StatusDone    = "done"
	StatusDropped = "dropped"
)

// Notify tri-state values. auto resolves against the server-side policy rule,
// on and off override it for a single task.
const (
	NotifyAuto = "auto"
	NotifyOn   = "on"
	NotifyOff  = "off"
)

// Roles linking a person to a task. waiting is not stored in task_person; it
// is derived from task.waiting_on and appears here so filters and the person
// page can speak about it uniformly.
const (
	RoleAssigner = "assigner"
	RoleAssignee = "assignee"
	RoleInvolved = "involved"
	RoleWaiting  = "waiting"
)

// Task is the full representation of a task as the API returns it. Date
// fields are strings rather than time.Time because a due value is either an
// RFC3339 instant or a bare YYYY-MM-DD calendar date, and collapsing the two
// into one Go type loses the distinction that DueIsDate exists to carry.
type Task struct {
	ID       string `json:"id"`
	Num      int64  `json:"num"`
	Title    string `json:"title"`
	Notes    string `json:"notes"`
	Status   string `json:"status"`
	Priority *int   `json:"priority"`

	DueAt       *string `json:"due_at"`
	DueIsDate   bool    `json:"due_is_date"`
	StartAt     *string `json:"start_at"`
	SnoozeUntil *string `json:"snooze_until"`

	Notify       string  `json:"notify"`
	RemindBefore *int    `json:"remind_before"`
	NotifiedAt   *string `json:"notified_at"`

	WaitingOn    *string `json:"waiting_on"`
	WaitingSince *string `json:"waiting_since"`

	Effort   *int    `json:"effort"`
	ParentID *string `json:"parent_id"`
	SeriesID *string `json:"series_id"`

	Source       string  `json:"source"`
	ExternalID   *string `json:"external_id"`
	ExternalURL  *string `json:"external_url"`
	ExternalRev  *string `json:"external_rev"`
	UpstreamGone bool    `json:"upstream_gone"`

	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
	CompletedAt *string `json:"completed_at"`

	Tags   []string     `json:"tags"`
	People []TaskPerson `json:"people"`
	Groups []string     `json:"groups"`

	Attachments   int `json:"attachments"`
	ChildrenTotal int `json:"children_total"`
	ChildrenDone  int `json:"children_done"`
}

// TaskPerson is one person link on a task, carrying the role that link plays.
type TaskPerson struct {
	PersonID string `json:"person_id"`
	Name     string `json:"name"`
	Role     string `json:"role"`
}

// Person is a human the tasks refer to. Identities from sync plugins resolve
// onto one of these so a query spans systems.
type Person struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Handle string `json:"handle"`
	Email  string `json:"email,omitempty"`
	Notes  string `json:"notes,omitempty"`
}

// Group is static membership, not a saved search.
type Group struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Handle  string   `json:"handle"`
	Members []string `json:"members"`
}

// Event is one row of the append-only history. Undo, the activity feed, and
// the MCP change cursor all read this table.
type Event struct {
	Seq    int64  `json:"seq"`
	At     string `json:"at"`
	Actor  string `json:"actor"`
	TaskID string `json:"task_id,omitempty"`
	Kind   string `json:"kind"`
	Patch  Patch  `json:"patch"`
}

// Patch describes what one mutation changed. Fields carries the before and
// after value of every scalar field the mutation touched, which is what makes
// the event reversible.
type Patch struct {
	Fields map[string]Change `json:"fields,omitempty"`
	// UndoOf is set on events of kind undo and names the seq reversed.
	UndoOf int64 `json:"undo_of,omitempty"`
}

// Change is the before and after value of a single field.
type Change struct {
	From any `json:"from"`
	To   any `json:"to"`
}

// TaskCreate is the body of POST /tasks. ID is client-supplied so quick-add
// can return before the server answers and a retrying plugin cannot duplicate
// a row.
type TaskCreate struct {
	ID          string   `json:"id,omitempty"`
	Title       string   `json:"title"`
	Notes       string   `json:"notes,omitempty"`
	Status      string   `json:"status,omitempty"`
	Priority    *int     `json:"priority,omitempty"`
	DueAt       *string  `json:"due_at,omitempty"`
	StartAt     *string  `json:"start_at,omitempty"`
	Notify      string   `json:"notify,omitempty"`
	Effort      *int     `json:"effort,omitempty"`
	ParentID    *string  `json:"parent_id,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	People      []string `json:"people,omitempty"`
	WaitingOn   *string  `json:"waiting_on,omitempty"`
	Source      string   `json:"source,omitempty"`
	ExternalID  *string  `json:"external_id,omitempty"`
	ExternalURL *string  `json:"external_url,omitempty"`
}

// TaskPatch is the body of PATCH /tasks/{id}. Every field is a pointer so an
// absent key and an explicit null mean different things: absent leaves the
// field alone, null clears it.
type TaskPatch struct {
	Title       *string   `json:"title,omitempty"`
	Notes       *string   `json:"notes,omitempty"`
	Status      *string   `json:"status,omitempty"`
	Priority    *int      `json:"priority,omitempty"`
	DueAt       *string   `json:"due_at,omitempty"`
	StartAt     *string   `json:"start_at,omitempty"`
	SnoozeUntil *string   `json:"snooze_until,omitempty"`
	Notify      *string   `json:"notify,omitempty"`
	Effort      *int      `json:"effort,omitempty"`
	WaitingOn   *string   `json:"waiting_on,omitempty"`
	Tags        *[]string `json:"tags,omitempty"`

	// Presence records which keys were actually present in the request body,
	// so a null can be told apart from an omission.
	Presence map[string]bool `json:"-"`
}

// patchKeys are the request body keys TaskPatch tracks presence for. An
// absent key leaves a field alone; a key present with a null clears it, and
// the two cannot be told apart from the decoded struct alone.
var patchKeys = []string{
	"title", "notes", "status", "priority", "due_at", "start_at",
	"snooze_until", "notify", "effort", "waiting_on", "tags",
}

// UnmarshalJSON decodes a patch and records which keys the body actually
// carried, so `{"due_at": null}` clears the due date and `{}` does not.
func (p *TaskPatch) UnmarshalJSON(data []byte) error {
	type plain TaskPatch
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	var out plain
	if err := json.Unmarshal(data, &out); err != nil {
		return err
	}
	*p = TaskPatch(out)
	p.Presence = make(map[string]bool, len(raw))
	for _, k := range patchKeys {
		if _, ok := raw[k]; ok {
			p.Presence[k] = true
		}
	}
	return nil
}

// TaskList is the body of GET /tasks. There is no cursor: the task list does
// not paginate, and Total reports the untruncated count so a caller passing
// limit can tell a top N from the whole answer.
type TaskList struct {
	Tasks []Task `json:"tasks"`
	Total int    `json:"total"`
}

// CompleteResult is the body of POST /tasks/{id}/complete. ChildrenOpen is
// what the client uses to decide whether to prompt; the server never
// cascades on its own.
type CompleteResult struct {
	Task         Task `json:"task"`
	ChildrenOpen int  `json:"children_open"`
}

// UndoResult is the body of POST /undo.
type UndoResult struct {
	Reversed int64  `json:"reversed"`
	Kind     string `json:"kind"`
	TaskID   string `json:"task_id,omitempty"`
	Task     *Task  `json:"task,omitempty"`
}

// SavedFilter is a query bound to a number key, stored server-side so it
// follows the user between clients.
type SavedFilter struct {
	ID    string `json:"id"`
	Slot  int    `json:"slot"`
	Name  string `json:"name"`
	Query string `json:"query"`
}

// Error is the body every failing request returns. Message is written for a
// human and says what to do about it; Code is what a client branches on.
type Error struct {
	Code    string `json:"error"`
	Message string `json:"message,omitempty"`
	From    string `json:"from,omitempty"`
	To      string `json:"to,omitempty"`
}

// Error implements the error interface so handlers can return one directly.
func (e *Error) Error() string {
	if e.Message != "" {
		return e.Code + ": " + e.Message
	}
	return e.Code
}

// Error codes returned by the task routes.
const (
	ErrIllegalTransition  = "illegal_transition"
	ErrInboxIncomplete    = "inbox_incomplete"
	ErrWaitingNeedsPerson = "waiting_needs_person"
	ErrNotFound           = "not_found"
	ErrBadRequest         = "bad_request"
	ErrConflict           = "conflict"
	ErrNestingTooDeep     = "nesting_too_deep"
	ErrNothingToUndo      = "nothing_to_undo"
)

// Event kinds written to the log.
const (
	KindTaskCreated  = "task.created"
	KindTaskUpdated  = "task.updated"
	KindTaskStatus   = "task.status"
	KindTaskComplete = "task.completed"
	KindTaskDropped  = "task.dropped"
	KindTaskSnoozed  = "task.snoozed"
	KindTaskTagged   = "task.tagged"
	KindUndo         = "undo"
)
