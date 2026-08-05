package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/recur"
)

// Series is a recurrence rule plus the template its instances are made from.
type Series struct {
	ID       string         `json:"id"`
	RRule    string         `json:"rrule"`
	Mode     string         `json:"mode"`
	Catchup  string         `json:"catchup"`
	Anchor   string         `json:"anchor"`
	TZ       string         `json:"tz"`
	Template api.TaskCreate `json:"template"`
	NextAt   *string        `json:"next_at"`
	EndsAt   *string        `json:"ends_at"`

	// LastFiredAt is where the catch-up walk starts. Without it a restart
	// cannot tell a missed occurrence from one that never came due.
	LastFiredAt   *string `json:"last_fired_at"`
	CurrentTaskID *string `json:"current_task_id"`
}

// CreateSeries stores a rule and materializes its first instance.
//
// Exactly one open instance exists per series at any time. Generating a year
// of instances up front makes every query slower and every edit ambiguous.
func (s *Store) CreateSeries(ctx context.Context, actor string, in Series, now time.Time) (Series, api.Task, error) {
	series, first, err := s.insertSeries(ctx, in, now)
	if err != nil {
		return Series{}, api.Task{}, err
	}
	task, err := s.materialize(ctx, actor, series, first, now)
	if err != nil {
		return Series{}, api.Task{}, err
	}
	series.CurrentTaskID = &task.ID
	return series, task, nil
}

// RepeatTask turns a task that already exists into the first instance of a new
// series, rather than making a second one beside it.
//
// This is what "repeat this" means when you are looking at a task: the thing
// in front of you becomes the one live instance and the rule decides what
// comes after it. CreateSeries materializes from the template instead, which
// is right for the API, where a series is created from scratch and there is no
// task yet. Calling that path from a task produced an exact duplicate: same
// title, same due date, one attached to the series and one not.
//
// Section 3's rule holds either way. Exactly one open instance exists, and the
// task's own fields are untouched: adopting it changes what comes next, not
// what it is.
func (s *Store) RepeatTask(ctx context.Context, actor, taskID string, in Series, now time.Time) (Series, api.Task, error) {
	task, err := s.Get(ctx, taskID)
	if err != nil {
		return Series{}, api.Task{}, err
	}
	if task.SeriesID != nil && *task.SeriesID != "" {
		return Series{}, api.Task{}, &api.Error{
			Code:    api.ErrBadRequest,
			Message: "that task already belongs to a series; edit the series instead",
		}
	}

	series, _, err := s.insertSeries(ctx, in, now)
	if err != nil {
		return Series{}, api.Task{}, err
	}

	if _, err := s.db.ExecContext(ctx,
		`UPDATE task SET series_id = ? WHERE id = ?`, series.ID, task.ID); err != nil {
		return Series{}, api.Task{}, err
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE series SET current_task_id = ? WHERE id = ?`, task.ID, series.ID); err != nil {
		return Series{}, api.Task{}, err
	}
	if err := s.logSeriesEvent(ctx, actor, task.ID, api.KindTaskAttached, map[string]any{
		"series": series.ID,
		"rrule":  series.RRule,
	}, now); err != nil {
		return Series{}, api.Task{}, err
	}

	series.CurrentTaskID = &task.ID
	out, err := s.Get(ctx, task.ID)
	if err != nil {
		return Series{}, api.Task{}, err
	}
	return series, out, nil
}

// insertSeries validates a rule, anchors it, and stores the row. It returns
// the series and its first occurrence, leaving the caller to decide whether
// that occurrence becomes a new task or is served by one that already exists.
func (s *Store) insertSeries(ctx context.Context, in Series, now time.Time) (Series, time.Time, error) {
	if in.ID == "" {
		in.ID = NewID()
	}
	if in.Mode == "" {
		in.Mode = recur.ModeFixed
	}
	if in.Catchup == "" {
		in.Catchup = recur.CatchupSkip
	}
	if in.Anchor == "" {
		in.Anchor = "due"
	}
	if in.TZ == "" {
		in.TZ = s.loc.String()
	}
	if in.Mode != recur.ModeFixed && in.Mode != recur.ModeAfterCompletion {
		return Series{}, time.Time{}, &api.Error{
			Code: api.ErrBadRequest, Message: "mode is fixed or after_completion",
		}
	}
	if in.Catchup != recur.CatchupSkip && in.Catchup != recur.CatchupPile {
		return Series{}, time.Time{}, &api.Error{
			Code: api.ErrBadRequest, Message: "catchup is skip or pile",
		}
	}

	loc, err := time.LoadLocation(in.TZ)
	if err != nil {
		return Series{}, time.Time{}, &api.Error{Code: api.ErrBadRequest, Message: "unknown timezone " + in.TZ}
	}

	// The rule is anchored on the first due date, or on now when none was
	// given, which is what dtstart means.
	start := now.In(loc)
	if in.Template.DueAt != nil && *in.Template.DueAt != "" {
		if at, err := parseAnyDate(*in.Template.DueAt, loc); err == nil {
			start = at
		}
	}
	rule, err := recur.Parse(in.RRule, start, loc)
	if err != nil {
		return Series{}, time.Time{}, &api.Error{Code: api.ErrBadRequest, Message: err.Error()}
	}

	first, err := rule.Occurrences(1)
	if err != nil || len(first) == 0 {
		return Series{}, time.Time{}, &api.Error{Code: api.ErrBadRequest, Message: "that rule produces no occurrences"}
	}

	template, err := json.Marshal(in.Template)
	if err != nil {
		return Series{}, time.Time{}, err
	}

	// The first occurrence is materialized right away, so the series is
	// already fired up to that point and the next one is what the scheduler
	// waits for.
	fired := first[0].UTC().Format(time.RFC3339)
	in.LastFiredAt = &fired
	if next, err := rule.After(first[0]); err == nil && !next.IsZero() {
		formatted := next.UTC().Format(time.RFC3339)
		in.NextAt = &formatted
	}

	if _, err := s.db.ExecContext(ctx, `INSERT INTO series
		(id, rrule, mode, catchup, anchor, tz, template_json, next_at, ends_at, last_fired_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)`,
		in.ID, in.RRule, in.Mode, in.Catchup, in.Anchor, in.TZ,
		string(template), in.NextAt, in.EndsAt, in.LastFiredAt); err != nil {
		return Series{}, time.Time{}, fmt.Errorf("create series: %w", err)
	}

	return in, first[0], nil
}

// Series returns one series.
func (s *Store) Series(ctx context.Context, id string) (Series, error) {
	var out Series
	var template string
	err := s.db.QueryRowContext(ctx,
		`SELECT id, rrule, mode, catchup, anchor, tz, template_json, next_at, ends_at,
		        last_fired_at, current_task_id
		 FROM series WHERE id = ?`, id).
		Scan(&out.ID, &out.RRule, &out.Mode, &out.Catchup, &out.Anchor, &out.TZ,
			&template, &out.NextAt, &out.EndsAt, &out.LastFiredAt, &out.CurrentTaskID)
	if errors.Is(err, sql.ErrNoRows) {
		return Series{}, ErrNotFound
	}
	if err != nil {
		return Series{}, err
	}
	if err := json.Unmarshal([]byte(template), &out.Template); err != nil {
		return Series{}, err
	}
	return out, nil
}

// UpdateSeries rewrites the rule and the template. It is deliberately a
// separate call from patching a task: section 3 says editing an instance
// edits that instance, and editing the series needs an explicit action.
// Nothing already materialized is touched, so the change lands on the next
// instance and the one in the list today stays what the user is looking at.
func (s *Store) UpdateSeries(ctx context.Context, in Series) (Series, error) {
	current, err := s.Series(ctx, in.ID)
	if err != nil {
		return Series{}, err
	}
	if in.Mode == "" {
		in.Mode = current.Mode
	}
	if in.Catchup == "" {
		in.Catchup = current.Catchup
	}
	if in.TZ == "" {
		in.TZ = current.TZ
	}
	if in.Anchor == "" {
		in.Anchor = current.Anchor
	}
	if in.RRule == "" {
		in.RRule = current.RRule
	}

	loc, err := time.LoadLocation(in.TZ)
	if err != nil {
		return Series{}, &api.Error{Code: api.ErrBadRequest, Message: "unknown timezone " + in.TZ}
	}
	if _, _, err := s.ruleFor(in, loc, time.Now()); err != nil {
		return Series{}, &api.Error{Code: api.ErrBadRequest, Message: err.Error()}
	}

	template, err := json.Marshal(in.Template)
	if err != nil {
		return Series{}, err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE series
		SET rrule = ?, mode = ?, catchup = ?, anchor = ?, tz = ?, template_json = ?, ends_at = ?
		WHERE id = ?`,
		in.RRule, in.Mode, in.Catchup, in.Anchor, in.TZ, string(template), in.EndsAt, in.ID); err != nil {
		return Series{}, err
	}
	return s.Series(ctx, in.ID)
}

// AdvanceDue fires every series whose next occurrence has arrived and
// returns how many instances were created. It is what the scheduler calls,
// and it is safe to call on any tick: a series whose next_at is still in the
// future is skipped without expanding its rule.
func (s *Store) AdvanceDue(ctx context.Context, actor string, now time.Time) (int, error) {
	ids, err := s.dueSeriesIDs(ctx, now)
	if err != nil {
		return 0, err
	}
	made := 0
	for _, id := range ids {
		series, err := s.Series(ctx, id)
		if err != nil {
			return made, err
		}
		instances, err := s.AdvanceSeries(ctx, actor, series, now)
		if err != nil {
			return made, err
		}
		made += len(instances)
	}

	filled, err := s.fillIdleSeries(ctx, actor, now)
	return made + filled, err
}

// fillIdleSeries gives a fixed series its next instance when it has none.
//
// Completing an instance brings the next one forward, so in normal use no
// fixed series is ever idle. This is for the ways one gets there anyway: an
// instance that was dropped rather than finished, a database that predates the
// bring-forward behaviour, a reset that took the tasks. Without it a series in
// that state is invisible until its next occurrence arrives, which for a
// monthly rule is most of a month.
//
// It materializes the next occurrence, not the missed one. Dropping an
// instance means "not this time", and answering that by handing back the same
// task would be arguing with somebody about their own list.
func (s *Store) fillIdleSeries(ctx context.Context, actor string, now time.Time) (int, error) {
	ids, err := s.idleFixedSeriesIDs(ctx)
	if err != nil {
		return 0, err
	}

	made := 0
	for _, id := range ids {
		series, err := s.Series(ctx, id)
		if err != nil {
			return made, err
		}
		task, err := s.NextFixedAfterCompletion(ctx, actor, series, now)
		if err != nil {
			return made, err
		}
		if task.ID != "" {
			made++
		}
	}
	return made, nil
}

func (s *Store) dueSeriesIDs(ctx context.Context, now time.Time) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id FROM series
		 WHERE mode = ? AND next_at IS NOT NULL AND next_at <= ?
		 ORDER BY id`,
		recur.ModeFixed, now.UTC().Format(time.RFC3339))
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
	return ids, rows.Err()
}

// AllSeries returns every series, for the scheduler.
func (s *Store) AllSeries(ctx context.Context) ([]Series, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM series ORDER BY id`)
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

	out := make([]Series, 0, len(ids))
	for _, id := range ids {
		series, err := s.Series(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, series)
	}
	return out, nil
}

// materialize creates one instance of a series at a due instant.
func (s *Store) materialize(ctx context.Context, actor string, series Series, due time.Time, now time.Time) (api.Task, error) {
	in := series.Template
	in.ID = NewID()
	in.Status = ""

	// A date-only template keeps its instances date-only: "pay the mortgage
	// on the 1st" is a date, not an instant.
	if in.DueAt != nil && len(*in.DueAt) == len(recur.DateLayout) {
		formatted := due.Format(recur.DateLayout)
		in.DueAt = &formatted
	} else {
		formatted := due.Format(time.RFC3339)
		in.DueAt = &formatted
	}

	task, err := s.Create(ctx, actor, in, now)
	if err != nil {
		return api.Task{}, err
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE task SET series_id = ? WHERE id = ?`, series.ID, task.ID); err != nil {
		return api.Task{}, err
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE series SET current_task_id = ? WHERE id = ?`, task.ID, series.ID); err != nil {
		return api.Task{}, err
	}
	if err := s.logSeriesEvent(ctx, actor, task.ID, api.KindRecurrenceFired, map[string]any{
		"series": series.ID,
		"due":    due.Format(recur.DateLayout),
	}, now); err != nil {
		return api.Task{}, err
	}
	return s.Get(ctx, task.ID)
}

// openInstances counts the instances of a series that are neither done nor
// dropped. It is what "exactly one open instance at a time" is checked
// against, and it is a count rather than a flag because catchup=pile is
// allowed to break the rule on purpose.
func (s *Store) openInstances(ctx context.Context, seriesID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM task WHERE series_id = ? AND status NOT IN ('done','dropped')`,
		seriesID).Scan(&n)
	return n, err
}

// AdvanceSeries generates whatever a fixed series owes as of now and returns
// the instances it created.
//
// This is the catch-up question: a daily fixed task ignored for three days,
// do you now owe three, or one? skip logs each occurrence it rolls past and
// leaves the one open instance alone; pile creates the backlog. skip is the
// default because anything that piles up silently gets ignored, and then the
// whole list gets ignored.
//
// after_completion series generate nothing here. Their next instance comes
// from the completion, which is the whole difference between the modes.
func (s *Store) AdvanceSeries(ctx context.Context, actor string, series Series, now time.Time) ([]api.Task, error) {
	if series.Mode != recur.ModeFixed {
		return nil, nil
	}
	loc, err := time.LoadLocation(series.TZ)
	if err != nil {
		return nil, err
	}
	if series.EndsAt != nil && *series.EndsAt != "" {
		if ends, err := time.Parse(time.RFC3339, *series.EndsAt); err == nil && now.After(ends) {
			return nil, nil
		}
	}

	rule, dtstart, err := s.ruleFor(series, loc, now)
	if err != nil {
		return nil, err
	}

	// The walk starts where the last one stopped. Without last_fired_at a
	// restart cannot tell a missed occurrence from one that never came due,
	// and the series would either fire twice or not at all.
	from := dtstart
	if series.LastFiredAt != nil && *series.LastFiredAt != "" {
		if at, err := time.Parse(time.RFC3339, *series.LastFiredAt); err == nil {
			from = at.In(loc)
		}
	}

	due, err := rule.Between(from, now.In(loc))
	if err != nil {
		return nil, err
	}
	if len(due) == 0 {
		return nil, nil
	}

	open, err := s.openInstances(ctx, series.ID)
	if err != nil {
		return nil, err
	}

	made := make([]api.Task, 0, len(due))
	for _, at := range due {
		if series.Catchup == recur.CatchupSkip && open > 0 {
			// The previous instance is still sitting there. Firing again would
			// stack duplicates of the same chore, so the occurrence is logged
			// and dropped.
			if err := s.logMisses(ctx, actor, series, []time.Time{at}, now); err != nil {
				return made, err
			}
			continue
		}
		task, err := s.materialize(ctx, actor, series, at, now)
		if err != nil {
			return made, err
		}
		made = append(made, task)
		open++
	}

	if err := s.markFired(ctx, series, rule, due[len(due)-1], now.In(loc)); err != nil {
		return made, err
	}
	return made, nil
}

// markFired moves the walk forward and caches the next occurrence, so the
// scheduler can find work with a comparison instead of re-expanding every
// rule on every tick.
func (s *Store) markFired(ctx context.Context, series Series, rule *recur.Rule, fired, now time.Time) error {
	var next any
	if at, err := rule.After(now); err == nil && !at.IsZero() {
		next = at.UTC().Format(time.RFC3339)
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE series SET last_fired_at = ?, next_at = ? WHERE id = ?`,
		fired.UTC().Format(time.RFC3339), next, series.ID)
	return err
}

// ruleFor rebuilds a series' rule against the dtstart it was created with.
// The template's due date is that anchor: materializing an instance never
// rewrites the template, so it survives every tick.
func (s *Store) ruleFor(series Series, loc *time.Location, now time.Time) (*recur.Rule, time.Time, error) {
	dtstart := now.In(loc)
	switch {
	case series.Template.DueAt != nil && *series.Template.DueAt != "":
		if at, err := parseAnyDate(*series.Template.DueAt, loc); err == nil {
			dtstart = at
		}
	case series.LastFiredAt != nil && *series.LastFiredAt != "":
		if at, err := time.Parse(time.RFC3339, *series.LastFiredAt); err == nil {
			dtstart = at.In(loc)
		}
	}
	rule, err := recur.Parse(series.RRule, dtstart, loc)
	return rule, dtstart, err
}

// logMisses records the occurrences that rolled past. They are events rather
// than tasks: the point of skip is that they do not become work, but losing
// the fact that they happened would make the roll-forward invisible.
func (s *Store) logMisses(ctx context.Context, actor string, series Series, missed []time.Time, now time.Time) error {
	for _, at := range missed {
		if err := s.logSeriesEvent(ctx, actor, "", api.KindRecurrenceMissed, map[string]any{
			"series": series.ID,
			"due":    at.Format(recur.DateLayout),
		}, now); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) logSeriesEvent(ctx context.Context, actor, taskID, kind string, meta map[string]any, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if err := appendEvent(ctx, tx, now, actor, taskID, kind, api.Patch{Meta: meta}); err != nil {
		return err
	}
	return tx.Commit()
}

// idleFixedSeriesIDs finds fixed series with no open instance.
//
// Its own function so the cursor closes before the materializing starts. The
// pool is one connection, so an open cursor and a write on the same store are
// a deadlock rather than a slowdown.
func (s *Store) idleFixedSeriesIDs(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT s.id FROM series s
		 WHERE s.mode = ?
		   AND NOT EXISTS (
		     SELECT 1 FROM task t
		     WHERE t.series_id = s.id AND t.status NOT IN ('done','dropped'))
		 ORDER BY s.id`, recur.ModeFixed)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// NextFixedAfterCompletion brings a fixed series forward when its open
// instance is finished.
//
// Without this a monthly series vanishes from the list the moment you complete
// it and does not come back until the next occurrence arrives. That is the
// letter of "exactly one open instance at a time" and the wrong reading of it:
// for anything monthly it means the commitment is invisible for most of its
// cycle, and the first time somebody notices is when they wonder whether the
// series is broken.
//
// The schedule is not moved. The instance created is dated to the next
// occurrence the rule produces, so a report due on the 1st stays due on the
// 1st however early or late August's was finished. That is the whole
// difference from after_completion mode, which counts from the completion and
// would let the date drift.
//
// The series is marked fired up to that occurrence, so when the day arrives
// the scheduler sees next_at in the future and does nothing rather than
// materializing a second one.
func (s *Store) NextFixedAfterCompletion(ctx context.Context, actor string, series Series, now time.Time) (api.Task, error) {
	loc, err := time.LoadLocation(series.TZ)
	if err != nil {
		return api.Task{}, err
	}

	// A series somebody ended is finished, not paused.
	if series.EndsAt != nil && *series.EndsAt != "" {
		if ends, err := time.Parse(time.RFC3339, *series.EndsAt); err == nil && now.After(ends) {
			return api.Task{}, nil
		}
	}

	// The invariant, checked rather than assumed. Completing one instance
	// while another is open must not make a third.
	open, err := s.openInstances(ctx, series.ID)
	if err != nil {
		return api.Task{}, err
	}
	if open > 0 {
		return api.Task{}, nil
	}

	rule, dtstart, err := s.ruleFor(series, loc, now)
	if err != nil {
		return api.Task{}, err
	}
	from := dtstart
	if series.LastFiredAt != nil && *series.LastFiredAt != "" {
		if at, err := time.Parse(time.RFC3339, *series.LastFiredAt); err == nil {
			from = at.In(loc)
		}
	}

	due, err := rule.After(from)
	if err != nil || due.IsZero() {
		return api.Task{}, err
	}
	if series.EndsAt != nil && *series.EndsAt != "" {
		if ends, err := time.Parse(time.RFC3339, *series.EndsAt); err == nil && due.After(ends) {
			return api.Task{}, nil
		}
	}

	task, err := s.materialize(ctx, actor, series, due, now)
	if err != nil {
		return api.Task{}, err
	}
	if err := s.markFired(ctx, series, rule, due, due); err != nil {
		return api.Task{}, err
	}
	return task, nil
}

// NextAfterCompletion generates the next instance of an after_completion
// series, which is completion time plus the rule's interval.
func (s *Store) NextAfterCompletion(ctx context.Context, actor string, series Series, completedAt, now time.Time) (api.Task, error) {
	loc, err := time.LoadLocation(series.TZ)
	if err != nil {
		return api.Task{}, err
	}
	rule, err := recur.Parse(series.RRule, completedAt.In(loc), loc)
	if err != nil {
		return api.Task{}, err
	}
	due := rule.NextAfterCompletion(completedAt)

	task, err := s.materialize(ctx, actor, series, due, now)
	if err != nil {
		return api.Task{}, err
	}
	if err := s.markFired(ctx, series, rule, due, due); err != nil {
		return api.Task{}, err
	}
	return task, nil
}

// parseAnyDate reads either a bare calendar date or an RFC3339 instant.
func parseAnyDate(value string, loc *time.Location) (time.Time, error) {
	if len(value) == len(recur.DateLayout) {
		return time.ParseInLocation(recur.DateLayout, value, loc)
	}
	at, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, err
	}
	return at.In(loc), nil
}
