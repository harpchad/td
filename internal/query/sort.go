package query

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/harpchad/td/internal/api"
)

// LocalDate reduces a stored date value to a calendar date in loc. A stored
// value is either a bare YYYY-MM-DD, which is already a calendar date and has
// no zone to convert, or an RFC3339 instant, whose date component is taken
// after converting into loc. Returns "" when the value is empty or
// unparseable.
//
// Every date comparison in td goes through this. A container running UTC
// while the user lives in Central is the likeliest source of an
// off-by-one-day bug in the whole system, and this is the single place that
// decides it.
func LocalDate(value string, loc *time.Location) string {
	if value == "" {
		return ""
	}
	if len(value) == len(DateLayout) {
		if _, err := time.Parse(DateLayout, value); err == nil {
			return value
		}
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return ""
	}
	return t.In(loc).Format(DateLayout)
}

// Sorter applies the default order. It is one function shared by the API, the
// TUI, and the web UI, so the same filter produces the same list everywhere.
//
// The keys, in order:
//
//  1. bucket: overdue, due today, everything else
//  2. within overdue, due date ascending, so the most overdue sorts first
//  3. priority ascending, unset sorts last
//  4. due date ascending, no due date sorts last
//  5. created_at ascending
//  6. num ascending, as a total-order tiebreak
//
// Bucket outranks priority, so a P4 due today sorts above a P1 due tomorrow.
// That looks like a bug in a screenshot and is not one.
type Sorter struct {
	Today string
	Loc   *time.Location
	// Order is an explicit sort from a sort: term. Zero is the default above.
	Order Sort
}

// NewSorter builds a Sorter anchored on now, using now's location for every
// date comparison.
func NewSorter(now time.Time) Sorter {
	return Sorter{Today: now.Format(DateLayout), Loc: now.Location()}
}

// NewSorterFor builds a Sorter for an explicit order. A zero Sort gives the
// default comparator, so a query with no sort: is unchanged.
func NewSorterFor(now time.Time, order Sort) Sorter {
	out := NewSorter(now)
	out.Order = order
	return out
}

// Sort orders tasks in place.
func (s Sorter) Sort(tasks []api.Task) {
	if s.Order.Explicit() {
		sort.SliceStable(tasks, func(i, j int) bool {
			return s.lessExplicit(&tasks[i], &tasks[j])
		})
		return
	}
	sort.SliceStable(tasks, func(i, j int) bool {
		return s.Less(&tasks[i], &tasks[j])
	})
}

// lessExplicit is the comparator for a sort: term.
//
// Deliberately not the default with one key moved to the front. Asking for an
// order is asking to be rid of the clever one, so there are no overdue and
// due-today buckets here: sort:due is the dates in order and nothing else.
//
// Two things hold in both directions. A task with nothing in the sorted field
// goes last, because reversing an order should not promote the rows that have
// no answer to the top. And num is the final tiebreak, so the order is total
// and two runs over the same data return the same list.
func (s Sorter) lessExplicit(a, b *api.Task) bool {
	av, bv, ok := s.sortKey(a, b)
	if !ok {
		return a.Num < b.Num
	}

	// Missing values sort last whichever way round the rest is.
	switch {
	case av == "" && bv == "":
		return a.Num < b.Num
	case av == "":
		return false
	case bv == "":
		return true
	}

	if av == bv {
		return a.Num < b.Num
	}
	if s.Order.Desc {
		return av > bv
	}
	return av < bv
}

// sortKey renders the field being sorted on as a comparable string. Priority
// is zero padded so "10" would not sort before "2", which it cannot today and
// will the day priorities go past 9.
func (s Sorter) sortKey(a, b *api.Task) (string, string, bool) {
	switch s.Order.Key {
	case "due":
		return s.dueDate(a), s.dueDate(b), true
	case "priority":
		return priorityText(a.Priority), priorityText(b.Priority), true
	case "created":
		return a.CreatedAt, b.CreatedAt, true
	case "title":
		return strings.ToLower(strings.TrimSpace(a.Title)),
			strings.ToLower(strings.TrimSpace(b.Title)), true
	case "num":
		return "", "", false
	}
	return "", "", false
}

// priorityText renders a priority for comparison. Unset renders empty, which
// the caller sorts last, and that is the same rule the default order uses.
func priorityText(p *int) string {
	if p == nil {
		return ""
	}
	return fmt.Sprintf("%03d", *p)
}

// Less reports whether a sorts before b under the default order. The order is
// total: num is the final tiebreak, so no two rows compare equal and two runs
// over the same data return the same list.
func (s Sorter) Less(a, b *api.Task) bool {
	ad, bd := s.dueDate(a), s.dueDate(b)
	ab, bb := s.bucket(ad), s.bucket(bd)
	if ab != bb {
		return ab < bb
	}

	// Key 2 applies only inside the overdue bucket, where the most overdue
	// comes first regardless of priority.
	if ab == bucketOverdue && ad != bd {
		return ad < bd
	}

	ap, bp := priorityKey(a.Priority), priorityKey(b.Priority)
	if ap != bp {
		return ap < bp
	}

	if ad != bd {
		// An empty due date sorts last rather than first, which is the
		// opposite of what a plain string compare would do.
		if ad == "" {
			return false
		}
		if bd == "" {
			return true
		}
		return ad < bd
	}

	if c := compareInstants(a.CreatedAt, b.CreatedAt); c != 0 {
		return c < 0
	}

	return a.Num < b.Num
}

const (
	bucketOverdue = 0
	bucketToday   = 1
	bucketRest    = 2
)

func (s Sorter) dueDate(t *api.Task) string {
	if t.DueAt == nil {
		return ""
	}
	return LocalDate(*t.DueAt, s.Loc)
}

func (s Sorter) bucket(due string) int {
	switch {
	case due == "":
		return bucketRest
	case due < s.Today:
		return bucketOverdue
	case due == s.Today:
		return bucketToday
	default:
		return bucketRest
	}
}

// priorityKey maps an unset priority above the highest real one so it sorts
// last under an ascending compare.
func priorityKey(p *int) int {
	if p == nil {
		return 1 << 30
	}
	return *p
}

// compareInstants orders two RFC3339 timestamps by the instant they name.
// Comparing the strings directly is wrong once two rows carry different UTC
// offsets.
func compareInstants(a, b string) int {
	at, aerr := time.Parse(time.RFC3339, a)
	bt, berr := time.Parse(time.RFC3339, b)
	if aerr != nil || berr != nil {
		switch {
		case a < b:
			return -1
		case a > b:
			return 1
		default:
			return 0
		}
	}
	switch {
	case at.Before(bt):
		return -1
	case at.After(bt):
		return 1
	default:
		return 0
	}
}
