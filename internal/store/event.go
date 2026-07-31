package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/harpchad/td/internal/api"
)

// undoableKinds are the event kinds an undo may reverse. Auth events, sync
// writes from a plugin, attachment uploads, and a previous undo are all
// outside it.
// Nothing with the auth. prefix appears here: reversing a login is not a
// thing, and the log exists so something odd can be found later.
var undoableKinds = map[string]bool{
	api.KindTaskCreated:  true,
	api.KindTaskUpdated:  true,
	api.KindTaskStatus:   true,
	api.KindTaskComplete: true,
	api.KindTaskDropped:  true,
	api.KindTaskTagged:   true,
	api.KindTaskSnoozed:  true,
}

func appendEvent(ctx context.Context, tx *sql.Tx, now time.Time, actor, taskID, kind string, patch api.Patch) error {
	body, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO event (at, actor, task_id, kind, patch_json) VALUES (?,?,?,?,?)`,
		now.UTC().Format(time.RFC3339), actor, nullIfEmpty(taskID), kind, string(body))
	return err
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Events returns the change feed from seq onwards, oldest first. MCP clients
// poll this instead of re-listing everything.
//
// Auth events are excluded. They live in the same table, because section 15
// says to log them there and because one ordered history is worth having, but
// they are not changes to anything and the feed is read by agents. Handing a
// read-scoped MCP token your login history and the source IPs behind it would
// be a needless disclosure to the least trusted credential in the system.
// AuthEvents reads them, and `tdd account log` is what prints them.
func (s *Store) Events(ctx context.Context, since int64, limit int) ([]api.Event, error) {
	return s.events(ctx, `seq > ? AND kind NOT LIKE 'auth.%'`, since, limit)
}

// AuthEvents returns the authentication history, newest first. It is not
// served over HTTP in phase 2: `tdd account log` reads it on the server, the
// same place the account is created.
func (s *Store) AuthEvents(ctx context.Context, limit int) ([]api.Event, error) {
	if limit <= 0 || limit > 1000 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT seq, at, actor, coalesce(task_id, ''), kind, patch_json
		 FROM event WHERE kind LIKE 'auth.%' ORDER BY seq DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []api.Event{}
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) events(ctx context.Context, where string, since int64, limit int) ([]api.Event, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT seq, at, actor, coalesce(task_id, ''), kind, patch_json
		 FROM event WHERE `+where+` ORDER BY seq LIMIT ?`, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []api.Event{}
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func scanEvent(sc rowScanner) (api.Event, error) {
	var e api.Event
	var body string
	if err := sc.Scan(&e.Seq, &e.At, &e.Actor, &e.TaskID, &e.Kind, &body); err != nil {
		return api.Event{}, err
	}
	if err := json.Unmarshal([]byte(body), &e.Patch); err != nil {
		return api.Event{}, err
	}
	return e, nil
}

// Undo reverses the newest eligible event belonging to actor and writes an
// event of kind undo naming the seq it reversed. Depth is unbounded: a second
// call reverses the next-oldest eligible event, never the undo itself.
//
// Undo writes the prior field values back directly rather than routing
// through the status state machine. Reversing an inbox-to-done completion
// means moving a done task back to inbox, which the machine forbids as a
// forward move and which is nonetheless the correct reversal.
func (s *Store) Undo(ctx context.Context, actor string, now time.Time) (api.UndoResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return api.UndoResult{}, err
	}
	defer tx.Rollback()

	reversed, err := reversedSeqs(ctx, tx, actor)
	if err != nil {
		return api.UndoResult{}, err
	}

	target, found, err := newestUndoable(ctx, tx, actor, reversed)
	if err != nil {
		return api.UndoResult{}, err
	}
	if !found {
		return api.UndoResult{}, &api.Error{Code: api.ErrNothingToUndo, Message: "nothing left to undo"}
	}

	if target.TaskID != "" {
		if err := reverseFields(ctx, tx, target, now); err != nil {
			return api.UndoResult{}, err
		}
	}

	if err := appendEvent(ctx, tx, now, actor, target.TaskID, api.KindUndo,
		api.Patch{UndoOf: target.Seq}); err != nil {
		return api.UndoResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return api.UndoResult{}, err
	}

	res := api.UndoResult{Reversed: target.Seq, Kind: target.Kind, TaskID: target.TaskID}
	if target.TaskID != "" {
		t, err := s.Get(ctx, target.TaskID)
		if err == nil {
			res.Task = &t
		} else if !errors.Is(err, ErrNotFound) {
			return api.UndoResult{}, err
		}
	}
	return res, nil
}

// newestUndoable walks the actor's events newest first and returns the first
// one that is still eligible: an undoable kind that no earlier undo has
// already reversed.
func newestUndoable(ctx context.Context, tx *sql.Tx, actor string, reversed map[int64]bool) (api.Event, bool, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT seq, at, actor, coalesce(task_id, ''), kind, patch_json
		 FROM event WHERE actor = ? ORDER BY seq DESC`, actor)
	if err != nil {
		return api.Event{}, false, err
	}
	defer rows.Close()

	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return api.Event{}, false, err
		}
		if !undoableKinds[e.Kind] || reversed[e.Seq] {
			continue
		}
		return e, true, rows.Err()
	}
	return api.Event{}, false, rows.Err()
}

// reverseFields writes the from side of a patch back onto the task. Undoing a
// create drops the task rather than deleting the row, because nothing in td
// hard-deletes and the event log still refers to it.
func reverseFields(ctx context.Context, tx *sql.Tx, e api.Event, now time.Time) error {
	if e.Kind == api.KindTaskCreated {
		_, err := tx.ExecContext(ctx,
			`UPDATE task SET status = 'dropped', updated_at = ? WHERE id = ?`,
			now.UTC().Format(time.RFC3339), e.TaskID)
		return err
	}

	names := make([]string, 0, len(e.Patch.Fields))
	for name := range e.Patch.Fields {
		if name == tagsField {
			restored, err := stringList(e.Patch.Fields[name].From)
			if err != nil {
				return err
			}
			if err := setTags(ctx, tx, e.TaskID, restored); err != nil {
				return err
			}
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)

	cols := make([]string, 0, len(names)+1)
	args := make([]any, 0, len(names)+2)
	for _, name := range names {
		v, err := bindValue(name, e.Patch.Fields[name].From)
		if err != nil {
			return err
		}
		cols = append(cols, name+" = ?")
		args = append(args, v)
	}
	if len(cols) == 0 {
		_, err := tx.ExecContext(ctx, `UPDATE task SET updated_at = ? WHERE id = ?`,
			now.UTC().Format(time.RFC3339), e.TaskID)
		return err
	}
	cols = append(cols, "updated_at = ?")
	args = append(args, now.UTC().Format(time.RFC3339), e.TaskID)

	_, err := tx.ExecContext(ctx,
		`UPDATE task SET `+strings.Join(cols, ", ")+` WHERE id = ?`, args...)
	return err
}

// stringList decodes a tag list out of an event patch. Round-tripping
// through JSON turns []string into []any, so both shapes have to be handled.
func stringList(v any) ([]string, error) {
	switch list := v.(type) {
	case nil:
		return []string{}, nil
	case []string:
		return list, nil
	case []any:
		out := make([]string, 0, len(list))
		for _, item := range list {
			s, ok := item.(string)
			if !ok {
				return nil, errors.New("tag list holds a non-string")
			}
			out = append(out, s)
		}
		return out, nil
	}
	return nil, errors.New("tag list is not a list")
}

// reversedSeqs collects every seq an earlier undo already reversed, so a
// second undo walks past it to the next-oldest eligible event.
func reversedSeqs(ctx context.Context, tx *sql.Tx, actor string) (map[int64]bool, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT patch_json FROM event WHERE actor = ? AND kind = ?`, actor, api.KindUndo)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[int64]bool{}
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		var p api.Patch
		if err := json.Unmarshal([]byte(body), &p); err != nil {
			return nil, err
		}
		if p.UndoOf != 0 {
			out[p.UndoOf] = true
		}
	}
	return out, rows.Err()
}
