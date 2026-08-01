package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/query"
)

// People returns every person, ordered by name.
func (s *Store) People(ctx context.Context) ([]api.Person, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, handle, name, coalesce(email, ''), notes FROM person ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []api.Person{}
	for rows.Next() {
		var p api.Person
		if err := rows.Scan(&p.ID, &p.Handle, &p.Name, &p.Email, &p.Notes); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PersonByHandle looks a person up by the @handle the filter grammar uses.
func (s *Store) PersonByHandle(ctx context.Context, handle string) (api.Person, error) {
	var p api.Person
	err := s.db.QueryRowContext(ctx,
		`SELECT id, handle, name, coalesce(email, ''), notes FROM person WHERE lower(handle) = lower(?)`,
		handle).Scan(&p.ID, &p.Handle, &p.Name, &p.Email, &p.Notes)
	if errors.Is(err, sql.ErrNoRows) {
		return api.Person{}, ErrNotFound
	}
	return p, err
}

// Person looks a person up by id.
func (s *Store) Person(ctx context.Context, id string) (api.Person, error) {
	var p api.Person
	err := s.db.QueryRowContext(ctx,
		`SELECT id, handle, name, coalesce(email, ''), notes FROM person WHERE id = ?`,
		id).Scan(&p.ID, &p.Handle, &p.Name, &p.Email, &p.Notes)
	if errors.Is(err, sql.ErrNoRows) {
		return api.Person{}, ErrNotFound
	}
	return p, err
}

// ResolvePerson accepts an id or a handle.
func (s *Store) ResolvePerson(ctx context.Context, ref string) (api.Person, error) {
	if p, err := s.Person(ctx, ref); err == nil {
		return p, nil
	}
	return s.PersonByHandle(ctx, strings.TrimPrefix(ref, "@"))
}

// CreatePerson adds someone.
func (s *Store) CreatePerson(ctx context.Context, in api.Person, now time.Time) (api.Person, error) {
	in.Handle = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(in.Handle), "@"))
	in.Name = strings.TrimSpace(in.Name)
	if in.Handle == "" || in.Name == "" {
		return api.Person{}, &api.Error{Code: api.ErrBadRequest, Message: "a person needs a handle and a name"}
	}
	if strings.ContainsAny(in.Handle, " \t:@#") {
		return api.Person{}, &api.Error{
			Code:    api.ErrBadRequest,
			Message: "a handle is what you type after @, so it cannot contain spaces or sigils",
		}
	}
	if in.ID == "" {
		in.ID = NewID()
	}

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO person (id, handle, name, email, notes) VALUES (?,?,?,?,?)`,
		in.ID, in.Handle, in.Name, nullIfEmpty(in.Email), in.Notes)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return api.Person{}, &api.Error{Code: api.ErrConflict, Message: "that handle is taken"}
		}
		return api.Person{}, err
	}
	return in, nil
}

// UpdatePerson changes a person's name, email, or notes. The handle is not
// editable: saved filters and the event log refer to it.
func (s *Store) UpdatePerson(ctx context.Context, id string, in api.Person) (api.Person, error) {
	if _, err := s.Person(ctx, id); err != nil {
		return api.Person{}, err
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE person SET name = ?, email = ?, notes = ? WHERE id = ?`,
		strings.TrimSpace(in.Name), nullIfEmpty(in.Email), in.Notes, id)
	if err != nil {
		return api.Person{}, err
	}
	return s.Person(ctx, id)
}

// --- groups ------------------------------------------------------------

// Groups returns every group with its membership. Groups are static
// membership, not saved searches: they stay dumb on purpose.
func (s *Store) Groups(ctx context.Context) ([]api.Group, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, handle, name FROM person_group ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []api.Group{}
	for rows.Next() {
		var g api.Group
		if err := rows.Scan(&g.ID, &g.Handle, &g.Name); err != nil {
			return nil, err
		}
		g.Members = []string{}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for i := range out {
		members, err := s.groupMembers(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Members = members
	}
	return out, nil
}

func (s *Store) groupMembers(ctx context.Context, groupID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT p.id FROM group_member gm JOIN person p ON p.id = gm.person_id
		 WHERE gm.group_id = ? ORDER BY p.name`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// CreateGroup adds a group and its members.
func (s *Store) CreateGroup(ctx context.Context, in api.Group) (api.Group, error) {
	in.Handle = strings.ToLower(strings.TrimSpace(in.Handle))
	in.Name = strings.TrimSpace(in.Name)
	if in.Handle == "" || in.Name == "" {
		return api.Group{}, &api.Error{Code: api.ErrBadRequest, Message: "a group needs a handle and a name"}
	}
	if in.ID == "" {
		in.ID = NewID()
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return api.Group{}, err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO person_group (id, handle, name) VALUES (?,?,?)`,
		in.ID, in.Handle, in.Name); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return api.Group{}, &api.Error{Code: api.ErrConflict, Message: "that group handle is taken"}
		}
		return api.Group{}, err
	}
	for _, personID := range in.Members {
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO group_member (group_id, person_id) VALUES (?,?)`,
			in.ID, personID); err != nil {
			return api.Group{}, err
		}
	}
	return in, tx.Commit()
}

// SetGroupMembers replaces a group's membership.
func (s *Store) SetGroupMembers(ctx context.Context, groupID string, personIDs []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM group_member WHERE group_id = ?`, groupID); err != nil {
		return err
	}
	for _, id := range personIDs {
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO group_member (group_id, person_id) VALUES (?,?)`, groupID, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// --- task links --------------------------------------------------------

// LinkPerson attaches a person to a task in a role, and writes an event so
// the link can be undone like any other change.
func (s *Store) LinkPerson(ctx context.Context, actor, taskID, personID, role string, now time.Time) error {
	if role != api.RoleAssigner && role != api.RoleAssignee && role != api.RoleInvolved {
		return &api.Error{Code: api.ErrBadRequest, Message: "role is assigner, assignee, or involved"}
	}
	return s.changeLink(ctx, actor, taskID, personID, role, true, now)
}

// UnlinkPerson removes one role link.
func (s *Store) UnlinkPerson(ctx context.Context, actor, taskID, personID, role string, now time.Time) error {
	return s.changeLink(ctx, actor, taskID, personID, role, false, now)
}

func (s *Store) changeLink(ctx context.Context, actor, taskID, personID, role string, add bool, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	before, err := peopleOf(ctx, tx, taskID)
	if err != nil {
		return err
	}

	if add {
		_, err = tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO task_person (task_id, person_id, role) VALUES (?,?,?)`,
			taskID, personID, role)
	} else {
		_, err = tx.ExecContext(ctx,
			`DELETE FROM task_person WHERE task_id = ? AND person_id = ? AND role = ?`,
			taskID, personID, role)
	}
	if err != nil {
		return err
	}

	after, err := peopleOf(ctx, tx, taskID)
	if err != nil {
		return err
	}
	if strings.Join(before, ",") == strings.Join(after, ",") {
		return tx.Commit()
	}

	// Person links live in another table, so the field diff cannot see them.
	// They travel in the patch under a pseudo-field, the same way tags do.
	if err := appendEvent(ctx, tx, now, actor, taskID, api.KindTaskPeople, api.Patch{
		Fields: map[string]api.Change{peopleField: {From: before, To: after}},
	}); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE task SET updated_at = ? WHERE id = ?`, now.UTC().Format(time.RFC3339), taskID); err != nil {
		return err
	}
	return tx.Commit()
}

// peopleField is the pseudo-field a person-link change travels under. Each
// entry is "personID:role", which is enough for undo to restore the set.
const peopleField = "people"

func peopleOf(ctx context.Context, tx *sql.Tx, taskID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT person_id, role FROM task_person WHERE task_id = ? ORDER BY person_id, role`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []string{}
	for rows.Next() {
		var id, role string
		if err := rows.Scan(&id, &role); err != nil {
			return nil, err
		}
		out = append(out, id+":"+role)
	}
	return out, rows.Err()
}

// setPeopleLinks replaces every link on a task, for undo.
func setPeopleLinks(ctx context.Context, tx *sql.Tx, taskID string, links []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM task_person WHERE task_id = ?`, taskID); err != nil {
		return err
	}
	for _, link := range links {
		personID, role, ok := strings.Cut(link, ":")
		if !ok {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO task_person (task_id, person_id, role) VALUES (?,?,?)`,
			taskID, personID, role); err != nil {
			return err
		}
	}
	return nil
}

// --- identity mapping --------------------------------------------------

// LinkIdentity maps an external account onto a person, which is what lets
// "everything involving Brandiss" span systems. Without it a Jira account, a
// monday user, and an Entra object are three different Brandisses.
func (s *Store) LinkIdentity(ctx context.Context, personID, source, externalID string) error {
	if personID == "" || source == "" || externalID == "" {
		return &api.Error{Code: api.ErrBadRequest, Message: "an identity needs a person, a source, and an external id"}
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO person_identity (person_id, source, external_id) VALUES (?,?,?)
		 ON CONFLICT(source, external_id) DO UPDATE SET person_id = excluded.person_id`,
		personID, source, externalID)
	return err
}

// PersonByIdentity resolves an external account to a person.
func (s *Store) PersonByIdentity(ctx context.Context, source, externalID string) (api.Person, error) {
	var id string
	err := s.db.QueryRowContext(ctx,
		`SELECT person_id FROM person_identity WHERE source = ? AND external_id = ?`,
		source, externalID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return api.Person{}, ErrNotFound
	}
	if err != nil {
		return api.Person{}, err
	}
	return s.Person(ctx, id)
}

// Identities lists the external accounts mapped to a person.
func (s *Store) Identities(ctx context.Context, personID string) ([]api.Identity, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT source, external_id FROM person_identity WHERE person_id = ? ORDER BY source, external_id`,
		personID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []api.Identity{}
	for rows.Next() {
		var i api.Identity
		if err := rows.Scan(&i.Source, &i.ExternalID); err != nil {
			return nil, err
		}
		i.PersonID = personID
		out = append(out, i)
	}
	return out, rows.Err()
}

// --- the person page ---------------------------------------------------

// PersonPage assembles the screen you open before a 1:1.
//
// The order is section 5's, and it is the order rather than the contents that
// makes it useful: what they owe you, what you owe them, what you are waiting
// on with its age, then the softer links.
func (s *Store) PersonPage(ctx context.Context, personID string, now time.Time) (api.PersonPage, error) {
	person, err := s.Person(ctx, personID)
	if err != nil {
		return api.PersonPage{}, err
	}

	page := api.PersonPage{Person: person}
	handle := person.Handle

	sections := []struct {
		into   *[]api.Task
		filter string
	}{
		// Assigned to them: they are doing it.
		{&page.Assigned, "is:open @" + handle + ":assignee"},
		// Assigned by them: you owe them.
		{&page.Owed, "is:open @" + handle + ":assigner"},
		// Waiting on them, which the age below makes actionable.
		{&page.Waiting, "is:waiting @" + handle + ":waiting"},
		// Involved, which is everything softer.
		{&page.Involved, "is:open @" + handle + ":involved"},
		// The agenda: tasks tagged agenda, scoped to them.
		{&page.Agenda, "is:open #agenda @" + handle},
	}
	for _, section := range sections {
		tasks, err := s.List(ctx, section.filter, now)
		if err != nil {
			return api.PersonPage{}, err
		}
		*section.into = tasks
	}

	// Their groups' tasks, which is the reason groups exist at all.
	groups, err := s.Groups(ctx)
	if err != nil {
		return api.PersonPage{}, err
	}
	for _, g := range groups {
		for _, member := range g.Members {
			if member != personID {
				continue
			}
			page.Groups = append(page.Groups, g.Name)
			tasks, err := s.List(ctx, "is:open grp:"+g.Handle, now)
			if err != nil {
				return api.PersonPage{}, err
			}
			page.GroupTasks = append(page.GroupTasks, tasks...)
			break
		}
	}

	page.Identities, err = s.Identities(ctx, personID)
	if err != nil {
		return api.PersonPage{}, err
	}

	// Waiting age is what turns "waiting on Mikah" into "waiting on Mikah
	// since the 12th", which is the state you actually live in.
	page.WaitingDays = make([]int, len(page.Waiting))
	for i, t := range page.Waiting {
		page.WaitingDays[i] = waitingDays(t, now, s.loc)
	}
	return page, nil
}

// waitingDays is how long a task has been waiting, in whole days.
func waitingDays(t api.Task, now time.Time, loc *time.Location) int {
	if t.WaitingSince == nil {
		return 0
	}
	since := query.LocalDate(*t.WaitingSince, loc)
	if since == "" {
		return 0
	}
	start, err := time.ParseInLocation(query.DateLayout, since, loc)
	if err != nil {
		return 0
	}
	today, err := time.ParseInLocation(query.DateLayout, now.In(loc).Format(query.DateLayout), loc)
	if err != nil {
		return 0
	}
	days := int(today.Sub(start).Hours() / 24)
	if days < 0 {
		return 0
	}
	return days
}

// PersonByEmail finds a person by address, case-insensitively.
//
// It is how a sync attaches an upstream identity to somebody td already
// knows. An address is an identity, which a display name is not: two people
// called Stacey is ordinary, two people at the same address is not. An
// ambiguous match reports nothing found rather than picking one, because the
// whole point of preferring email over name is that it does not guess.
func (s *Store) PersonByEmail(ctx context.Context, email string) (api.Person, error) {
	email = strings.TrimSpace(email)
	if email == "" {
		return api.Person{}, ErrNotFound
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, handle, name, coalesce(email, ''), notes
		 FROM person WHERE lower(email) = lower(?)`, email)
	if err != nil {
		return api.Person{}, err
	}
	defer rows.Close()

	var found []api.Person
	for rows.Next() {
		var p api.Person
		if err := rows.Scan(&p.ID, &p.Handle, &p.Name, &p.Email, &p.Notes); err != nil {
			return api.Person{}, err
		}
		found = append(found, p)
	}
	if err := rows.Err(); err != nil {
		return api.Person{}, err
	}
	if len(found) != 1 {
		return api.Person{}, ErrNotFound
	}
	return found[0], nil
}
