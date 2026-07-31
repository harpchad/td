package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"time"

	"github.com/harpchad/td/internal/api"
)

// ExportVersion is the document's shape. It is checked on import, because a
// backup that silently half-restores is worse than one that refuses.
const ExportVersion = 1

// Export is everything td holds, in a form that goes back in.
//
// "A task system you cannot get your data out of is a hostage situation", and
// getting it out is only half of that: the round trip is what makes the file
// a backup rather than a report. TestExportRoundTripsWithNoLoss is the
// assertion that keeps it one.
//
// What is deliberately absent: fold state. Section 11 says it must not
// generate events, appear in an export, or sync to a plugin, and leaving it
// out of the type that gets serialized makes that structural rather than a
// rule to remember. Accounts, sessions, tokens, OAuth clients, grants, and
// signing keys are absent too: they are credentials, and a file you copy to
// object storage should not be able to log anybody in.
type Export struct {
	Version    int    `json:"version"`
	ExportedAt string `json:"exported_at"`
	Timezone   string `json:"timezone"`

	Tasks       []api.Task        `json:"tasks"`
	People      []api.Person      `json:"people"`
	Groups      []api.Group       `json:"groups"`
	Identities  []api.Identity    `json:"identities"`
	Filters     []api.SavedFilter `json:"filters"`
	Series      []Series          `json:"series"`
	Attachments []api.Attachment  `json:"attachments"`
	// Events are the history. They are what makes an export a record rather
	// than a snapshot: "what did I close in March" is a question about the
	// log, and a backup that dropped it would answer nothing.
	Events []api.Event `json:"events"`
}

// Export reads the whole database.
func (s *Store) Export(ctx context.Context, now time.Time) (Export, error) {
	out := Export{
		Version:    ExportVersion,
		ExportedAt: now.UTC().Format(time.RFC3339),
		Timezone:   s.loc.String(),
	}

	// Everything, in a stable order, so two exports of an unchanged database
	// are byte-identical and a diff of two backups shows what actually
	// changed.
	tasks, err := s.allTasks(ctx)
	if err != nil {
		return out, err
	}
	out.Tasks = tasks

	if out.People, err = s.People(ctx); err != nil {
		return out, err
	}
	if out.Groups, err = s.Groups(ctx); err != nil {
		return out, err
	}
	for _, person := range out.People {
		identities, err := s.Identities(ctx, person.ID)
		if err != nil {
			return out, err
		}
		out.Identities = append(out.Identities, identities...)
	}
	sort.Slice(out.Identities, func(i, j int) bool {
		if out.Identities[i].Source != out.Identities[j].Source {
			return out.Identities[i].Source < out.Identities[j].Source
		}
		return out.Identities[i].ExternalID < out.Identities[j].ExternalID
	})

	if out.Filters, err = s.SavedFilters(ctx); err != nil {
		return out, err
	}
	if out.Series, err = s.AllSeries(ctx); err != nil {
		return out, err
	}
	if out.Attachments, err = s.allAttachments(ctx); err != nil {
		return out, err
	}
	if out.Events, err = s.Events(ctx, 0, 0); err != nil {
		return out, err
	}

	// Nil and empty are the same thing to a reader and different to a byte
	// comparison, so the round-trip test would fail on an empty database for
	// no reason worth having.
	if out.Identities == nil {
		out.Identities = []api.Identity{}
	}
	if out.Series == nil {
		out.Series = []Series{}
	}
	return out, nil
}

// allTasks reads every task, in creation order.
func (s *Store) allTasks(ctx context.Context) ([]api.Task, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM task ORDER BY num`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]api.Task, 0, len(ids))
	for _, id := range ids {
		task, err := s.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, task)
	}
	return out, nil
}

func (s *Store) allAttachments(ctx context.Context) ([]api.Attachment, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, task_id, sha256, coalesce(filename, ''), coalesce(bytes, 0),
		        coalesce(mime, ''), created_at
		 FROM attachment ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []api.Attachment{}
	for rows.Next() {
		var a api.Attachment
		if err := rows.Scan(&a.ID, &a.TaskID, &a.SHA256, &a.Filename,
			&a.Bytes, &a.Mime, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Import restores an export into an empty database.
//
// It refuses a database that already holds tasks. Merging two task sets means
// deciding what a colliding number means, and there is no answer to that
// which is not somebody's data quietly disappearing. Restore into a fresh
// database, which is what a restore is.
//
// The write path is deliberately direct rather than going through Create and
// Patch: those apply defaults, run the state machine, and write events, all
// of which would rewrite the history the export exists to preserve.
func (s *Store) Import(ctx context.Context, in Export) error {
	if in.Version != ExportVersion {
		return &api.Error{
			Code: api.ErrBadRequest,
			Message: "this export is version " + itoa(in.Version) +
				" and this build reads version " + itoa(ExportVersion),
		}
	}

	var existing int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM task`).Scan(&existing); err != nil {
		return err
	}
	if existing > 0 {
		return &api.Error{
			Code:    api.ErrConflict,
			Message: "this database already has tasks; import restores into an empty one",
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// People first: tasks reference them, and so do the group memberships.
	for _, p := range in.People {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO person (id, name, handle, email, notes) VALUES (?,?,?,?,?)`,
			p.ID, p.Name, p.Handle, nullIfEmpty(p.Email), p.Notes); err != nil {
			return err
		}
	}
	for _, g := range in.Groups {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO person_group (id, name, handle) VALUES (?,?,?)`,
			g.ID, g.Name, g.Handle); err != nil {
			return err
		}
		for _, member := range g.Members {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO group_member (group_id, person_id) VALUES (?,?)`,
				g.ID, member); err != nil {
				return err
			}
		}
	}
	for _, id := range in.Identities {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO person_identity (person_id, source, external_id) VALUES (?,?,?)`,
			id.PersonID, id.Source, id.ExternalID); err != nil {
			return err
		}
	}

	// Series before tasks, because a task references one.
	for _, series := range in.Series {
		template, err := json.Marshal(series.Template)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO series
			(id, rrule, mode, catchup, anchor, tz, template_json, next_at, ends_at, last_fired_at)
			VALUES (?,?,?,?,?,?,?,?,?,?)`,
			series.ID, series.RRule, series.Mode, series.Catchup, series.Anchor,
			series.TZ, string(template), series.NextAt, series.EndsAt,
			series.LastFiredAt); err != nil {
			return err
		}
	}

	// A task's group links are stored, not derived, so they need the group
	// ids. The export carries names, because a name is what a person reads.
	groupIDs := make(map[string]string, len(in.Groups))
	for _, g := range in.Groups {
		groupIDs[g.Name] = g.ID
	}

	// Parents before children, so the foreign key holds.
	for _, task := range sortForImport(in.Tasks) {
		if err := insertExportedTask(ctx, tx, task, groupIDs); err != nil {
			return err
		}
	}
	// current_task_id is set after the tasks exist, for the same reason.
	for _, series := range in.Series {
		if series.CurrentTaskID == nil {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE series SET current_task_id = ? WHERE id = ?`,
			*series.CurrentTaskID, series.ID); err != nil {
			return err
		}
	}

	for _, a := range in.Attachments {
		if _, err := tx.ExecContext(ctx, `INSERT INTO attachment
			(id, task_id, sha256, filename, bytes, mime, created_at)
			VALUES (?,?,?,?,?,?,?)`,
			a.ID, a.TaskID, a.SHA256, a.Filename, a.Bytes, a.Mime, a.CreatedAt); err != nil {
			return err
		}
	}
	for _, f := range in.Filters {
		var slot any
		if f.Slot > 0 {
			slot = f.Slot
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO saved_filter (id, slot, name, query) VALUES (?,?,?,?)`,
			f.ID, slot, f.Name, f.Query); err != nil {
			return err
		}
	}

	// The event log last, with its sequence numbers preserved. Undo walks
	// backwards through it and the MCP change cursor points into it, so
	// renumbering would break both.
	for _, e := range in.Events {
		body, err := json.Marshal(e.Patch)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO event (seq, at, actor, task_id, kind, patch_json) VALUES (?,?,?,?,?,?)`,
			e.Seq, e.At, e.Actor, nullIfEmpty(e.TaskID), e.Kind, string(body)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// insertExportedTask writes one task and its tags and person links.
func insertExportedTask(ctx context.Context, tx *sql.Tx, t api.Task, groupIDs map[string]string) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO task
		(id, num, title, notes, status, priority, due_at, due_is_date, start_at,
		 snooze_until, notify, remind_before, notified_at, waiting_on, waiting_since,
		 effort, parent_id, series_id, source, external_id, external_url, external_rev,
		 upstream_gone, created_at, updated_at, completed_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.ID, t.Num, t.Title, t.Notes, t.Status, t.Priority, t.DueAt,
		boolInt(t.DueIsDate), t.StartAt, t.SnoozeUntil, t.Notify, t.RemindBefore,
		t.NotifiedAt, t.WaitingOn, t.WaitingSince, t.Effort, t.ParentID, t.SeriesID,
		t.Source, t.ExternalID, t.ExternalURL, t.ExternalRev, boolInt(t.UpstreamGone),
		t.CreatedAt, t.UpdatedAt, t.CompletedAt); err != nil {
		return err
	}
	// Tags go through the same normalisation the write path uses, so a
	// restored database has one tag row per name rather than one per task
	// that used it.
	if err := setTags(ctx, tx, t.ID, t.Tags); err != nil {
		return err
	}
	for _, p := range t.People {
		// waiting is derived from task.waiting_on rather than stored, so
		// re-inserting it would create a link the exporter did not read.
		if p.Role == api.RoleWaiting {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO task_person (task_id, person_id, role) VALUES (?,?,?)`,
			t.ID, p.PersonID, p.Role); err != nil {
			return err
		}
	}
	for _, name := range t.Groups {
		id, ok := groupIDs[name]
		if !ok {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO task_group (task_id, group_id) VALUES (?,?)`, t.ID, id); err != nil {
			return err
		}
	}
	return nil
}

// sortForImport puts parents before their children.
//
// A single pass is not enough for nesting, and the depth limit is two, so a
// second pass covers everything the schema allows. A cycle is impossible for
// the same reason, but the loop is bounded anyway rather than trusting that.
func sortForImport(tasks []api.Task) []api.Task {
	placed := map[string]bool{}
	out := make([]api.Task, 0, len(tasks))

	remaining := tasks
	for range 8 {
		if len(remaining) == 0 {
			break
		}
		var deferred []api.Task
		for _, t := range remaining {
			if t.ParentID != nil && *t.ParentID != "" && !placed[*t.ParentID] {
				deferred = append(deferred, t)
				continue
			}
			out = append(out, t)
			placed[t.ID] = true
		}
		if len(deferred) == len(remaining) {
			// Nothing moved, so whatever is left references a parent that is
			// not in the file. Appending it lets the foreign key report that
			// rather than silently dropping the rows.
			break
		}
		remaining = deferred
	}
	return append(out, remaining...)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
