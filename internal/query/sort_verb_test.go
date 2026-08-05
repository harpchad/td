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

// TestSortIsParsedBesideTheFilter.
func TestSortIsParsedBesideTheFilter(t *testing.T) {
	for _, tc := range []struct {
		in   string
		key  string
		desc bool
	}{
		{"sort:due", "due", false},
		{"sort:-due", "due", true},
		{"is:open sort:priority", "priority", false},
		{"sort:created is:open", "created", false},
		{"SORT:DUE", "due", false},
		{"is:open", "", false},
	} {
		q, err := query.ParseQueryAt(tc.in, at(t))
		if err != nil {
			t.Errorf("%q: %v", tc.in, err)
			continue
		}
		if q.Sort.Key != tc.key || q.Sort.Desc != tc.desc {
			t.Errorf("%q -> %+v, want key %q desc %v", tc.in, q.Sort, tc.key, tc.desc)
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
	for _, in := range []string{"sort:", "sort:urgency", "sort:-", "sort:due sort:priority"} {
		if _, err := query.ParseQueryAt(in, at(t)); err == nil {
			t.Errorf("%q was accepted", in)
		}
	}
	// The same key twice is a person repeating themselves, not a conflict.
	if _, err := query.ParseQueryAt("sort:due sort:due", at(t)); err != nil {
		t.Errorf("the same sort twice was refused: %v", err)
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

	query.NewSorterFor(now, query.Sort{Key: "due"}).Sort(tasks)
	if got := nums(tasks); got != "1,3,2,4" {
		t.Errorf("sort:due = %s, want 1,3,2,4: dates ascending, no due date last", got)
	}

	query.NewSorterFor(now, query.Sort{Key: "due", Desc: true}).Sort(tasks)
	if got := nums(tasks); got != "2,3,1,4" {
		t.Errorf("sort:-due = %s, want 2,3,1,4: reversed, and no due date still last", got)
	}
}

// TestReversingDoesNotPromoteTheEmptyOnes. A task with no due date has no
// answer to "when", and reversing the order is not a reason to put it first.
func TestReversingDoesNotPromoteTheEmptyOnes(t *testing.T) {
	now := at(t)
	tasks := []api.Task{
		{Num: 1, Title: "no due"},
		{Num: 2, Title: "has one", DueAt: strp("2026-08-09")},
	}
	for _, desc := range []bool{false, true} {
		query.NewSorterFor(now, query.Sort{Key: "due", Desc: desc}).Sort(tasks)
		if tasks[len(tasks)-1].Num != 1 {
			t.Errorf("desc=%v put the task with no due date at %v, want last", desc, nums(tasks))
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
