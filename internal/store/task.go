package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/query"
)

const taskColumns = `t.id, t.num, t.title, t.notes, t.status, t.priority,
	t.due_at, t.due_is_date, t.start_at, t.snooze_until,
	t.notify, t.remind_before, t.notified_at,
	t.waiting_on, t.waiting_since, t.effort, t.parent_id, t.series_id,
	t.source, t.external_id, t.external_url, t.external_rev, t.upstream_gone,
	t.created_at, t.updated_at, t.completed_at`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanTask(sc rowScanner) (api.Task, error) {
	var t api.Task
	err := sc.Scan(
		&t.ID, &t.Num, &t.Title, &t.Notes, &t.Status, &t.Priority,
		&t.DueAt, &t.DueIsDate, &t.StartAt, &t.SnoozeUntil,
		&t.Notify, &t.RemindBefore, &t.NotifiedAt,
		&t.WaitingOn, &t.WaitingSince, &t.Effort, &t.ParentID, &t.SeriesID,
		&t.Source, &t.ExternalID, &t.ExternalURL, &t.ExternalRev, &t.UpstreamGone,
		&t.CreatedAt, &t.UpdatedAt, &t.CompletedAt,
	)
	if t.Tags == nil {
		t.Tags = []string{}
	}
	if t.People == nil {
		t.People = []api.TaskPerson{}
	}
	if t.Groups == nil {
		t.Groups = []string{}
	}
	return t, err
}

// List runs a filter query and returns the matching tasks in the default
// order. Filtering happens in SQL and ordering happens in Go, so the API, the
// TUI, and the web UI all share one comparator rather than three ORDER BY
// clauses that drift.
func (s *Store) List(ctx context.Context, filter string, now time.Time) ([]api.Task, error) {
	node, err := query.ParseAt(filter, now.In(s.loc))
	if err != nil {
		return nil, err
	}
	where, err := buildWhere(node, newFilterContext(now, s.loc))
	if err != nil {
		return nil, err
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT `+taskColumns+` FROM task t WHERE `+where.sql, where.args...)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	defer rows.Close()

	var out []api.Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if err := s.hydrate(ctx, out); err != nil {
		return nil, err
	}
	query.NewSorter(now.In(s.loc)).Sort(out)
	return out, nil
}

// Get returns one task by id.
func (s *Store) Get(ctx context.Context, id string) (api.Task, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM task t WHERE t.id = ?`, id)
	t, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return api.Task{}, ErrNotFound
	}
	if err != nil {
		return api.Task{}, err
	}
	one := []api.Task{t}
	if err := s.hydrate(ctx, one); err != nil {
		return api.Task{}, err
	}
	return one[0], nil
}

// GetByNum returns one task by its short human id, the number you type in
// `td done 412`.
func (s *Store) GetByNum(ctx context.Context, num int64) (api.Task, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM task WHERE num = ?`, num).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return api.Task{}, ErrNotFound
	}
	if err != nil {
		return api.Task{}, err
	}
	return s.Get(ctx, id)
}

// Resolve accepts either a ULID or a decimal task number and returns the id.
func (s *Store) Resolve(ctx context.Context, ref string) (string, error) {
	if n, err := parseNum(ref); err == nil {
		var id string
		err := s.db.QueryRowContext(ctx, `SELECT id FROM task WHERE num = ?`, n).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		return id, err
	}
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM task WHERE id = ?`, ref).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return id, err
}

func parseNum(s string) (int64, error) {
	if s == "" {
		return 0, errors.New("empty")
	}
	var n int64
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errors.New("not a number")
		}
		n = n*10 + int64(c-'0')
	}
	return n, nil
}

// runLoader executes one hydration query and hands each row to scan.
func (s *Store) runLoader(ctx context.Context, sqlText string, ids []any, scan func(*sql.Rows) error) error {
	rows, err := s.db.QueryContext(ctx, sqlText, ids...)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		if err := scan(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

// hydrate fills in the fields that live in other tables: tags, person links,
// groups, attachment count, and the child counts that drive the 2/5 badge.
func (s *Store) hydrate(ctx context.Context, tasks []api.Task) error {
	if len(tasks) == 0 {
		return nil
	}
	byID := make(map[string]*api.Task, len(tasks))
	ids := make([]any, 0, len(tasks))
	for i := range tasks {
		byID[tasks[i].ID] = &tasks[i]
		ids = append(ids, tasks[i].ID)
	}
	in := "(" + strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + ")"

	type loader struct {
		sql  string
		scan func(*sql.Rows) error
	}

	loaders := []loader{
		{
			sql: `SELECT tt.task_id, g.name FROM task_tag tt JOIN tag g ON g.id = tt.tag_id
			      WHERE tt.task_id IN ` + in + ` ORDER BY g.name`,
			scan: func(r *sql.Rows) error {
				var id, name string
				if err := r.Scan(&id, &name); err != nil {
					return err
				}
				byID[id].Tags = append(byID[id].Tags, name)
				return nil
			},
		},
		{
			sql: `SELECT tp.task_id, pe.id, pe.name, tp.role
			      FROM task_person tp JOIN person pe ON pe.id = tp.person_id
			      WHERE tp.task_id IN ` + in + ` ORDER BY tp.role, pe.name`,
			scan: func(r *sql.Rows) error {
				var id string
				var p api.TaskPerson
				if err := r.Scan(&id, &p.PersonID, &p.Name, &p.Role); err != nil {
					return err
				}
				byID[id].People = append(byID[id].People, p)
				return nil
			},
		},
		{
			sql: `SELECT tg.task_id, pg.name FROM task_group tg
			      JOIN person_group pg ON pg.id = tg.group_id
			      WHERE tg.task_id IN ` + in + ` ORDER BY pg.name`,
			scan: func(r *sql.Rows) error {
				var id, name string
				if err := r.Scan(&id, &name); err != nil {
					return err
				}
				byID[id].Groups = append(byID[id].Groups, name)
				return nil
			},
		},
		{
			sql: `SELECT task_id, count(*) FROM attachment
			      WHERE task_id IN ` + in + ` GROUP BY task_id`,
			scan: func(r *sql.Rows) error {
				var id string
				var n int
				if err := r.Scan(&id, &n); err != nil {
					return err
				}
				byID[id].Attachments = n
				return nil
			},
		},
		{
			sql: `SELECT parent_id, count(*), sum(CASE WHEN status = 'done' THEN 1 ELSE 0 END)
			      FROM task WHERE parent_id IN ` + in + ` GROUP BY parent_id`,
			scan: func(r *sql.Rows) error {
				var id string
				var total, done int
				if err := r.Scan(&id, &total, &done); err != nil {
					return err
				}
				byID[id].ChildrenTotal = total
				byID[id].ChildrenDone = done
				return nil
			},
		},
	}

	for _, l := range loaders {
		if err := s.runLoader(ctx, l.sql, ids, l.scan); err != nil {
			return err
		}
	}

	// A waiting_on link is not a task_person row, so add it here. Both the
	// person page and the @handle:waiting filter treat it as a role.
	for i := range tasks {
		if tasks[i].WaitingOn == nil {
			continue
		}
		var name string
		err := s.db.QueryRowContext(ctx, `SELECT name FROM person WHERE id = ?`, *tasks[i].WaitingOn).Scan(&name)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		tasks[i].People = append(tasks[i].People, api.TaskPerson{
			PersonID: *tasks[i].WaitingOn, Name: name, Role: api.RoleWaiting,
		})
	}
	return nil
}
