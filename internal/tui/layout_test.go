package tui_test

import (
	"strings"
	"testing"

	"github.com/harpchad/td/internal/tui"
)

// TestColumnsLineUp covers the rule that everything lands on a character
// grid. Each of the title, tokens, child count, and due date gets a column,
// so the eye can run down one of them instead of tracking a ragged edge.
func TestColumnsLineUp(t *testing.T) {
	f := newFake(t)
	h := newHarness(t, f, tui.Options{Mouse: true})
	h.send(windowSize(78, 14))

	var dueEnds, titleStarts []int
	for _, line := range lines(t, h) {
		idx := strings.Index(line, "[")
		if idx < 0 || !strings.Contains(line, "]") {
			continue
		}
		// The title starts after the checkbox, number, and priority columns.
		body := []rune(line)
		titleStarts = append(titleStarts, idx)

		// Only rows that carry a date say anything about where the date
		// column ends. A row with no due date leaves it blank, which is the
		// column doing its job rather than a misalignment.
		if !hasDue(line) {
			continue
		}
		trimmed := strings.TrimRight(string(body[:len(body)-1]), " ")
		dueEnds = append(dueEnds, len([]rune(trimmed)))
	}

	if len(dueEnds) < 5 {
		t.Fatalf("only %d task rows rendered", len(dueEnds))
	}
	for i, got := range dueEnds {
		if got != dueEnds[0] {
			t.Errorf("row %d ends its due column at %d, want %d: dates do not line up",
				i, got, dueEnds[0])
		}
	}
	// The checkbox column is fixed too, apart from the one indented subtask.
	counts := map[int]int{}
	for _, start := range titleStarts {
		counts[start]++
	}
	if len(counts) > 2 {
		t.Errorf("checkboxes sit at %d different columns, want one plus the subtask indent", len(counts))
	}
}

// hasDue reports whether a rendered row carries a date in its due column.
func hasDue(line string) bool {
	if strings.Contains(line, "Today") {
		return true
	}
	for _, month := range []string{
		"Jan", "Feb", "Mar", "Apr", "May", "Jun",
		"Jul", "Aug", "Sep", "Oct", "Nov", "Dec",
	} {
		if strings.Contains(line, month+" ") {
			return true
		}
	}
	return false
}

// TestStatusBarReadsInSpecOrder covers the bottom bar as section 11 draws it.
func TestStatusBarReadsInSpecOrder(t *testing.T) {
	f := newFake(t)
	h := newHarness(t, f, tui.Options{Mouse: true})

	screen := lines(t, h)
	bar := screen[len(screen)-2]

	want := []string{"a add", "d done", "z fold", "w wait", "/ search", "u undo", "? keys"}
	at := -1
	for _, hint := range want {
		idx := strings.Index(bar, hint)
		if idx < 0 {
			t.Fatalf("status bar = %q, missing %q", bar, hint)
		}
		if idx < at {
			t.Errorf("status bar = %q, %q is out of order", bar, hint)
		}
		at = idx
	}
}
