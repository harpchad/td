package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/seed"
)

// counterEntropy produces a repeatable byte stream so a seeded database gets
// the same ULIDs on every load. A fixture whose ids change between runs makes
// every diff of an export useless.
type counterEntropy struct{ n byte }

func (c *counterEntropy) Read(p []byte) (int, error) {
	for i := range p {
		c.n++
		p[i] = c.n
	}
	return len(p), nil
}

// Seed loads a fixture dataset into an empty database.
//
// It writes rows directly rather than going through Create, for two reasons:
// the fixture pins task numbers and created_at values that the normal path
// would assign itself, and seeding is not a mutation the user made, so it
// writes no events. A seeded database has an empty activity feed.
func (s *Store) Seed(ctx context.Context, d *seed.Data) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	ent := &counterEntropy{}
	newID := func(at time.Time) string {
		return ulid.MustNew(ulid.Timestamp(at), ent).String()
	}
	base := time.Unix(0, 0).UTC()

	personIDs := map[string]string{}
	for _, p := range d.People {
		id := newID(base)
		personIDs[p.Key] = id
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO person (id, handle, name) VALUES (?,?,?)`,
			id, strings.ToLower(p.Key), p.Name); err != nil {
			return fmt.Errorf("seed person %s: %w", p.Key, err)
		}
	}

	groupIDs := map[string]string{}
	for _, g := range d.Groups {
		id := newID(base)
		groupIDs[g.Key] = id
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO person_group (id, handle, name) VALUES (?,?,?)`,
			id, strings.ToLower(g.Key), g.Key); err != nil {
			return fmt.Errorf("seed group %s: %w", g.Key, err)
		}
		for _, member := range g.Members {
			pid, ok := personIDs[member]
			if !ok {
				return fmt.Errorf("seed group %s: unknown member %q", g.Key, member)
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO group_member (group_id, person_id) VALUES (?,?)`, id, pid); err != nil {
				return err
			}
		}
	}

	tagIDs := map[string]string{}
	taskIDs := map[int64]string{}

	// Two passes over tasks: the first inserts every row so a parent_num can
	// resolve regardless of the order the fixture lists them in.
	for _, t := range d.Tasks {
		taskIDs[t.Num] = newID(base)
	}

	for _, t := range d.Tasks {
		id := taskIDs[t.Num]

		due, dueIsDate, err := s.seedDate(t.DueAt)
		if err != nil {
			return fmt.Errorf("task %d due_at: %w", t.Num, err)
		}
		start, _, err := s.seedDate(t.StartAt)
		if err != nil {
			return fmt.Errorf("task %d start_at: %w", t.Num, err)
		}
		snooze, _, err := s.seedDate(t.SnoozeUntil)
		if err != nil {
			return fmt.Errorf("task %d snooze_until: %w", t.Num, err)
		}
		completed, _, err := s.seedDate(t.CompletedAt)
		if err != nil {
			return fmt.Errorf("task %d completed_at: %w", t.Num, err)
		}
		waitingSince, _, err := s.seedDate(t.WaitingSince)
		if err != nil {
			return fmt.Errorf("task %d waiting_since: %w", t.Num, err)
		}
		created, _, err := s.seedDate(&t.CreatedAt)
		if err != nil {
			return fmt.Errorf("task %d created_at: %w", t.Num, err)
		}

		var parentID any
		if t.ParentNum != nil {
			pid, ok := taskIDs[*t.ParentNum]
			if !ok {
				return fmt.Errorf("task %d: unknown parent_num %d", t.Num, *t.ParentNum)
			}
			parentID = pid
		}

		var waitingOn any
		if t.WaitingOn != nil {
			pid, ok := personIDs[*t.WaitingOn]
			if !ok {
				return fmt.Errorf("task %d: unknown waiting_on %q", t.Num, *t.WaitingOn)
			}
			waitingOn = pid
		}

		notify := t.Notify
		if notify == "" {
			notify = api.NotifyAuto
		}
		source := t.Source
		if source == "" {
			source = "local"
		}

		if _, err := tx.ExecContext(ctx, `INSERT INTO task
			(id, num, title, notes, status, priority, due_at, due_is_date, start_at,
			 snooze_until, notify, waiting_on, waiting_since, parent_id, source,
			 external_id, created_at, updated_at, completed_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			id, t.Num, t.Title, t.Notes, t.Status, t.Priority, due, boolInt(dueIsDate), start,
			snooze, notify, waitingOn, waitingSince, parentID, source,
			t.ExternalID, created, created, completed); err != nil {
			return fmt.Errorf("seed task %d: %w", t.Num, err)
		}

		for _, name := range t.Tags {
			key := strings.ToLower(name)
			tagID, ok := tagIDs[key]
			if !ok {
				tagID = newID(base)
				tagIDs[key] = tagID
				if _, err := tx.ExecContext(ctx,
					`INSERT INTO tag (id, name) VALUES (?,?)`, tagID, name); err != nil {
					return err
				}
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO task_tag (task_id, tag_id) VALUES (?,?)`, id, tagID); err != nil {
				return err
			}
		}

		for role, keys := range t.People {
			for _, key := range keys {
				pid, ok := personIDs[key]
				if !ok {
					return fmt.Errorf("task %d: unknown person %q", t.Num, key)
				}
				if _, err := tx.ExecContext(ctx,
					`INSERT INTO task_person (task_id, person_id, role) VALUES (?,?,?)`,
					id, pid, role); err != nil {
					return err
				}
			}
		}

		for _, key := range t.Groups {
			gid, ok := groupIDs[key]
			if !ok {
				return fmt.Errorf("task %d: unknown group %q", t.Num, key)
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO task_group (task_id, group_id) VALUES (?,?)`, id, gid); err != nil {
				return err
			}
		}

		for i := 0; i < t.Attachments; i++ {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO attachment (id, task_id, sha256, filename, bytes, mime, created_at)
				 VALUES (?,?,?,?,?,?,?)`,
				newID(base), id,
				fmt.Sprintf("%064x", t.Num*100+int64(i)),
				fmt.Sprintf("seed-%d-%d.bin", t.Num, i), 1024, "application/octet-stream",
				created); err != nil {
				return err
			}
		}
	}

	if err := seedDefaultFilters(ctx, tx, newID(base), ent); err != nil {
		return err
	}
	return tx.Commit()
}

// defaultFilters ship on every install: the four saved filters section 6
// names, plus the everything view on slot 2 that shows synced mirrors.
var defaultFilters = []api.SavedFilter{
	{Slot: 1, Name: "Today", Query: "is:open src:local -is:inbox -is:snoozed -is:deferred"},
	{Slot: 2, Name: "Everything", Query: "is:open"},
	{Slot: 3, Name: "Inbox", Query: "is:inbox"},
	{Slot: 4, Name: "Waiting", Query: "is:waiting"},
	{Slot: 5, Name: "Overdue", Query: "is:open due:overdue"},
}

func seedDefaultFilters(ctx context.Context, tx *sql.Tx, _ string, ent *counterEntropy) error {
	for _, f := range defaultFilters {
		id := ulid.MustNew(ulid.Timestamp(time.Unix(0, 0).UTC()), ent).String()
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO saved_filter (id, slot, name, query) VALUES (?,?,?,?)`,
			id, f.Slot, f.Name, f.Query); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) seedDate(v *string) (any, bool, error) {
	if v == nil || *v == "" {
		return nil, true, nil
	}
	out, isDate, err := normalizeDate(*v, s.loc)
	if err != nil {
		return nil, false, err
	}
	return out, isDate, nil
}
