package tui_test

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	zone "github.com/lrstanley/bubblezone/v2"

	"github.com/harpchad/td/internal/tui"
)

func open(t *testing.T) (*harness, *fakeServer) {
	t.Helper()
	f := newFake(t)
	return newHarness(t, f, tui.Options{Mouse: true}), f
}

// plain strips the escape sequences so a test can read the screen the way a
// person does.
func plain(s string) string {
	var b strings.Builder
	inEscape := false
	for _, r := range s {
		switch {
		case r == 0x1b:
			inEscape = true
		case inEscape && (r == 'm' || r == 'K'):
			inEscape = false
		case !inEscape:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func lines(t *testing.T, h *harness) []string {
	t.Helper()
	return strings.Split(plain(h.view()), "\n")
}

// TestListShowsTheFixtureOrderWithTheSubtaskLifted covers the layout in
// section 11 and the display-order rule: 113 sorts seventh and displays
// fourth, indented under 101.
func TestListShowsTheFixtureOrderWithTheSubtaskLifted(t *testing.T) {
	h, _ := open(t)
	screen := lines(t, h)

	var nums []string
	for _, line := range screen {
		for _, n := range []string{"104", "102", "101", "113", "114", "108", "106", "103"} {
			if strings.Contains(line, " "+n+" ") {
				nums = append(nums, n)
				break
			}
		}
	}
	want := []string{"104", "102", "101", "113", "114", "108", "106", "103"}
	if strings.Join(nums, ",") != strings.Join(want, ",") {
		t.Errorf("row order = %v\nwant %v", nums, want)
	}

	// The child is indented under its parent.
	for _, line := range screen {
		if strings.Contains(line, "Draft cert renewal runbook") {
			if !strings.Contains(line, "  113") {
				t.Errorf("113 is not indented: %q", line)
			}
		}
	}
}

// TestEveryRowIsTheSameWidth covers the character grid. A row one cell wide
// breaks the box border for every row below it.
func TestEveryRowIsTheSameWidth(t *testing.T) {
	h, _ := open(t)
	screen := lines(t, h)

	width := len([]rune(screen[0]))
	for i, line := range screen {
		if got := len([]rune(line)); got != width {
			t.Errorf("line %d is %d cells, want %d: %q", i, got, width, line)
		}
	}
	if width != 80 {
		t.Errorf("screen width = %d, want the terminal's 80", width)
	}
	if len(screen) != 24 {
		t.Errorf("screen height = %d lines, want 24", len(screen))
	}
}

// TestTopBarCarriesTheCounts covers the reason the inbox is hidden from the
// home view: the count nags without cluttering.
func TestTopBarCarriesTheCounts(t *testing.T) {
	h, _ := open(t)
	top := lines(t, h)[0]

	if !strings.Contains(top, "td ─ Today") {
		t.Errorf("title bar = %q, want the saved filter's name", top)
	}
	// The fixture home view has one waiting task and one overdue task.
	for _, want := range []string{"waiting 1", "overdue 1"} {
		if !strings.Contains(top, want) {
			t.Errorf("title bar = %q, want it to carry %q", top, want)
		}
	}
}

func TestCursorMovesWithJAndK(t *testing.T) {
	h, _ := open(t)

	first := selectedRow(t, h)
	h.key("j")
	second := selectedRow(t, h)
	if first == second {
		t.Fatal("j did not move the cursor")
	}
	h.key("k")
	if back := selectedRow(t, h); back != first {
		t.Errorf("k did not move back: %q then %q", first, back)
	}

	h.key("G")
	last := selectedRow(t, h)
	if !strings.Contains(last, "103") {
		t.Errorf("G landed on %q, want the last row", last)
	}
	h.key("g")
	if top := selectedRow(t, h); top != first {
		t.Errorf("g landed on %q, want the first row", top)
	}
}

// selectedRow finds the inverse-video row, which is how selection is drawn.
func selectedRow(t *testing.T, h *harness) string {
	t.Helper()
	for _, line := range strings.Split(h.view(), "\n") {
		if strings.Contains(line, "\x1b[7m") {
			return plain(line)
		}
	}
	return ""
}

// TestSelectionIsInverseVideo covers the rule that focus and selection are
// inverse video, never a glow, an outline, or a background tint.
func TestSelectionIsInverseVideo(t *testing.T) {
	h, _ := open(t)

	view := h.view()
	if !strings.Contains(view, "\x1b[7m") {
		t.Fatal("no inverse video in the view, so the selection is drawn some other way")
	}
	count := strings.Count(view, "\x1b[7m")
	if count != 1 {
		t.Errorf("%d inverse-video runs, want exactly one selected row", count)
	}
}

// TestSpaceCompletesAndTheStatusLineSaysSo covers toggle-done plus the rule
// that a parent with open children reports them rather than cascading.
func TestSpaceCompletesTheTaskUnderTheCursor(t *testing.T) {
	h, f := open(t)

	h.key(" ")
	if len(f.completed) != 1 || f.completed[0] != "task-104" {
		t.Fatalf("completed = %v, want the row under the cursor", f.completed)
	}
	if !strings.Contains(plain(h.view()), "done") {
		t.Error("the status line does not say what happened")
	}
}

func TestCompletingAParentReportsItsOpenChildren(t *testing.T) {
	h, f := open(t)

	// Move to 101, which has one open child.
	h.key("j")
	h.key("j")
	h.key("d")

	if len(f.completed) != 1 || f.completed[0] != "task-101" {
		t.Fatalf("completed = %v", f.completed)
	}
	status := plain(h.view())
	if !strings.Contains(status, "1 subtask still open") {
		t.Error("the status line does not report the open child, so the client cannot prompt")
	}
}

// TestFoldHidesChildrenAndKeepsTheCount covers the fold rule: a collapsed
// parent hides its children and the count becomes the only signal they exist,
// which is why it is always drawn.
func TestFoldHidesChildrenAndKeepsTheCount(t *testing.T) {
	h, f := open(t)

	if !strings.Contains(plain(h.view()), "Draft cert renewal runbook") {
		t.Fatal("the child is not visible to begin with")
	}

	h.key("j")
	h.key("j")
	h.key("z")

	if !f.collapsed["task-101"] {
		t.Fatal("z did not fold the parent")
	}
	view := plain(h.view())
	if strings.Contains(view, "Draft cert renewal runbook") {
		t.Error("the child is still drawn under a collapsed parent")
	}
	if !strings.Contains(view, "0/1") {
		t.Error("the child count is gone, so nothing signals the children exist")
	}
	if !strings.Contains(view, "▸") {
		t.Error("the fold glyph does not show the collapsed state")
	}

	h.key("z")
	if f.collapsed["task-101"] {
		t.Error("z did not unfold")
	}
}

// TestAddParsesInlineTokens covers quick-add: same tokens as the filter
// grammar, parsed on the way in.
func TestAddParsesInlineTokens(t *testing.T) {
	h, f := open(t)

	h.key("a")
	if !strings.Contains(plain(h.view()), "add:") {
		t.Fatal("the add line is not showing")
	}
	for _, r := range "call the dealer #truck p:2" {
		h.key(string(r))
	}
	h.key("enter")

	if len(f.created) != 1 {
		t.Fatalf("created %d tasks, want 1", len(f.created))
	}
	got := f.created[0]
	if got.Title != "call the dealer" {
		t.Errorf("title = %q, want the tokens stripped out", got.Title)
	}
	if len(got.Tags) != 1 || got.Tags[0] != "truck" {
		t.Errorf("tags = %v", got.Tags)
	}
	if got.Priority == nil || *got.Priority != 2 {
		t.Errorf("priority = %v", got.Priority)
	}
	if got.ID == "" {
		t.Error("no client-generated id, so a retry could duplicate the row")
	}
}

// TestEscapeCancelsTheAddLine covers "one line, Escape cancels".
func TestEscapeCancelsTheAddLine(t *testing.T) {
	h, f := open(t)

	h.key("a")
	for _, r := range "never mind" {
		h.key(string(r))
	}
	h.key("esc")

	if len(f.created) != 0 {
		t.Errorf("escape still created %v", f.created)
	}
	if strings.Contains(plain(h.view()), "add:") {
		t.Error("the add line is still showing after escape")
	}
}

// TestFilterErrorsShowBeforeSending covers the reason both binaries share one
// parser: a typo reports its own message without a round trip.
func TestFilterErrorsShowBeforeSending(t *testing.T) {
	h, f := open(t)
	before := f.lastQuery

	h.key("/")
	// Clear the existing filter, then type a bad one.
	for range 40 {
		h.send(tea.KeyPressMsg{Code: tea.KeyBackspace})
	}
	for _, r := range "foo:bar" {
		h.key(string(r))
	}

	if !strings.Contains(plain(h.view()), `unknown filter key "foo"`) {
		t.Errorf("the parser's message is not on screen:\n%s", plain(h.view()))
	}

	h.key("enter")
	if f.lastQuery != before {
		t.Errorf("a filter that does not parse was sent anyway: %q", f.lastQuery)
	}
}

func TestSavedFilterKeys(t *testing.T) {
	h, f := open(t)

	h.key("3")
	if f.lastQuery != "is:inbox" {
		t.Errorf("3 sent %q, want the saved filter on slot 3", f.lastQuery)
	}
	if !strings.Contains(plain(lines(t, h)[0]), "Inbox") {
		t.Error("the title bar does not name the saved filter")
	}

	h.key("7")
	if !strings.Contains(plain(h.view()), "no saved filter on 7") {
		t.Error("an unbound slot says nothing")
	}
}

// TestEmptyStateNamesTheFilter covers the rule that empty is an invitation:
// it says what would put something there and names the current filter.
func TestEmptyStateNamesTheFilter(t *testing.T) {
	h, _ := open(t)

	h.key("/")
	for range 40 {
		h.send(tea.KeyPressMsg{Code: tea.KeyBackspace})
	}
	for _, r := range "#nosuchtag" {
		h.key(string(r))
	}
	h.key("enter")

	view := plain(h.view())
	if !strings.Contains(view, "Nothing matches") {
		t.Errorf("no empty state:\n%s", view)
	}
	if !strings.Contains(view, "#nosuchtag") {
		t.Error("the empty state does not name the filter that found nothing")
	}
	if !strings.Contains(view, "Press") {
		t.Error("the empty state does not say what would put something there")
	}
}

func TestUndoCallsTheServer(t *testing.T) {
	h, f := open(t)
	h.key("u")
	if f.undos != 1 {
		t.Errorf("undo calls = %d, want 1", f.undos)
	}
}

// TestDetailIsAFullScreenReplacement covers the choice against a split pane:
// splits stop working at 80 columns and on a phone.
func TestDetailIsAFullScreenReplacement(t *testing.T) {
	h, _ := open(t)

	h.key("enter")
	view := plain(h.view())

	if !strings.Contains(view, "Send Q3 numbers") {
		t.Fatal("the detail view does not show the task")
	}
	if strings.Contains(view, "Follow up on monday forms") {
		t.Error("another task is visible, so the detail is a pane rather than a replacement")
	}
	if !strings.Contains(view, "esc back") {
		t.Error("the detail view does not say how to get out")
	}

	h.key("esc")
	if !strings.Contains(plain(h.view()), "Follow up on monday forms") {
		t.Error("escape did not return to the list")
	}
}

// TestDeferredKeysSayWhereTheyLand covers the choice that a specified key
// which is not built yet reports itself rather than doing nothing, because a
// key that silently does nothing reads as a bug. It also guards against the
// message naming a phase that has already shipped.
func TestDeferredKeysSayWhereTheyLand(t *testing.T) {
	h, _ := open(t)

	h.key("w")
	view := plain(h.view())
	if !strings.Contains(view, "phase 6") {
		t.Errorf("w did nothing visible:\n%s", view)
	}
	if !strings.Contains(view, "waiting") {
		t.Error("the message does not say what the key would do")
	}

	// Editing has no phase in section 16's build order, so the key says that
	// rather than naming a phase nobody committed to. The first cut said
	// "phase 4", which shipped without it.
	h.key("e")
	view = plain(h.view())
	if !strings.Contains(view, "not scheduled") {
		t.Errorf("e claims a schedule it does not have:\n%s", view)
	}
}

// TestHelpNamesTheMouseCost covers the requirement to say out loud that
// capturing the mouse takes the terminal's own text selection away.
func TestHelpNamesTheMouseCost(t *testing.T) {
	h, _ := open(t)

	h.key("?")
	view := plain(h.view())

	if !strings.Contains(view, "text selection") {
		t.Error("the help does not mention losing text selection")
	}
	if !strings.Contains(view, "shift") {
		t.Error("the help does not say shift hands selection back")
	}
	if !strings.Contains(view, "--no-mouse") {
		t.Error("the help does not name the escape hatch")
	}
}

// TestStatusLineIsAToolbar covers the rule that every clickable thing shows
// its key hint and the bottom bar is a real status line.
func TestStatusLineIsAToolbar(t *testing.T) {
	h, _ := open(t)
	screen := lines(t, h)
	bottom := screen[len(screen)-2]

	for _, want := range []string{"a add", "d done", "z fold", "/ search", "u undo", "? keys"} {
		if !strings.Contains(bottom, want) {
			t.Errorf("status line = %q, want it to carry %q", bottom, want)
		}
	}
}

// TestMouseModeIsCellMotionOnTheView covers the two v2 facts CLAUDE.md warns
// about: mouse mode is a View field rather than a program option, and the
// mode is cell motion rather than all motion.
func TestMouseModeIsCellMotionOnTheView(t *testing.T) {
	f := newFake(t)

	on := newHarness(t, f, tui.Options{Mouse: true})
	if got := on.model.View().MouseMode; got != tea.MouseModeCellMotion {
		t.Errorf("MouseMode = %v, want cell motion", got)
	}
	if !on.model.View().AltScreen {
		t.Error("AltScreen is off, and bubblezone requires it")
	}

	off := newHarness(t, f, tui.Options{Mouse: false})
	if got := off.model.View().MouseMode; got != tea.MouseModeNone {
		t.Errorf("MouseMode with --no-mouse = %v, want none", got)
	}
}

// TestClickSelectsARow covers the pointer affordance, using the zones the
// renderer marked rather than column arithmetic.
func TestClickSelectsARow(t *testing.T) {
	h, _ := open(t)

	// Rendering registers the zones: View calls zone.Scan itself, so a test
	// must not scan the result again.
	_ = h.view()
	z := zone.Get("row:task-108")
	if z == nil || z.IsZero() {
		t.Fatal("no zone was marked for row 108, so clicks cannot find it")
	}

	h.send(tea.MouseClickMsg{X: z.StartX + 20, Y: z.StartY, Button: tea.MouseLeft})
	if got := selectedRow(t, h); !strings.Contains(got, "108") {
		t.Errorf("clicking row 108 selected %q", got)
	}
}

// TestClickingATagFiltersByIt covers the one thing the mouse does better than
// the keyboard here.
func TestClickingATagFiltersByIt(t *testing.T) {
	h, f := open(t)

	_ = h.view()
	z := zone.Get("tag:task-104:finance")
	if z == nil || z.IsZero() {
		t.Fatal("no zone was marked for the #finance tag")
	}

	h.send(tea.MouseClickMsg{X: z.StartX, Y: z.StartY, Button: tea.MouseLeft})
	if f.lastQuery != "#finance" {
		t.Errorf("clicking #finance sent %q", f.lastQuery)
	}
}

// TestClickingTheCheckboxCompletes covers the checkbox cell being its own hit
// region inside the row.
func TestClickingTheCheckboxCompletes(t *testing.T) {
	h, f := open(t)

	_ = h.view()
	z := zone.Get("check:task-102")
	if z == nil || z.IsZero() {
		t.Fatal("no zone was marked for the checkbox")
	}

	h.send(tea.MouseClickMsg{X: z.StartX + 1, Y: z.StartY, Button: tea.MouseLeft})
	if len(f.completed) != 1 || f.completed[0] != "task-102" {
		t.Errorf("completed = %v, want the clicked row", f.completed)
	}
}

// TestWheelScrollsWithoutMovingTheSelection covers the pager rule, and is the
// case that makes the wheel a viewport control rather than a cursor control.
func TestWheelScrollsWithoutMovingTheSelection(t *testing.T) {
	f := newFake(t)
	h := newHarness(t, f, tui.Options{Mouse: true})
	// A short window, so there is something to scroll.
	h.send(windowSize(80, 12))

	before := selectedRow(t, h)
	firstVisible := firstTaskLine(t, h)

	h.send(tea.MouseWheelMsg{Button: tea.MouseWheelDown})

	if after := selectedRow(t, h); after != before && after != "" {
		t.Errorf("the wheel moved the selection: %q then %q", before, after)
	}
	if nowVisible := firstTaskLine(t, h); nowVisible == firstVisible {
		t.Error("the wheel did not scroll the viewport")
	}
}

// firstTaskLine returns the first rendered task row.
func firstTaskLine(t *testing.T, h *harness) string {
	t.Helper()
	for _, line := range lines(t, h) {
		if strings.Contains(line, "[ ]") || strings.Contains(line, "[x]") || strings.Contains(line, "[~]") {
			return line
		}
	}
	return ""
}

// TestMouseOffIgnoresClicks covers --no-mouse and `mouse = false`.
func TestMouseOffIgnoresClicks(t *testing.T) {
	f := newFake(t)
	h := newHarness(t, f, tui.Options{Mouse: false})

	_ = h.view()
	before := selectedRow(t, h)
	h.send(tea.MouseClickMsg{X: 10, Y: 5, Button: tea.MouseLeft})

	if after := selectedRow(t, h); after != before {
		t.Errorf("a click moved the selection with the mouse off: %q then %q", before, after)
	}
}

// TestRightClickDoesNothing covers the decision against a right-click menu:
// terminal emulators intercept the right button inconsistently.
func TestRightClickDoesNothing(t *testing.T) {
	h, f := open(t)

	_ = h.view()
	z := zone.Get("check:task-102")
	h.send(tea.MouseClickMsg{X: z.StartX + 1, Y: z.StartY, Button: tea.MouseRight})

	if len(f.completed) != 0 {
		t.Errorf("a right click completed %v", f.completed)
	}
}

// TestOverdueIsTheOneColorException covers the rule that de-emphasis is
// dimming and overdue gets a paired color token instead.
func TestOverdueIsTheOneColorException(t *testing.T) {
	h, _ := open(t)

	// The overdue row sorts first and is therefore the selected one, and a
	// selected row is drawn as one inverse-video run with no inner color.
	// Move the cursor off it to see the token itself.
	h.key("j")

	for _, line := range strings.Split(h.view(), "\n") {
		if !strings.Contains(plain(line), "Send Q3 numbers") {
			continue
		}
		// ANSI 1 is the red the overdue token maps onto.
		if !strings.Contains(line, "\x1b[31m") && !strings.Contains(line, "\x1b[91m") {
			t.Errorf("the overdue row carries no color: %q", line)
		}
		return
	}
	t.Fatal("the overdue row was not rendered")
}
