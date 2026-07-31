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

// TestEditingTheSeriesIsItsOwnAction covers section 3's rule that editing an
// instance edits that instance and editing the series needs an explicit
// action. E is that action, and nothing else in the keymap reaches the rule.
func TestEditingTheSeriesIsItsOwnAction(t *testing.T) {
	h, f := open(t)

	h.key("E")
	if !strings.Contains(plain(h.view()), "repeats:") {
		t.Fatalf("E did not open the series prompt:\n%s", plain(h.view()))
	}

	h.typeText("every 2 weeks")
	h.key("enter")

	if len(f.series) != 1 {
		t.Fatalf("%d series calls, want 1", len(f.series))
	}
	if got := f.series[0]["rrule"]; got != "FREQ=WEEKLY;INTERVAL=2" {
		t.Errorf("rrule = %v", got)
	}

	// The instance itself was not patched. That is the whole distinction.
	for _, patch := range f.patched {
		if _, ok := patch["title"]; ok {
			t.Error("editing the series patched the instance")
		}
	}
}

// TestARuleThatDoesNotParseSaysSo keeps a typo from becoming a year of the
// wrong thing repeating.
func TestARuleThatDoesNotParseSaysSo(t *testing.T) {
	h, f := open(t)

	h.key("E")
	h.typeText("every fortnight")
	h.key("enter")

	if len(f.series) != 0 {
		t.Errorf("an unparseable rule was sent: %v", f.series)
	}
	if !strings.Contains(plain(h.view()), "fortnight") {
		t.Errorf("the error does not name the word:\n%s", plain(h.view()))
	}
}

// TestWaitingNeedsAPerson covers the rule that waiting needs the person link.
func TestWaitingNeedsAPerson(t *testing.T) {
	h, f := open(t)

	h.key("w")
	if !strings.Contains(plain(h.view()), "waiting on:") {
		t.Fatal("w did not open the waiting editor")
	}
	for _, r := range "mikah" {
		h.key(string(r))
	}
	h.key("enter")

	if len(f.patched) != 1 {
		t.Fatalf("%d patches, want 1", len(f.patched))
	}
	if got := f.patched[0]["status"]; got != "waiting" {
		t.Errorf("status = %v, want waiting", got)
	}
	if f.patched[0]["waiting_on"] == nil {
		t.Error("the task moved to waiting with nobody attached")
	}
}

// TestLinkingAPersonTakesARole covers @ and its optional role.
func TestLinkingAPersonTakesARole(t *testing.T) {
	h, f := open(t)

	h.key("@")
	for _, r := range "stacey:assignee" {
		h.key(string(r))
	}
	h.key("enter")

	if len(f.linked) != 1 {
		t.Fatalf("%d links, want 1", len(f.linked))
	}
	if f.linked[0].person != "stacey" || f.linked[0].role != "assignee" {
		t.Errorf("link = %+v", f.linked[0])
	}

	// A bare handle is an involved link, which is the softest one.
	h.key("@")
	for _, r := range "mikah" {
		h.key(string(r))
	}
	h.key("enter")
	if len(f.linked) != 2 || f.linked[1].role != "involved" {
		t.Errorf("a bare handle linked as %+v", f.linked)
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

// TestEditRoundTripsThroughTheCaptureGrammar covers the choice to edit a task
// as one line rather than as a form. The row already reads as the grammar, so
// editing it as the grammar is one thing to learn rather than two.
func TestEditRoundTripsThroughTheCaptureGrammar(t *testing.T) {
	h, f := open(t)

	h.key("e")
	view := plain(h.view())
	if !strings.Contains(view, "edit:") {
		t.Fatalf("e did not open an editor:\n%s", view)
	}
	// Prefilled with the task in the form quick-add accepts.
	for _, want := range []string{"Send Q3 numbers", "#finance", "@stacey", "p1", "due:"} {
		if !strings.Contains(view, want) {
			t.Errorf("the edit line is missing %q:\n%s", want, view)
		}
	}

	// Escape leaves the task alone.
	h.key("esc")
	if len(f.patched) != 0 {
		t.Errorf("escape still patched %v", f.patched)
	}
}

// TestPriorityPromptTakesOneToFour covers p, and the empty value that clears.
func TestPriorityPromptTakesOneToFour(t *testing.T) {
	h, f := open(t)

	h.key("p")
	if !strings.Contains(plain(h.view()), "priority:") {
		t.Fatal("p did not open the priority editor")
	}
	// The current value is prefilled, so changing it is one keystroke.
	if !strings.Contains(plain(h.view()), "1") {
		t.Error("the priority editor is not prefilled with the current value")
	}

	h.send(tea.KeyPressMsg{Code: tea.KeyBackspace})
	h.key("3")
	h.key("enter")

	if len(f.patched) != 1 {
		t.Fatalf("%d patches, want 1", len(f.patched))
	}
	if got := f.patched[0]["priority"]; got != float64(3) {
		t.Errorf("priority = %v, want 3", got)
	}
}

// TestPriorityRejectsNonsenseWithoutSending covers the validation happening
// before the round trip, the same as the filter bar's.
func TestPriorityRejectsNonsenseWithoutSending(t *testing.T) {
	h, f := open(t)

	h.key("p")
	h.send(tea.KeyPressMsg{Code: tea.KeyBackspace})
	h.key("9")
	h.key("enter")

	if len(f.patched) != 0 {
		t.Errorf("an invalid priority was sent: %v", f.patched)
	}
	if !strings.Contains(plain(h.view()), "1 to 4") {
		t.Error("the status line does not say what a priority may be")
	}
}

// TestSnoozeTakesADurationOrADate covers both forms the API accepts.
func TestSnoozeTakesADurationOrADate(t *testing.T) {
	h, f := open(t)

	h.key("s")
	for _, r := range "2h" {
		h.key(string(r))
	}
	h.key("enter")

	if len(f.snoozed) != 1 {
		t.Fatalf("%d snoozes, want 1", len(f.snoozed))
	}
	if f.snoozed[0].Duration != "2h" {
		t.Errorf("snooze = %+v, want a 2h duration", f.snoozed[0])
	}

	h.key("s")
	for _, r := range "friday" {
		h.key(string(r))
	}
	h.key("enter")
	if len(f.snoozed) != 2 {
		t.Fatalf("%d snoozes, want 2", len(f.snoozed))
	}
	if f.snoozed[1].Until == "" {
		t.Errorf("a date snooze sent %+v, want an absolute instant", f.snoozed[1])
	}
}

// TestNotesGetATextarea covers the one multi-line thing in the product.
func TestNotesGetATextarea(t *testing.T) {
	h, f := open(t)

	h.key("N")
	view := plain(h.view())
	if !strings.Contains(view, "notes on") {
		t.Fatalf("N did not open the notes editor:\n%s", view)
	}
	if !strings.Contains(view, "ctrl+s save") {
		t.Error("the notes editor does not say how to save")
	}

	for _, r := range "quoted 780" {
		h.key(string(r))
	}
	// No Text: a real ctrl+s from a terminal carries a code and a modifier,
	// and String() returns the text when there is one.
	h.send(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})

	if len(f.patched) != 1 {
		t.Fatalf("%d patches, want 1", len(f.patched))
	}
	notes, _ := f.patched[0]["notes"].(string)
	if !strings.Contains(notes, "quoted 780") {
		t.Errorf("notes = %q", notes)
	}
}

// TestEscapeCancelsAnEdit covers the rule that every one-line editor is
// cancelled the same way.
func TestEscapeCancelsAnEdit(t *testing.T) {
	h, f := open(t)

	for _, key := range []string{"e", "p", "t", "s", "N"} {
		h.key(key)
		h.key("esc")
	}
	if len(f.patched) != 0 || len(f.snoozed) != 0 {
		t.Errorf("escape still sent something: %v %v", f.patched, f.snoozed)
	}
	// And the list is showing again.
	if !strings.Contains(plain(h.view()), "Send Q3 numbers") {
		t.Error("escape did not return to the list")
	}
}

// TestTriageIsOneTaskAtATime covers section 7's requirement that triage is a
// dedicated mode rather than a view. Getting from 20 to 0 should take two
// minutes, and that only works if one decision is on screen at a time.
func TestTriageIsOneTaskAtATime(t *testing.T) {
	h, f := open(t)

	h.key("T")
	view := plain(h.view())
	// The card's own footer, not the word in the toolbar hint.
	if !strings.Contains(view, "1-4 priority") {
		t.Fatalf("T did not open triage:\n%s", view)
	}
	// The inbox is what triage works, and it asked for exactly that.
	if f.lastQuery != "is:inbox" {
		t.Errorf("triage queried %q, want is:inbox", f.lastQuery)
	}

	// Only one task's title is showing, not the list.
	titles := 0
	for _, task := range f.tasks {
		if strings.Contains(view, task.Title) {
			titles++
		}
	}
	if titles > 1 {
		t.Errorf("%d tasks on screen at once in triage", titles)
	}
}

// TestTriagePriorityPromotesInOneKeystroke is the reason triage is fast: a
// priority is one of the two things that lets a task leave the inbox, so
// setting one both sets it and promotes.
func TestTriagePriorityPromotesInOneKeystroke(t *testing.T) {
	h, f := open(t)

	h.key("T")
	h.key("2")

	if len(f.patched) == 0 {
		t.Fatal("no patch was sent")
	}
	last := f.patched[len(f.patched)-1]
	if last["priority"] != float64(2) {
		t.Errorf("priority = %v, want 2", last["priority"])
	}
	if last["status"] != "todo" {
		t.Errorf("status = %v, want the task out of the inbox", last["status"])
	}
}

// TestTriageSaysWhenItIsEmpty, because a blank screen at the end of a queue
// reads as a crash.
func TestTriageSaysWhenItIsEmpty(t *testing.T) {
	h, f := open(t)
	f.tasks = nil

	h.key("T")
	if !strings.Contains(plain(h.view()), "Inbox zero") {
		t.Errorf("an empty triage queue says nothing:\n%s", plain(h.view()))
	}
}

// TestEscapeLeavesTriage keeps the mode from being a trap.
func TestEscapeLeavesTriage(t *testing.T) {
	h, _ := open(t)

	h.key("T")
	h.key("esc")
	if strings.Contains(plain(h.view()), "1-4 priority") {
		t.Error("escape did not leave triage")
	}
	if !strings.Contains(plain(h.view()), "Follow up on monday forms") {
		t.Error("escape did not return to the list")
	}
}
