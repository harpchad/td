package query_test

import (
	"testing"
	"time"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/query"
)

// sort: is an instruction about the answer rather than part of the question,
// which is why it comes back beside the tree instead of inside it.

func at(t *testing.T) time.Time {
	t.Helper()
	loc, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Fatal(err)
	}
	return time.Date(2026, 8, 5, 10, 0, 0, 0, loc)
}

// TestSortIsParsedBesideTheFilter. The want column is Sort.String, which
// renders the order the way it is written, so "" is the default order.
func TestSortIsParsedBesideTheFilter(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
	}{
		{"sort:due", "due"},
		{"sort:-due", "-due"},
		{"is:open sort:priority", "priority"},
		{"sort:created is:open", "created"},
		{"SORT:DUE", "due"},
		{"is:open", ""},
		{"sort:due,priority", "due,priority"},
		{"sort:due,-priority", "due,-priority"},
		{"sort:-due,created,num", "-due,created,num"},
		{"SORT:Due,-PRIORITY", "due,-priority"},
	} {
		q, err := query.ParseQueryAt(tc.in, at(t))
		if err != nil {
			t.Errorf("%q: %v", tc.in, err)
			continue
		}
		if got := q.Sort.String(); got != tc.want {
			t.Errorf("%q -> %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestSortAloneMatchesEverything. "sort:due" is not a filter, so it must not
// narrow anything: it is every task, in that order.
func TestSortAloneMatchesEverything(t *testing.T) {
	q, err := query.ParseQueryAt("sort:due", at(t))
	if err != nil {
		t.Fatal(err)
	}
	if q.Node != nil {
		t.Errorf("sort:due produced a predicate %T; it should match everything", q.Node)
	}
}

// TestABadSortSaysWhatIsAvailable, rather than becoming a search term that
// matches nothing.
func TestABadSortSaysWhatIsAvailable(t *testing.T) {
	for _, in := range []string{
		"sort:", "sort:urgency", "sort:-", "sort:due sort:priority",
		"sort:due,urgency", "sort:due,", "sort:due,,priority", "sort:,due",
		"sort:due,-",
		// The same key twice in one list is dead weight: the second can
		// never break a tie the first left, so it is a typo to report.
		"sort:due,due", "sort:due,-due",
		// Two sort: terms with different key lists conflict, same as ever.
		"sort:due,priority sort:due",
	} {
		if _, err := query.ParseQueryAt(in, at(t)); err == nil {
			t.Errorf("%q was accepted", in)
		}
	}
	// The same sort twice is a person repeating themselves, not a conflict.
	for _, in := range []string{"sort:due sort:due", "sort:due,-priority sort:due,-priority"} {
		if _, err := query.ParseQueryAt(in, at(t)); err != nil {
			t.Errorf("%q was refused: %v", in, err)
		}
	}
}

// TestAnExplicitSortDropsTheBuckets.
//
// The default order puts overdue first and due-today next, and ranks priority
// above the due date after that. Asking for sort:due is asking to be rid of
// all of it: the dates in order and nothing else.
func TestAnExplicitSortDropsTheBuckets(t *testing.T) {
	now := at(t)
	p1, p4 := 1, 4
	tasks := []api.Task{
		{Num: 1, Title: "overdue p4", DueAt: strp("2026-08-01"), Priority: &p4},
		{Num: 2, Title: "next month p1", DueAt: strp("2026-09-10"), Priority: &p1},
		{Num: 3, Title: "tomorrow p4", DueAt: strp("2026-08-06"), Priority: &p4},
		{Num: 4, Title: "no due date p1", Priority: &p1},
	}

	query.NewSorterFor(now, sortOf(t, "sort:due")).Sort(tasks)
	if got := nums(tasks); got != "1,3,2,4" {
		t.Errorf("sort:due = %s, want 1,3,2,4: dates ascending, no due date last", got)
	}

	query.NewSorterFor(now, sortOf(t, "sort:-due")).Sort(tasks)
	if got := nums(tasks); got != "2,3,1,4" {
		t.Errorf("sort:-due = %s, want 2,3,1,4: reversed, and no due date still last", got)
	}
}

// TestALaterKeyBreaksTheTiesTheEarlierOneLeft. sort:due,priority is the dates
// ascending, and within one date the priorities ascending. The tasks with no
// due date all tie on the first key, so the second orders them too.
func TestALaterKeyBreaksTheTiesTheEarlierOneLeft(t *testing.T) {
	now := at(t)
	p1, p2, p3, p4 := 1, 2, 3, 4
	tasks := []api.Task{
		{Num: 1, Title: "aug 10 p3", DueAt: strp("2026-08-10"), Priority: &p3},
		{Num: 2, Title: "aug 10 p1", DueAt: strp("2026-08-10"), Priority: &p1},
		{Num: 3, Title: "aug 9 p4", DueAt: strp("2026-08-09"), Priority: &p4},
		{Num: 4, Title: "no due p2", Priority: &p2},
		{Num: 5, Title: "no due p1", Priority: &p1},
		{Num: 6, Title: "aug 10 unset", DueAt: strp("2026-08-10")},
	}

	query.NewSorterFor(now, sortOf(t, "sort:due,priority")).Sort(tasks)
	if got := nums(tasks); got != "3,2,1,6,5,4" {
		t.Errorf("sort:due,priority = %s, want 3,2,1,6,5,4", got)
	}

	// Reversing the second key reverses only the tie-breaks; a task with no
	// priority still goes last within its date, and no due date still last
	// overall.
	query.NewSorterFor(now, sortOf(t, "sort:due,-priority")).Sort(tasks)
	if got := nums(tasks); got != "3,1,2,6,4,5" {
		t.Errorf("sort:due,-priority = %s, want 3,1,2,6,4,5", got)
	}
}

// TestMinusNumDescends. num is never missing, so the only thing a minus can
// mean is the numbers downward.
func TestMinusNumDescends(t *testing.T) {
	now := at(t)
	tasks := []api.Task{{Num: 2}, {Num: 3}, {Num: 1}}
	query.NewSorterFor(now, sortOf(t, "sort:-num")).Sort(tasks)
	if got := nums(tasks); got != "3,2,1" {
		t.Errorf("sort:-num = %s, want 3,2,1", got)
	}
}

// sortOf parses a query and hands back its order, so the behavior tests
// exercise the same path a typed filter takes.
func sortOf(t *testing.T, in string) query.Sort {
	t.Helper()
	q, err := query.ParseQueryAt(in, at(t))
	if err != nil {
		t.Fatal(err)
	}
	return q.Sort
}

// TestReversingDoesNotPromoteTheEmptyOnes. A task with no due date has no
// answer to "when", and reversing the order is not a reason to put it first.
func TestReversingDoesNotPromoteTheEmptyOnes(t *testing.T) {
	now := at(t)
	tasks := []api.Task{
		{Num: 1, Title: "no due"},
		{Num: 2, Title: "has one", DueAt: strp("2026-08-09")},
	}
	for _, in := range []string{"sort:due", "sort:-due"} {
		query.NewSorterFor(now, sortOf(t, in)).Sort(tasks)
		if tasks[len(tasks)-1].Num != 1 {
			t.Errorf("%s put the task with no due date at %v, want last", in, nums(tasks))
		}
	}
}

// TestTheDefaultOrderIsUnchanged. Every list that does not ask for an order
// gets exactly what it got before.
func TestTheDefaultOrderIsUnchanged(t *testing.T) {
	now := at(t)
	p1, p4 := 1, 4
	tasks := []api.Task{
		{Num: 2, Title: "next month p1", DueAt: strp("2026-09-10"), Priority: &p1},
		{Num: 1, Title: "overdue p4", DueAt: strp("2026-08-01"), Priority: &p4},
	}
	query.NewSorterFor(now, query.Sort{}).Sort(tasks)
	if got := nums(tasks); got != "1,2" {
		t.Errorf("the default order = %s, want overdue first", got)
	}
}

func strp(s string) *string { return &s }

func nums(tasks []api.Task) string {
	out := ""
	for i, t := range tasks {
		if i > 0 {
			out += ","
		}
		out += itoa(t.Num)
	}
	return out
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
