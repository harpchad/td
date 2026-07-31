package tui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	zone "github.com/lrstanley/bubblezone/v2"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/query"
)

// View renders the whole screen.
//
// Two chrome rows at the top, one at the bottom, everything else is list. The
// mouse mode and the alt screen are fields on the returned View in Bubble Tea
// v2 rather than options passed to the program, which is why they are set
// here on every render.
func (m *Model) View() tea.View {
	v := tea.NewView("")
	v.AltScreen = true

	// Cell motion, not all motion. All-motion sends an event for every
	// pointer move, which floods the event loop over SSH and buys only hover,
	// and there is no hover in the TUI by design.
	if m.mouseEnabled {
		v.MouseMode = tea.MouseModeCellMotion
	}
	if m.quitting {
		return v
	}

	var body string
	switch m.mode {
	case modeHelp:
		body = m.renderHelp()
	case modeDetail:
		body = m.renderDetail()
	default:
		body = m.renderList()
	}

	// Scan resolves the marks the render left into screen coordinates. It has
	// to run over the final composed string, once, after everything is drawn.
	v.SetContent(zone.Scan(body))
	return v
}

// renderList draws the home view: title rule, filter row, the list, and the
// status line, inside a box.
func (m *Model) renderList() string {
	width := max(m.width, 40)
	inner := width - 2

	lines := make([]string, 0, m.height)
	lines = append(lines, m.renderTitleRule(width))
	lines = append(lines, m.boxed(inner, m.renderFilterRow(inner)))
	lines = append(lines, m.rule(width, "├", "┤"))

	h := m.listHeight()
	rows := m.renderRows(inner)
	for i := range h {
		if i < len(rows) {
			lines = append(lines, m.boxed(inner, rows[i]))
		} else {
			lines = append(lines, m.boxed(inner, ""))
		}
	}

	lines = append(lines, m.rule(width, "├", "┤"))
	lines = append(lines, m.boxed(inner, m.renderStatusLine(inner)))
	lines = append(lines, m.rule(width, "└", "┘"))

	if m.mode == modeAdd {
		// The add line replaces the status line rather than opening a pane:
		// one line, Escape cancels.
		lines[len(lines)-2] = m.boxed(inner, truncate(m.addInput.View(), inner))
	}
	if m.mode == modeFilter {
		lines[1] = m.boxed(inner, truncate(m.filterInput.View(), inner))
	}

	return strings.Join(lines, "\n")
}

// renderTitleRule is the top border with the title inset into it, and the
// counts inset at the right. The inbox count is the entire point of hiding
// the inbox from the home view: it nags without cluttering.
func (m *Model) renderTitleRule(width int) string {
	title := " td ─ " + m.describeFilter() + " "

	c := m.counts()
	var parts []string
	if c.inbox > 0 {
		parts = append(parts, "inbox "+itoa(int64(c.inbox)))
	}
	if c.waiting > 0 {
		parts = append(parts, "waiting "+itoa(int64(c.waiting)))
	}
	if c.overdue > 0 {
		parts = append(parts, "overdue "+itoa(int64(c.overdue)))
	}
	right := ""
	if len(parts) > 0 {
		right = " " + strings.Join(parts, " ─ ") + " "
	}

	fill := width - 2 - lipgloss.Width(title) - lipgloss.Width(right)
	if fill < 0 {
		title = truncate(title, width-2)
		fill, right = 0, ""
	}
	return dim.Render("┌") + base.Render(title) +
		dim.Render(strings.Repeat("─", fill)) + dimIfEmpty(right, c) + dim.Render("┐")
}

// dimIfEmpty renders the counts, with overdue in the one paired color token
// that de-emphasis-by-dimming is not allowed to swallow.
func dimIfEmpty(right string, c counts) string {
	if right == "" {
		return ""
	}
	if c.overdue > 0 {
		idx := strings.Index(right, "overdue")
		return dim.Render(right[:idx]) + overdue.Render(right[idx:])
	}
	return dim.Render(right)
}

func (m *Model) rule(width int, left, rightCap string) string {
	if width < 2 {
		return ""
	}
	return dim.Render(left + strings.Repeat("─", width-2) + rightCap)
}

func (m *Model) boxed(inner int, content string) string {
	return dim.Render("│") + pad(content, inner) + dim.Render("│")
}

func (m *Model) renderFilterRow(inner int) string {
	text := m.filter
	if strings.TrimSpace(text) == "" {
		text = "(everything)"
	}
	// Marked so a click lands on it: clicking the filter bar edits it.
	return zone.Mark(zoneFilter, dim.Render(" filter: ")+truncate(text, inner-9))
}

// renderRows draws the visible window of the list.
func (m *Model) renderRows(inner int) []string {
	if m.loading && len(m.rows) == 0 {
		// Loading is nothing. Requests are local, and if one is slow the
		// status line names what it is waiting on, in words.
		return nil
	}
	if len(m.rows) == 0 {
		return m.renderEmpty(inner)
	}

	out := make([]string, 0, m.listHeight())
	for _, rv := range m.visibleRows() {
		out = append(out, m.renderRow(rv.Row, rv.Index == m.cursor, inner))
	}
	return out
}

// renderEmpty is an invitation rather than a shrug. It says what would put
// something here and names the filter that found nothing.
func (m *Model) renderEmpty(inner int) []string {
	var lines []string
	switch {
	case m.err != nil:
		return nil
	case strings.TrimSpace(m.filter) == "":
		lines = []string{"", " Nothing here yet.", " Press a to put the first thing in the inbox."}
	case m.filter == "is:inbox":
		lines = []string{"", " Inbox zero.", " Nothing waiting to be triaged."}
	default:
		lines = []string{
			"",
			" Nothing matches " + truncate(m.filter, inner-20),
			" Press / to change the filter, or a to add something that would match.",
		}
	}
	for i := range lines {
		lines[i] = dim.Render(truncate(lines[i], inner))
	}
	return lines
}

// Column widths for a task row. The due column is fixed so dates line up
// down the list, and the child count sits beside it so a folded parent's
// only signal is always in the same place.
const (
	dueWidth      = 7
	childWidth    = 5
	minTitleWidth = 16
)

// renderRow draws one task line: fold cell, checkbox, number, priority,
// title, tags and people, child count, due date.
//
// A selected row is built without its inner styles rather than having them
// stripped afterwards. Two reasons, and the second is the one that bites.
// Inverting a string that already carries colors leaves the first inner reset
// cancelling the inversion for the rest of the line. And bubblezone marks its
// hit regions with escape sequences, so stripping a rendered row would take
// every clickable region on it away, leaving the selected row the one row the
// mouse could not touch.
func (m *Model) renderRow(row query.Row, isCursor bool, inner int) string {
	t := row.Task

	// paint applies a style, or does not, depending on whether this row is
	// about to be inverted.
	paint := func(st lipgloss.Style, text string) string {
		if isCursor {
			return text
		}
		return st.Render(text)
	}

	fold := " "
	if t.ChildrenTotal > 0 {
		glyph := "▾"
		if m.collapsed[t.ID] {
			glyph = "▸"
		}
		fold = zone.Mark(zoneFold+t.ID, glyph)
	}

	check := zone.Mark(zoneCheckbox+t.ID, checkbox(t.Status))
	indent := strings.Repeat("  ", row.Depth)

	num := paint(dim, padRight(itoa(t.Num), 4))
	prio := paint(priorityStyle(t.Priority), padRight(priorityLabel(t.Priority), 2))

	// Tags and people get no border, no fill, and no padding. Dim is the
	// entire treatment: the sigils already mark them as structured values.
	tokens := make([]string, 0, len(t.People)+len(t.Tags))
	for _, p := range t.People {
		tokens = append(tokens, zone.Mark(zonePerson+t.ID+":"+p.PersonID+":"+p.Role,
			paint(dim, "@"+firstWordLower(p.Name))))
	}
	for _, tag := range t.Tags {
		tokens = append(tokens, zone.Mark(zoneTag+t.ID+":"+tag, paint(dim, "#"+tag)))
	}
	tokenText := strings.Join(tokens, " ")

	children := ""
	if t.ChildrenTotal > 0 {
		// Always drawn: when a parent is collapsed this count is the only
		// signal the children exist.
		children = paint(dim, itoa(int64(t.ChildrenDone))+"/"+itoa(int64(t.ChildrenTotal)))
	}

	due := m.renderDue(t, isCursor)

	// Everything lands on a character grid: the title, the tokens, the child
	// count, and the due date each get a column, so the eye can run down one
	// of them instead of tracking a ragged right edge.
	left := " " + fold + check + " " + indent + num + " " + prio + " "
	available := inner - lipgloss.Width(left) - childWidth - dueWidth

	titleWidth := available * 3 / 5
	if titleWidth < minTitleWidth {
		titleWidth = min(minTitleWidth, available)
	}
	tokenWidth := available - titleWidth
	if tokenWidth < 0 {
		tokenWidth = 0
	}

	line := left +
		padStyled(truncate(t.Title, titleWidth), titleWidth) +
		padStyled(truncate(tokenText, tokenWidth), tokenWidth) +
		padStyled(children, childWidth) +
		padLeftStyled(due, dueWidth)
	line = pad(line, inner)

	if isCursor {
		// Focus and selection are inverse video, never a glow or an outline.
		line = selected.Render(line)
	}
	return zone.Mark(zoneRow+t.ID, line)
}

// renderDue formats the due date, with overdue in the one paired color token
// that de-emphasis is not allowed to swallow. On a selected row it renders
// flat, because that row is about to become one inverse-video run.
func (m *Model) renderDue(t api.Task, flat bool) string {
	if t.DueAt == nil {
		return ""
	}
	paint := func(st lipgloss.Style, text string) string {
		if flat {
			return text
		}
		return st.Render(text)
	}
	now := m.client.Now()
	d := query.LocalDate(*t.DueAt, now.Location())
	if d == "" {
		return ""
	}
	parsed, err := time.Parse(query.DateLayout, d)
	if err != nil {
		return paint(dim, d)
	}
	today := now.Format(query.DateLayout)

	switch {
	case d < today:
		return paint(overdue, parsed.Format("Jan 2"))
	case d == today:
		return paint(base, "Today")
	default:
		return paint(dim, parsed.Format("Jan 2"))
	}
}

// renderStatusLine is a real status line and changes with context. Every
// clickable thing shows its key hint, and each hint is marked so a click runs
// it: the bottom bar is a toolbar in both clients.
func (m *Model) renderStatusLine(inner int) string {
	if m.err != nil {
		// Errors say what failed and what to do. They do not apologize.
		return " " + truncate(overdue.Render(m.err.Error()), inner-1)
	}
	if m.offline {
		return " " + truncate(warn.Render("offline, showing the last list. Reads work, changes wait."), inner-1)
	}
	if m.loading {
		return " " + dim.Render("loading "+m.filter)
	}
	if m.status != "" {
		return " " + truncate(m.status, inner-1)
	}

	parts := make([]string, 0, 8)
	for _, b := range hints() {
		label := b.hint
		if b.keys[0] == "a" {
			// One accent per screen, on the primary action.
			label = accent.Render(label)
		} else {
			label = dim.Render(label)
		}
		parts = append(parts, zone.Mark(zoneHint+b.keys[0], label))
	}
	return " " + truncate(strings.Join(parts, "  "), inner-1)
}

// renderDetail is a full-screen replacement rather than a split pane: splits
// stop working at 80 columns and on a phone.
func (m *Model) renderDetail() string {
	t := m.detail
	width := max(m.width, 40)
	inner := width - 2

	lines := []string{
		m.renderTitleRule(width),
		m.boxed(inner, dim.Render(" ")+truncate(itoa(t.Num)+"  "+t.Title, inner-1)),
		m.rule(width, "├", "┤"),
	}

	add := func(label, value string) {
		if value == "" {
			return
		}
		lines = append(lines, m.boxed(inner,
			" "+dim.Render(padRight(label, 10))+truncate(value, inner-12)))
	}

	// A mirrored task puts its source on the first line, so one keystroke
	// opens the real thing.
	if t.ExternalURL != nil && *t.ExternalURL != "" {
		add("link", *t.ExternalURL)
	}
	add("status", t.Status)
	if t.Priority != nil {
		add("priority", priorityLabel(t.Priority))
	}
	if t.DueAt != nil {
		add("due", *t.DueAt)
	}
	if t.StartAt != nil {
		add("start", *t.StartAt)
	}
	if t.SnoozeUntil != nil {
		add("snoozed", "until "+*t.SnoozeUntil)
	}
	if len(t.Tags) > 0 {
		add("tags", "#"+strings.Join(t.Tags, " #"))
	}
	for _, p := range t.People {
		add(p.Role, p.Name)
	}
	if t.ChildrenTotal > 0 {
		add("subtasks", itoa(int64(t.ChildrenDone))+" of "+itoa(int64(t.ChildrenTotal))+" done")
	}
	if t.Attachments > 0 {
		add("files", itoa(int64(t.Attachments)))
	}
	add("notify", t.Notify)
	add("source", t.Source)

	if t.Notes != "" {
		lines = append(lines, m.boxed(inner, ""))
		for _, line := range strings.Split(t.Notes, "\n") {
			lines = append(lines, m.boxed(inner, " "+truncate(line, inner-1)))
		}
	}

	for len(lines) < m.height-2 {
		lines = append(lines, m.boxed(inner, ""))
	}
	lines = append(lines, m.rule(width, "├", "┤"))
	lines = append(lines, m.boxed(inner, " "+dim.Render("esc back  d done  x drop  u undo")))
	lines = append(lines, m.rule(width, "└", "┘"))
	return strings.Join(lines, "\n")
}

func (m *Model) renderHelp() string {
	width := max(m.width, 40)
	inner := width - 2

	lines := []string{
		m.renderTitleRule(width),
		m.boxed(inner, " "+dim.Render("keys")),
		m.rule(width, "├", "┤"),
	}
	for _, b := range bindings {
		keys := strings.Join(b.keys, " ")
		help := b.help
		if b.phase > 0 {
			help += dim.Render("  (phase " + itoa(int64(b.phase)) + ")")
		}
		lines = append(lines, m.boxed(inner, " "+dim.Render(padRight(keys, 14))+truncate(help, inner-16)))
	}

	lines = append(lines, m.boxed(inner, ""))
	if m.mouseEnabled {
		// The cost of capturing the mouse gets said out loud rather than
		// discovered.
		for _, line := range []string{
			"The mouse is on. Click a row to select, double-click to open,",
			"click a #tag or @person to filter by it, click a hint to run it.",
			"Capturing the mouse takes the terminal's own text selection away.",
			"Most emulators hand it back while shift is held. --no-mouse turns",
			"it off, as does mouse = false in config.toml.",
		} {
			lines = append(lines, m.boxed(inner, " "+dim.Render(truncate(line, inner-1))))
		}
	}

	for len(lines) < m.height-2 {
		lines = append(lines, m.boxed(inner, ""))
	}
	lines = append(lines, m.rule(width, "├", "┤"))
	lines = append(lines, m.boxed(inner, " "+dim.Render("esc back")))
	lines = append(lines, m.rule(width, "└", "┘"))
	return strings.Join(lines, "\n")
}

// checkbox draws the task state. A terminal has no other option, which is why
// the web UI uses a native control rather than this glyph: same control, two
// renderers, not the same glyph.
func checkbox(status string) string {
	switch status {
	case api.StatusDone:
		return "[x]"
	case api.StatusDropped:
		return "[-]"
	case api.StatusDoing, api.StatusWaiting:
		return "[~]"
	default:
		return "[ ]"
	}
}

// priorityLabel matches the p: token in the filter bar so the row teaches the
// grammar. Unset renders blank.
func priorityLabel(p *int) string {
	if p == nil {
		return ""
	}
	return "p" + itoa(int64(*p))
}
