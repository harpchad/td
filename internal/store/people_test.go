package store_test

import (
	"context"
	"testing"

	"github.com/harpchad/td/internal/api"
)

// TestPersonPageSections covers section 5's ordering. The page is a
// first-class screen rather than a filter preset, and the order is what makes
// it useful before a 1:1.
func TestPersonPageSections(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()

	mikah, err := s.PersonByHandle(ctx, "mikah")
	if err != nil {
		t.Fatal(err)
	}
	page, err := s.PersonPage(ctx, mikah.ID, now)
	if err != nil {
		t.Fatal(err)
	}

	if page.Person.Name != "Mikah" {
		t.Errorf("person = %+v", page.Person)
	}
	// The fixture links Mikah as assignee on 102, involved on 108, and the
	// waiting_on of 106.
	if !hasNum(page.Assigned, 102) {
		t.Errorf("assigned = %v, want 102", nums(page.Assigned))
	}
	if !hasNum(page.Involved, 108) {
		t.Errorf("involved = %v, want 108", nums(page.Involved))
	}
	if !hasNum(page.Waiting, 106) {
		t.Errorf("waiting = %v, want 106", nums(page.Waiting))
	}

	// A person's sections do not bleed into each other: the assignee link is
	// not also an involved link.
	if hasNum(page.Involved, 102) {
		t.Error("an assignee link showed up under involved as well")
	}
}

// TestWaitingCarriesItsAge covers the thing that makes the waiting section
// worth having: "waiting on Mikah since the 12th" is the state you live in,
// and the derived view is worth building on day one.
func TestWaitingCarriesItsAge(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()

	mikah, err := s.PersonByHandle(ctx, "mikah")
	if err != nil {
		t.Fatal(err)
	}
	page, err := s.PersonPage(ctx, mikah.ID, now)
	if err != nil {
		t.Fatal(err)
	}

	if len(page.Waiting) != len(page.WaitingDays) {
		t.Fatalf("%d waiting tasks and %d ages", len(page.Waiting), len(page.WaitingDays))
	}
	if len(page.Waiting) == 0 {
		t.Fatal("nothing waiting")
	}
	// The fixture says waiting since 2026-07-20 and the clock is 2026-08-03.
	if got := page.WaitingDays[0]; got != 14 {
		t.Errorf("waiting age = %d days, want 14", got)
	}
}

// TestGroupTasksAppearOnTheirMembersPages covers the reason groups exist.
func TestGroupTasksAppearOnTheirMembersPages(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()

	stacey, err := s.PersonByHandle(ctx, "stacey")
	if err != nil {
		t.Fatal(err)
	}
	page, err := s.PersonPage(ctx, stacey.ID, now)
	if err != nil {
		t.Fatal(err)
	}

	if len(page.Groups) == 0 {
		t.Fatal("stacey is in leadership in the fixture, but the page shows no groups")
	}
	if page.Groups[0] != "leadership" {
		t.Errorf("groups = %v", page.Groups)
	}
	if len(page.GroupTasks) == 0 {
		t.Error("the group has tasks in the fixture and none showed up")
	}
}

// TestTheAgendaIsJustATag covers the free-text agenda section, which is tasks
// tagged agenda scoped to that person rather than a separate store.
func TestTheAgendaIsJustATag(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()

	mikah, err := s.PersonByHandle(ctx, "mikah")
	if err != nil {
		t.Fatal(err)
	}

	before, err := s.PersonPage(ctx, mikah.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Agenda) != 0 {
		t.Fatalf("the fixture has no agenda items, got %d", len(before.Agenda))
	}

	p2 := 2
	if _, err := s.Create(ctx, actor, api.TaskCreate{
		Title: "Ask about the monday migration", Priority: &p2,
		Tags: []string{"agenda"}, People: []string{"mikah:involved"},
	}, now); err != nil {
		t.Fatal(err)
	}

	after, err := s.PersonPage(ctx, mikah.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Agenda) != 1 {
		t.Fatalf("agenda = %v, want the one tagged task", nums(after.Agenda))
	}
}

// TestIdentityMappingSpansSystems covers what person_identity is for: without
// it you get three Brandisses and the feature is worthless.
func TestIdentityMappingSpansSystems(t *testing.T) {
	s, _ := seeded(t)
	ctx := context.Background()

	brandiss, err := s.PersonByHandle(ctx, "brandiss")
	if err != nil {
		t.Fatal(err)
	}

	for _, id := range []api.Identity{
		{Source: "jira", ExternalID: "5b10a2844c20165700ede21g"},
		{Source: "monday", ExternalID: "44012345"},
		{Source: "planner", ExternalID: "8f3d2e11-0000-4a2b-9c3d-000000000001"},
	} {
		if err := s.LinkIdentity(ctx, brandiss.ID, id.Source, id.ExternalID); err != nil {
			t.Fatal(err)
		}
	}

	for _, id := range []api.Identity{
		{Source: "jira", ExternalID: "5b10a2844c20165700ede21g"},
		{Source: "monday", ExternalID: "44012345"},
	} {
		got, err := s.PersonByIdentity(ctx, id.Source, id.ExternalID)
		if err != nil {
			t.Fatalf("%s %s: %v", id.Source, id.ExternalID, err)
		}
		if got.ID != brandiss.ID {
			t.Errorf("%s resolved to %s, want one Brandiss", id.Source, got.Name)
		}
	}

	identities, err := s.Identities(ctx, brandiss.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(identities) != 3 {
		t.Errorf("%d identities, want 3", len(identities))
	}

	// Re-mapping an external account moves it rather than duplicating it.
	stacey, err := s.PersonByHandle(ctx, "stacey")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.LinkIdentity(ctx, stacey.ID, "jira", "5b10a2844c20165700ede21g"); err != nil {
		t.Fatal(err)
	}
	got, err := s.PersonByIdentity(ctx, "jira", "5b10a2844c20165700ede21g")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != stacey.ID {
		t.Error("re-mapping an identity did not move it")
	}
}

// TestPersonLinksAreUndoable covers the undo contract, which lists person
// links alongside tags. They live in another table, so they travel in the
// event patch under a pseudo-field.
func TestPersonLinksAreUndoable(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()

	task, err := s.GetByNum(ctx, 103)
	if err != nil {
		t.Fatal(err)
	}
	brandiss, err := s.PersonByHandle(ctx, "brandiss")
	if err != nil {
		t.Fatal(err)
	}

	if err := s.LinkPerson(ctx, actor, task.ID, brandiss.ID, api.RoleAssignee, now); err != nil {
		t.Fatal(err)
	}
	linked, err := s.Get(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(linked.People) != 1 {
		t.Fatalf("people = %+v, want one link", linked.People)
	}

	res, err := s.Undo(ctx, actor, now)
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != api.KindTaskPeople {
		t.Errorf("reversed %s, want %s", res.Kind, api.KindTaskPeople)
	}
	if res.Task == nil || len(res.Task.People) != 0 {
		t.Errorf("people = %v after undo, want the link gone", res.Task)
	}
}

// TestAHandleIsWhatYouTypeAfterAt covers the validation, since the handle is
// what the filter grammar matches on.
func TestAHandleIsWhatYouTypeAfterAt(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()

	for _, bad := range []api.Person{
		{Handle: "two words", Name: "Nope"},
		{Handle: "has@sigil", Name: "Nope"},
		{Handle: "has#hash", Name: "Nope"},
		{Handle: "", Name: "Nope"},
		{Handle: "fine", Name: ""},
	} {
		if _, err := s.CreatePerson(ctx, bad, now); err == nil {
			t.Errorf("accepted handle %q name %q", bad.Handle, bad.Name)
		}
	}

	// A leading @ is stripped rather than refused: it is how people write it.
	person, err := s.CreatePerson(ctx, api.Person{Handle: "@jordan", Name: "Jordan"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if person.Handle != "jordan" {
		t.Errorf("handle = %q, want the sigil stripped", person.Handle)
	}

	// And the handle is unique, since a filter has to resolve to one person.
	if _, err := s.CreatePerson(ctx, api.Person{Handle: "jordan", Name: "Someone else"}, now); err == nil {
		t.Error("a duplicate handle was accepted")
	}

	// The new person is immediately findable by the grammar.
	if _, err := s.Create(ctx, actor, api.TaskCreate{
		Title: "brief jordan", People: []string{"jordan"},
	}, now); err != nil {
		t.Fatal(err)
	}
	found, err := s.List(ctx, "@jordan", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 {
		t.Errorf("@jordan matched %d tasks, want 1", len(found))
	}
}

func hasNum(tasks []api.Task, num int64) bool {
	for _, t := range tasks {
		if t.Num == num {
			return true
		}
	}
	return false
}

// TestWaitingDaysHandlesTheEdges keeps the age from going negative or wild.
func TestWaitingDaysHandlesTheEdges(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()

	task, err := s.GetByNum(ctx, 102)
	if err != nil {
		t.Fatal(err)
	}
	mikah, err := s.PersonByHandle(ctx, "mikah")
	if err != nil {
		t.Fatal(err)
	}

	waiting := api.StatusWaiting
	if _, err := s.Patch(ctx, actor, task.ID, api.TaskPatch{
		Status: &waiting, WaitingOn: &mikah.ID,
		Presence: map[string]bool{"waiting_on": true},
	}, "", now); err != nil {
		t.Fatal(err)
	}

	page, err := s.PersonPage(ctx, mikah.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	for i, age := range page.WaitingDays {
		if age < 0 {
			t.Errorf("task %d has a negative waiting age of %d", page.Waiting[i].Num, age)
		}
	}

	// One that started today reads as zero rather than as one.
	for i, task := range page.Waiting {
		if task.Num == 102 && page.WaitingDays[i] != 0 {
			t.Errorf("a task that started waiting today reads as %d days", page.WaitingDays[i])
		}
	}
}
