package store

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/sync"
)

// Sync applies one batch from a plugin.
//
// Idempotent by design: a plugin can always replay. Every item is matched on
// (source, external_id), and an item whose rev has not moved is left
// completely alone rather than rewritten with identical values, so a replay
// costs nothing and writes no events.
func (s *Store) Sync(ctx context.Context, actor, source string, in sync.Request, now time.Time) (sync.Result, error) {
	source = strings.TrimSpace(strings.ToLower(source))
	if source == "" || source == "local" {
		return sync.Result{}, &api.Error{
			Code: api.ErrBadRequest, Message: "a sync source is required and cannot be local",
		}
	}

	out := sync.Result{Cursor: in.Cursor}
	for _, item := range in.Items {
		if strings.TrimSpace(item.ExternalID) == "" {
			return out, &api.Error{
				Code: api.ErrBadRequest, Message: "every item needs an external_id",
			}
		}
		kind, unresolved, err := s.syncOne(ctx, actor, source, item, now)
		out.Unresolved = mergeUnresolved(out.Unresolved, unresolved)
		if err != nil {
			return out, err
		}
		switch kind {
		case syncCreated:
			out.Created++
		case syncUpdated:
			out.Updated++
		default:
			out.Unchanged++
		}
	}

	sort.Slice(out.Unresolved, func(i, j int) bool {
		return out.Unresolved[i].SourceUser < out.Unresolved[j].SourceUser
	})

	for _, external := range in.Gone {
		gone, err := s.markGone(ctx, actor, source, external, now)
		if err != nil {
			return out, err
		}
		if gone {
			out.Gone++
		}
	}
	return out, nil
}

type syncOutcome int

const (
	syncUnchanged syncOutcome = iota
	syncCreated
	syncUpdated
)

func (s *Store) syncOne(ctx context.Context, actor, source string, item sync.Item, now time.Time) (syncOutcome, []sync.Unresolved, error) {
	existing, err := s.bySourceID(ctx, source, item.ExternalID)
	if errors.Is(err, ErrNotFound) {
		unresolved, err := s.createMirror(ctx, actor, source, item, now)
		return syncCreated, unresolved, err
	}
	if err != nil {
		return syncUnchanged, nil, err
	}

	// The rev is the cheap idempotence check. A plugin replaying its window
	// sends the same rev, and an item that has not moved upstream is left
	// untouched: no write, no event, no updated_at churn.
	if item.Rev != "" && existing.ExternalRev != nil && *existing.ExternalRev == item.Rev && !existing.UpstreamGone {
		return syncUnchanged, nil, nil
	}

	patch := api.TaskPatch{Presence: map[string]bool{}}
	patch.Title = &item.Title

	// Status is upstream, with one exception: what you completed or dropped
	// locally stays that way. Completing a mirrored task in td is a statement
	// about your own work, and a sync that reopened it every fifteen minutes
	// would make the mirror an argument rather than a list.
	if item.Status != "" && !sync.LocalStatus(existing.Status) {
		if mapped := normalizeStatus(item.Status); mapped != "" && mapped != existing.Status {
			patch.Status = &mapped
		}
	}

	// Due is upstream: it is a commitment somebody else made, and the mirror
	// should show it. Absent means the plugin could not see it, which is not
	// the same as cleared, so only a present value writes.
	if item.DueAt != nil {
		patch.DueAt, patch.Presence["due_at"] = item.DueAt, true
	}

	// Nothing here touches priority, notes, tags, people links, snooze, or
	// any other locally-owned field. That is the whole rule, and the test
	// that proves it sets all of them and syncs twice.
	if _, err := s.Patch(ctx, actor, existing.ID, patch, "", now); err != nil {
		return syncUnchanged, nil, err
	}
	if err := s.setMirrorColumns(ctx, existing.ID, item); err != nil {
		return syncUnchanged, nil, err
	}
	unresolved, err := s.linkSourcePeople(ctx, actor, source, existing.ID, item.People, now)
	if err != nil {
		return syncUnchanged, unresolved, err
	}
	return syncUpdated, unresolved, nil
}

// createMirror inserts a task the plugin has never reported before.
func (s *Store) createMirror(ctx context.Context, actor, source string, item sync.Item, now time.Time) ([]sync.Unresolved, error) {
	status := normalizeStatus(item.Status)
	if status == "" {
		status = api.StatusTodo
	}
	// A mirror never lands in the inbox. The inbox is for things you captured
	// and have not sorted; a Jira backlog arriving there would bury it.
	if status == api.StatusInbox {
		status = api.StatusTodo
	}

	external := item.ExternalID
	in := api.TaskCreate{
		Title: item.Title, Status: status, DueAt: item.DueAt,
		Source: source, ExternalID: &external,
	}
	if item.URL != "" {
		in.ExternalURL = &item.URL
	}
	task, err := s.Create(ctx, actor, in, now)
	if err != nil {
		return nil, err
	}
	if err := s.setMirrorColumns(ctx, task.ID, item); err != nil {
		return nil, err
	}
	return s.linkSourcePeople(ctx, actor, source, task.ID, item.People, now)
}

// setMirrorColumns writes the fields that have no place in TaskPatch because
// no human ever sets them.
func (s *Store) setMirrorColumns(ctx context.Context, taskID string, item sync.Item) error {
	var url, rev any
	if item.URL != "" {
		url = item.URL
	}
	if item.Rev != "" {
		rev = item.Rev
	}
	// upstream_gone is cleared here as well: an item that comes back is back.
	_, err := s.db.ExecContext(ctx,
		`UPDATE task SET external_url = coalesce(?, external_url),
		                 external_rev = coalesce(?, external_rev),
		                 upstream_gone = 0
		 WHERE id = ?`, url, rev, taskID)
	return err
}

// linkSourcePeople resolves upstream identities onto people, and reports the
// ones it could not.
//
// This is what person_identity exists for: a Jira account id, a monday user
// id, and a Graph object id all land on one person row, so "everything
// involving Brandiss" spans three systems instead of producing three
// Brandisses.
//
// Three ways an identity resolves, in descending order of evidence:
//
//  1. It is already mapped. Certain.
//  2. Its email matches a person's, exactly and uniquely. An address is an
//     identity; this is evidence rather than a guess, and it is what makes a
//     first sync attach the people you already track.
//  3. Nobody has that handle yet, so a new person is created for them.
//
// What it deliberately does not do is match on name. Two people called Stacey
// is an ordinary situation and merging them is not something you can see by
// looking at the list afterwards. An identity that gets this far is returned
// unresolved so the caller can say so, because dropping the link in silence
// was the original bug: it lost exactly the people who matter, since a handle
// collision means somebody you already track.
func (s *Store) linkSourcePeople(ctx context.Context, actor, source, taskID string, people []sync.ItemPerson, now time.Time) ([]sync.Unresolved, error) {
	var unresolved []sync.Unresolved

	for _, link := range people {
		if strings.TrimSpace(link.SourceUser) == "" {
			continue
		}
		role := link.Role
		if role == "" {
			role = api.RoleInvolved
		}

		person, reason, err := s.resolveIdentity(ctx, source, link, now)
		if err != nil {
			return unresolved, err
		}
		if reason != "" {
			unresolved = append(unresolved, sync.Unresolved{
				Source: source, SourceUser: link.SourceUser,
				Name: link.Name, Email: link.Email, Reason: reason,
			})
			continue
		}
		if err := s.LinkPerson(ctx, actor, taskID, person.ID, role, now); err != nil {
			return unresolved, err
		}
	}
	return unresolved, nil
}

// Reasons an identity did not resolve. They are separate strings because the
// fixes are different: one needs `td person map`, the other needs the plugin
// to send more.
const (
	reasonHandleTaken = "somebody already has that handle, so this was not guessed at"
	reasonNoName      = "the plugin sent no name or email to go on"
)

// resolveIdentity returns the person behind an upstream identity, or the
// reason there is not one.
func (s *Store) resolveIdentity(ctx context.Context, source string, link sync.ItemPerson, now time.Time) (api.Person, string, error) {
	// 1. Already mapped.
	person, err := s.PersonByIdentity(ctx, source, link.SourceUser)
	if err == nil {
		return person, "", nil
	}
	if !errors.Is(err, ErrNotFound) {
		return api.Person{}, "", err
	}

	// 2. The email matches somebody. An address is an identity, so this is
	// evidence rather than a guess, and the mapping is recorded so the next
	// sync takes the certain path.
	if email := strings.TrimSpace(link.Email); email != "" {
		person, err := s.PersonByEmail(ctx, email)
		if err == nil {
			if err := s.LinkIdentity(ctx, person.ID, source, link.SourceUser); err != nil {
				return api.Person{}, "", err
			}
			return person, "", nil
		}
		if !errors.Is(err, ErrNotFound) {
			return api.Person{}, "", err
		}
	}

	// 3. Nobody by that handle yet, so this is somebody new.
	handle := handleFor(link.Name)
	if handle == "" {
		// A name that is entirely non-ASCII, or absent. The address is the
		// next best thing to build a handle from.
		handle = handleFor(localPart(link.Email))
	}
	name := strings.TrimSpace(link.Name)
	if name == "" {
		name = strings.TrimSpace(link.Email)
	}
	if handle == "" || name == "" {
		return api.Person{}, reasonNoName, nil
	}

	// Asked rather than attempted, so a database fault stays a fault instead
	// of being reported as an ambiguous name.
	taken, err := s.handleTaken(ctx, handle)
	if err != nil {
		return api.Person{}, "", err
	}
	if taken {
		// Somebody with that first name is already known, which is the case
		// worth reporting rather than resolving: it is either the same person,
		// and one `td person map` fixes it forever, or a different one, and
		// guessing would have merged two people irreversibly.
		return api.Person{}, reasonHandleTaken, nil
	}

	created, err := s.CreatePerson(ctx, api.Person{
		Name: name, Handle: handle, Email: strings.TrimSpace(link.Email),
	}, now)
	if err != nil {
		return api.Person{}, "", err
	}
	if err := s.LinkIdentity(ctx, created.ID, source, link.SourceUser); err != nil {
		return api.Person{}, "", err
	}
	return created, "", nil
}

// handleTaken reports whether a handle is already in use.
func (s *Store) handleTaken(ctx context.Context, handle string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM person WHERE lower(handle) = lower(?)`, handle).Scan(&n)
	return n > 0, err
}

// localPart is the part of an address before the @.
func localPart(email string) string {
	at := strings.IndexByte(email, '@')
	if at <= 0 {
		return ""
	}
	return email[:at]
}

// markGone flags an item that disappeared upstream.
//
// Marked, never deleted. A ticket you can no longer see is not a ticket that
// never existed, and something in your notes probably refers to it. The task
// keeps its number, its history, and everything local you attached to it.
func (s *Store) markGone(ctx context.Context, actor, source, external string, now time.Time) (bool, error) {
	task, err := s.bySourceID(ctx, source, external)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if task.UpstreamGone {
		return false, nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`UPDATE task SET upstream_gone = 1, updated_at = ? WHERE id = ?`,
		now.UTC().Format(time.RFC3339), task.ID); err != nil {
		return false, err
	}
	if err := appendEvent(ctx, tx, now, actor, task.ID, api.KindSyncGone, api.Patch{
		Meta: map[string]any{"source": source, "external_id": external},
	}); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// bySourceID finds a mirrored task.
func (s *Store) bySourceID(ctx context.Context, source, external string) (api.Task, error) {
	var id string
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM task WHERE source = ? AND external_id = ?`, source, external).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return api.Task{}, ErrNotFound
	}
	if err != nil {
		return api.Task{}, err
	}
	return s.Get(ctx, id)
}

// normalizeStatus keeps a plugin from inventing states. The state machine is
// closed, and a plugin sending something outside it is a plugin bug that
// should not become a database full of unknown statuses.
func normalizeStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case api.StatusInbox:
		return api.StatusInbox
	case api.StatusTodo:
		return api.StatusTodo
	case api.StatusDoing:
		return api.StatusDoing
	case api.StatusWaiting:
		return api.StatusWaiting
	case api.StatusDone:
		return api.StatusDone
	case api.StatusDropped:
		return api.StatusDropped
	}
	return ""
}

// handleFor is the handle a person gets when a plugin introduces them.
func handleFor(name string) string {
	fields := strings.Fields(name)
	if len(fields) == 0 {
		return ""
	}
	var b strings.Builder
	for _, r := range strings.ToLower(fields[0]) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// mergeUnresolved keeps one entry per identity. The same person appears on
// every task they touch, and a report that listed them thirty times would be
// a report nobody reads.
func mergeUnresolved(into, add []sync.Unresolved) []sync.Unresolved {
	for _, candidate := range add {
		seen := false
		for _, existing := range into {
			if existing.SourceUser == candidate.SourceUser {
				seen = true
				break
			}
		}
		if !seen {
			into = append(into, candidate)
		}
	}
	return into
}
