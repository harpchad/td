package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/recur"
)

// NewID returns a fresh ULID. Clients generate their own so quick-add can
// return before the server answers and a retrying plugin cannot duplicate a
// row.
func NewID() string { return ulid.Make().String() }

// Create inserts a task and writes a task.created event. If a task with the
// supplied id already exists, the existing task is returned untouched, which
// is what makes POST /tasks idempotent under retry.
func (s *Store) Create(ctx context.Context, actor string, in api.TaskCreate, now time.Time) (api.Task, error) {
	if strings.TrimSpace(in.Title) == "" {
		return api.Task{}, &api.Error{Code: api.ErrBadRequest, Message: "a task needs a title"}
	}
	if in.ID == "" {
		in.ID = NewID()
	}
	if existing, err := s.Get(ctx, in.ID); err == nil {
		return existing, nil
	} else if !errors.Is(err, ErrNotFound) {
		return api.Task{}, err
	}

	due, dueIsDate, err := s.normalizeOptional(in.DueAt)
	if err != nil {
		return api.Task{}, &api.Error{Code: api.ErrBadRequest, Message: err.Error()}
	}
	start, _, err := s.normalizeOptional(in.StartAt)
	if err != nil {
		return api.Task{}, &api.Error{Code: api.ErrBadRequest, Message: err.Error()}
	}

	// Quick-add lands in the inbox. A priority or a due date given inline is
	// exactly the condition that lets a task leave it, so applying it at
	// creation saves a promote step.
	status := in.Status
	if status == "" {
		if in.Priority != nil || due != nil {
			status = api.StatusTodo
		} else {
			status = api.StatusInbox
		}
	}
	notify := in.Notify
	if notify == "" {
		notify = api.NotifyAuto
	}
	source := in.Source
	if source == "" {
		source = "local"
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return api.Task{}, err
	}
	defer tx.Rollback()

	if in.ParentID != nil {
		if err := checkParent(ctx, tx, *in.ParentID); err != nil {
			return api.Task{}, err
		}
	}

	var num int64
	if err := tx.QueryRowContext(ctx, `SELECT coalesce(max(num), 0) + 1 FROM task`).Scan(&num); err != nil {
		return api.Task{}, err
	}

	stamp := now.UTC().Format(time.RFC3339)
	_, err = tx.ExecContext(ctx, `INSERT INTO task
		(id, num, title, notes, status, priority, due_at, due_is_date, start_at,
		 notify, effort, parent_id, source, external_id, external_url, waiting_on,
		 created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		in.ID, num, in.Title, in.Notes, status, in.Priority, due, boolInt(dueIsDate), start,
		notify, in.Effort, in.ParentID, source, in.ExternalID, in.ExternalURL, in.WaitingOn,
		stamp, stamp)
	if err != nil {
		return api.Task{}, fmt.Errorf("insert task: %w", err)
	}

	tags := in.Tags
	if in.ParentID != nil && tags == nil {
		// A subtask inherits its parent's tags as a copy at creation, and
		// nothing else. Live inheritance would mean editing a parent silently
		// rewrites its children.
		tags, err = tagsOf(ctx, tx, *in.ParentID)
		if err != nil {
			return api.Task{}, err
		}
	}
	if err := setTags(ctx, tx, in.ID, tags); err != nil {
		return api.Task{}, err
	}
	if err := setPeople(ctx, tx, in.ID, in.People); err != nil {
		return api.Task{}, err
	}

	created, err := loadTaskTx(ctx, tx, in.ID)
	if err != nil {
		return api.Task{}, err
	}
	fields := map[string]api.Change{}
	for _, f := range mutableFields {
		if v := fieldValue(&created, f); v != nil {
			fields[f] = api.Change{From: nil, To: v}
		}
	}
	if err := appendEvent(ctx, tx, now, actor, in.ID, api.KindTaskCreated, api.Patch{Fields: fields}); err != nil {
		return api.Task{}, err
	}
	if err := tx.Commit(); err != nil {
		return api.Task{}, err
	}
	return s.Get(ctx, in.ID)
}

// Patch applies a partial update. ifMatch, when non-empty, is compared
// against updated_at so a slow TUI cannot clobber a web edit.
func (s *Store) Patch(ctx context.Context, actor, id string, p api.TaskPatch, ifMatch string, now time.Time) (api.Task, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return api.Task{}, err
	}
	defer tx.Rollback()

	before, err := loadTaskTx(ctx, tx, id)
	if err != nil {
		return api.Task{}, err
	}
	tagsBefore, err := tagsOf(ctx, tx, id)
	if err != nil {
		return api.Task{}, err
	}
	if ifMatch != "" && ifMatch != before.UpdatedAt {
		return api.Task{}, &api.Error{
			Code:    api.ErrConflict,
			Message: "the task changed underneath you, reload it",
		}
	}

	sets := map[string]any{}
	if p.Title != nil {
		sets["title"] = *p.Title
	}
	if p.Notes != nil {
		sets["notes"] = *p.Notes
	}
	if p.Presence["priority"] {
		sets["priority"] = intPtr(p.Priority)
	}
	if p.Presence["effort"] {
		sets["effort"] = intPtr(p.Effort)
	}
	if p.Presence["notify"] {
		if p.Notify == nil || (*p.Notify != api.NotifyAuto && *p.Notify != api.NotifyOn && *p.Notify != api.NotifyOff) {
			return api.Task{}, &api.Error{Code: api.ErrBadRequest, Message: "notify must be auto, on, or off"}
		}
		sets["notify"] = *p.Notify
	}
	if p.Presence["due_at"] {
		due, isDate, err := s.normalizeOptional(p.DueAt)
		if err != nil {
			return api.Task{}, &api.Error{Code: api.ErrBadRequest, Message: err.Error()}
		}
		sets["due_at"] = due
		sets["due_is_date"] = boolInt(isDate)
		// A new due value makes the task eligible for a reminder again.
		sets["notified_at"] = nil
	}
	if p.Presence["start_at"] {
		start, _, err := s.normalizeOptional(p.StartAt)
		if err != nil {
			return api.Task{}, &api.Error{Code: api.ErrBadRequest, Message: err.Error()}
		}
		sets["start_at"] = start
	}
	if p.Presence["snooze_until"] {
		if before.Status == api.StatusDone || before.Status == api.StatusDropped {
			return api.Task{}, &api.Error{
				Code: api.ErrConflict, From: before.Status, To: before.Status,
				Message: "a " + before.Status + " task cannot be snoozed",
			}
		}
		snooze, _, err := s.normalizeOptional(p.SnoozeUntil)
		if err != nil {
			return api.Task{}, &api.Error{Code: api.ErrBadRequest, Message: err.Error()}
		}
		sets["snooze_until"] = snooze
	}
	if p.Presence["waiting_on"] {
		sets["waiting_on"] = strPtr(p.WaitingOn)
	}

	kind := api.KindTaskUpdated
	if p.Status != nil && *p.Status != before.Status {
		waitingOn := before.WaitingOn
		if p.Presence["waiting_on"] {
			waitingOn = p.WaitingOn
		}
		effects, err := applyTransition(ctx, tx, &before, *p.Status, waitingOn, sets, now)
		if err != nil {
			return api.Task{}, err
		}
		for k, v := range effects {
			sets[k] = v
		}
		kind = statusEventKind(*p.Status)
	}

	stamp := now.UTC().Format(time.RFC3339)
	if len(sets) > 0 {
		cols := make([]string, 0, len(sets)+1)
		args := make([]any, 0, len(sets)+2)
		keys := make([]string, 0, len(sets))
		for k := range sets {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			cols = append(cols, k+" = ?")
			args = append(args, sets[k])
		}
		cols = append(cols, "updated_at = ?")
		args = append(args, stamp, id)
		if _, err := tx.ExecContext(ctx,
			`UPDATE task SET `+strings.Join(cols, ", ")+` WHERE id = ?`, args...); err != nil {
			return api.Task{}, err
		}
	}

	if p.Tags != nil {
		if err := setTags(ctx, tx, id, *p.Tags); err != nil {
			return api.Task{}, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE task SET updated_at = ? WHERE id = ?`, stamp, id); err != nil {
			return api.Task{}, err
		}
	}

	after, err := loadTaskTx(ctx, tx, id)
	if err != nil {
		return api.Task{}, err
	}
	fields := diff(&before, &after)

	if p.Tags != nil {
		tagsAfter, err := tagsOf(ctx, tx, id)
		if err != nil {
			return api.Task{}, err
		}
		// Tags live in another table, so the diff above cannot see them. They
		// go into the patch under a pseudo-field that undo knows to route
		// back through setTags rather than into an UPDATE.
		if strings.Join(sorted(tagsBefore), ",") != strings.Join(sorted(tagsAfter), ",") {
			fields[tagsField] = api.Change{From: tagsBefore, To: tagsAfter}
			if kind == api.KindTaskUpdated {
				kind = api.KindTaskTagged
			}
		}
	}

	if len(fields) == 0 {
		// Nothing changed, so there is no event to write. Commit first: the
		// pool is one connection, and calling Get with this transaction still
		// open waits forever for the connection the transaction is holding.
		if err := tx.Commit(); err != nil {
			return api.Task{}, err
		}
		return s.Get(ctx, id)
	}
	if err := appendEvent(ctx, tx, now, actor, id, kind, api.Patch{Fields: fields}); err != nil {
		return api.Task{}, err
	}
	if err := tx.Commit(); err != nil {
		return api.Task{}, err
	}
	if kind == api.KindTaskComplete {
		// After the commit, not inside it: generating the next instance runs
		// its own transactions, and the pool is one connection.
		if err := s.afterCompletion(ctx, actor, &after, now); err != nil {
			return api.Task{}, err
		}
	}
	return s.Get(ctx, id)
}

// afterCompletion generates the next instance of a series whose mode counts
// from completion. Fixed series generate nothing here: the scheduler does it
// when the rule fires, which is the whole difference between the two modes.
func (s *Store) afterCompletion(ctx context.Context, actor string, done *api.Task, now time.Time) error {
	if done.SeriesID == nil || *done.SeriesID == "" {
		return nil
	}
	series, err := s.Series(ctx, *done.SeriesID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	}
	if series.Mode != recur.ModeAfterCompletion {
		return nil
	}
	completedAt := now
	if done.CompletedAt != nil {
		if at, err := time.Parse(time.RFC3339, *done.CompletedAt); err == nil {
			completedAt = at
		}
	}
	_, err = s.NextAfterCompletion(ctx, actor, series, completedAt, now)
	return err
}

// Complete moves a task to done. It reports how many children are still open
// so the client can prompt; the server never cascades on its own.
func (s *Store) Complete(ctx context.Context, actor, id string, now time.Time) (api.CompleteResult, error) {
	status := api.StatusDone
	t, err := s.Patch(ctx, actor, id, api.TaskPatch{Status: &status}, "", now)
	if err != nil {
		return api.CompleteResult{}, err
	}
	var open int
	err = s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM task WHERE parent_id = ? AND status NOT IN ('done','dropped')`, id).Scan(&open)
	if err != nil {
		return api.CompleteResult{}, err
	}
	return api.CompleteResult{Task: t, ChildrenOpen: open}, nil
}

// Drop moves a task to dropped. There is no hard delete anywhere in td: the
// activity feed is supposed to show what you abandoned.
func (s *Store) Drop(ctx context.Context, actor, id string, now time.Time) (api.Task, error) {
	status := api.StatusDropped
	return s.Patch(ctx, actor, id, api.TaskPatch{Status: &status}, "", now)
}

// promotable reports whether a task will have a priority or a due date once
// this patch lands. A key present in pending overrides what the task has,
// including a nil value, which is how a field gets cleared.
func promotable(before *api.Task, pending map[string]any) bool {
	has := func(column string, current any) bool {
		if v, ok := pending[column]; ok {
			return !isNil(v)
		}
		return !isNil(current)
	}
	return has("priority", before.Priority) || has("due_at", before.DueAt)
}

// isNil covers an untyped nil and a nil pointer inside an interface, which is
// what a cleared field looks like once it is in a map[string]any.
func isNil(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case *int:
		return t == nil
	case *string:
		return t == nil
	default:
		return false
	}
}

// applyTransition validates a status move against the state machine and
// returns the extra column writes the move implies.
//
// pending is what the rest of this same patch is already writing. The fixture
// says the inbox transition requires "priority is set OR due_at is set", and
// the state that has to satisfy it is the state after the patch, not before
// it: setting a priority and promoting in one request is the ordinary move,
// and refusing it would make every client send two.
func applyTransition(ctx context.Context, tx *sql.Tx, before *api.Task, to string, waitingOn *string, pending map[string]any, now time.Time) (map[string]any, error) {
	tr, apiErr := lookupTransition(before.Status, to)
	if apiErr != nil {
		return nil, apiErr
	}

	if tr.needsPromotable && !promotable(before, pending) {
		return nil, &api.Error{
			Code:    api.ErrInboxIncomplete,
			Message: "set a priority or a due date before promoting",
		}
	}

	sets := map[string]any{"status": to}

	if tr.needsWaitingPerson {
		if waitingOn == nil || *waitingOn == "" {
			return nil, &api.Error{
				Code:    api.ErrWaitingNeedsPerson,
				Message: "say who you are waiting on",
			}
		}
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM person WHERE id = ?`, *waitingOn).Scan(&n); err != nil {
			return nil, err
		}
		if n == 0 {
			return nil, &api.Error{
				Code:    api.ErrWaitingNeedsPerson,
				Message: "no person with that id",
			}
		}
		sets["waiting_on"] = *waitingOn
	}
	if tr.setsWaitingSince {
		sets["waiting_since"] = now.UTC().Format(time.RFC3339)
	}
	for _, c := range tr.clears {
		sets[c] = nil
	}

	if to == api.StatusDone {
		// Completing stamps completed_at and clears a snooze. notified_at,
		// start_at, and notify are preserved.
		sets["completed_at"] = now.UTC().Format(time.RFC3339)
		sets["snooze_until"] = nil
	}
	return sets, nil
}

func statusEventKind(to string) string {
	switch to {
	case api.StatusDone:
		return api.KindTaskComplete
	case api.StatusDropped:
		return api.KindTaskDropped
	default:
		return api.KindTaskStatus
	}
}

// checkParent enforces one level of nesting. A task with a parent cannot
// itself be a parent: arbitrary nesting looks fine in a data model and is
// miserable in a flat list.
func checkParent(ctx context.Context, tx *sql.Tx, parentID string) error {
	var grandparent sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT parent_id FROM task WHERE id = ?`, parentID).Scan(&grandparent)
	if errors.Is(err, sql.ErrNoRows) {
		return &api.Error{Code: api.ErrNotFound, Message: "no parent task with that id"}
	}
	if err != nil {
		return err
	}
	if grandparent.Valid {
		return &api.Error{
			Code:    api.ErrNestingTooDeep,
			Message: "subtasks go one level deep, pick a top-level parent",
		}
	}
	return nil
}

func (s *Store) normalizeOptional(v *string) (*string, bool, error) {
	if v == nil || *v == "" {
		return nil, true, nil
	}
	out, isDate, err := normalizeDate(*v, s.loc)
	if err != nil {
		return nil, false, err
	}
	return &out, isDate, nil
}

// tagsField is the pseudo-field an event patch carries tag changes under. It
// is not a column, and reverseFields routes it through setTags.
const tagsField = "tags"

func sorted(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func tagsOf(ctx context.Context, tx *sql.Tx, taskID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT g.name FROM task_tag tt JOIN tag g ON g.id = tt.tag_id WHERE tt.task_id = ?`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// setTags replaces a task's tags, creating any tag row that does not exist
// yet. Tags are matched case-insensitively and stored in the case first used.
func setTags(ctx context.Context, tx *sql.Tx, taskID string, tags []string) error {
	if tags == nil {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM task_tag WHERE task_id = ?`, taskID); err != nil {
		return err
	}
	for _, name := range tags {
		name = strings.TrimPrefix(strings.TrimSpace(name), "#")
		if name == "" {
			continue
		}
		var tagID string
		err := tx.QueryRowContext(ctx, `SELECT id FROM tag WHERE lower(name) = lower(?)`, name).Scan(&tagID)
		if errors.Is(err, sql.ErrNoRows) {
			tagID = NewID()
			if _, err := tx.ExecContext(ctx, `INSERT INTO tag (id, name) VALUES (?, ?)`, tagID, name); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO task_tag (task_id, tag_id) VALUES (?, ?)`, taskID, tagID); err != nil {
			return err
		}
	}
	return nil
}

// setPeople links people by handle. An unknown handle is an error rather than
// an implicit person row: three Brandisses is exactly the failure
// person_identity exists to prevent.
func setPeople(ctx context.Context, tx *sql.Tx, taskID string, handles []string) error {
	for _, h := range handles {
		h = strings.TrimPrefix(strings.TrimSpace(h), "@")
		if h == "" {
			continue
		}
		handle, role, hasRole := strings.Cut(h, ":")
		if !hasRole {
			role = api.RoleInvolved
		}
		var personID string
		err := tx.QueryRowContext(ctx, `SELECT id FROM person WHERE lower(handle) = lower(?)`, handle).Scan(&personID)
		if errors.Is(err, sql.ErrNoRows) {
			return &api.Error{Code: api.ErrBadRequest, Message: "no person with handle @" + handle}
		}
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO task_person (task_id, person_id, role) VALUES (?, ?, ?)`,
			taskID, personID, role); err != nil {
			return err
		}
	}
	return nil
}

func loadTaskTx(ctx context.Context, tx *sql.Tx, id string) (api.Task, error) {
	row := tx.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM task t WHERE t.id = ?`, id)
	t, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return api.Task{}, ErrNotFound
	}
	return t, err
}
