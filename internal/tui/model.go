package tui

import (
	"context"
	"errors"
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/client"
	"github.com/harpchad/td/internal/query"
)

// mode is which screen is showing. Detail is a full-screen replacement rather
// than a split pane: splits stop working at 80 columns and on a phone.
type mode int

const (
	modeList mode = iota
	modeDetail
	modeAdd
	modeFilter
	modeHelp
)

// Model is the whole TUI.
type Model struct {
	client *client.Client
	ctx    context.Context

	mode   mode
	width  int
	height int

	// filter is the query the list is showing, and filterName is the saved
	// filter it came from, for the title bar.
	filter     string
	filterName string
	saved      []api.SavedFilter

	tasks []api.Task
	rows  []query.Row
	// collapsed is the fold state, keyed by task id. It lives on the server
	// so it follows you between clients.
	collapsed map[string]bool

	cursor int
	// offset is the first visible row. The wheel moves it without moving the
	// cursor, the same as every pager.
	offset int

	detail api.Task

	addInput    textinput.Model
	filterInput textinput.Model

	// promptInput is the one-line editor behind e, p, t and s. One input
	// serves all of them: they differ in what they prefill and what they do
	// with the answer, not in how they take it.
	promptInput textinput.Model
	prompt      prompt
	promptTask  api.Task

	// notes is the only multi-line thing in the product, so it is the only
	// place with a textarea.
	notesInput  textarea.Model
	editingNote bool

	// status is the one line the bottom bar shows instead of the hints, when
	// something has happened worth saying.
	status  string
	err     error
	offline bool
	loading bool

	// mouseEnabled is off with --no-mouse or `mouse = false`. Capturing the
	// mouse takes the terminal's own text selection away, and most emulators
	// only hand it back while shift is held.
	mouseEnabled bool

	// click tracks the last row clicked, so a second click inside the window
	// opens the detail view instead of selecting twice.
	click clickState

	// quitting stops the program on the next View.
	quitting bool
}

// Options configures a new Model.
type Options struct {
	// Filter is the query to open on. Empty opens the saved filter in slot 1.
	Filter string
	// Mouse enables pointer input. Default on.
	Mouse bool
}

// New builds the TUI model.
func New(ctx context.Context, c *client.Client, opts Options) *Model {
	add := textinput.New()
	add.Placeholder = `call the dealer  #truck @stacey p:2 due:friday`
	add.Prompt = "add: "
	add.SetVirtualCursor(false)

	filter := textinput.New()
	filter.Prompt = "filter: "
	filter.SetVirtualCursor(false)

	promptInput := textinput.New()
	promptInput.SetVirtualCursor(false)

	notes := textarea.New()
	notes.Placeholder = "notes"
	notes.ShowLineNumbers = false
	// The real cursor, not a drawn one that blinks on a timer. The only
	// animation in this product is the caret, and the terminal already has
	// one.
	notes.SetVirtualCursor(false)

	return &Model{
		client:       c,
		ctx:          ctx,
		filter:       opts.Filter,
		collapsed:    map[string]bool{},
		addInput:     add,
		filterInput:  filter,
		promptInput:  promptInput,
		notesInput:   notes,
		mouseEnabled: opts.Mouse,
		loading:      true,
		width:        80,
		height:       24,
	}
}

// Init loads the saved filters, the fold state, and the first list.
func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.loadFilters(), m.loadFolds(), m.reload())
}

// --- messages ---

type tasksMsg struct {
	filter string
	tasks  []api.Task
	err    error
}

type filtersMsg struct {
	filters []api.SavedFilter
	err     error
}

type foldsMsg struct {
	collapsed []string
	err       error
}

// actionMsg is the result of anything that changes data. Reload says whether
// the list has to be fetched again.
type actionMsg struct {
	status string
	err    error
	reload bool
}

// --- commands ---

func (m *Model) reload() tea.Cmd {
	filter := m.filter
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
		defer cancel()
		out, err := m.client.List(ctx, filter, 0)
		return tasksMsg{filter: filter, tasks: out.Tasks, err: err}
	}
}

func (m *Model) loadFilters() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
		defer cancel()
		filters, err := m.client.Filters(ctx)
		return filtersMsg{filters: filters, err: err}
	}
}

func (m *Model) loadFolds() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
		defer cancel()
		folds, err := m.client.Folds(ctx)
		return foldsMsg{collapsed: folds.Collapsed, err: err}
	}
}

func (m *Model) complete(t api.Task) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
		defer cancel()
		res, err := m.client.Complete(ctx, t.ID)
		if err != nil {
			return actionMsg{err: err}
		}
		status := "done  " + shorten(res.Task.Title)
		if res.ChildrenOpen > 0 {
			// The server never cascades. The parent is the commitment and the
			// children are steps, and they finish at different times.
			status += "  " + plural(res.ChildrenOpen, "subtask") + " still open"
		}
		return actionMsg{status: status, reload: true}
	}
}

func (m *Model) reopen(t api.Task) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
		defer cancel()
		todo := api.StatusTodo
		if _, err := m.client.Patch(ctx, t.ID, api.TaskPatch{Status: &todo}, ""); err != nil {
			return actionMsg{err: err}
		}
		return actionMsg{status: "reopened  " + shorten(t.Title), reload: true}
	}
}

func (m *Model) drop(t api.Task) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
		defer cancel()
		if _, err := m.client.Drop(ctx, t.ID); err != nil {
			return actionMsg{err: err}
		}
		return actionMsg{status: "dropped  " + shorten(t.Title) + "   u undoes it", reload: true}
	}
}

func (m *Model) undo() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
		defer cancel()
		res, err := m.client.Undo(ctx)
		if err != nil {
			var apiErr *api.Error
			if errors.As(err, &apiErr) && apiErr.Code == api.ErrNothingToUndo {
				return actionMsg{status: "nothing left to undo"}
			}
			return actionMsg{err: err}
		}
		status := "undid " + res.Kind
		if res.Task != nil {
			status += "  " + shorten(res.Task.Title)
		}
		return actionMsg{status: status, reload: true}
	}
}

func (m *Model) add(line string) tea.Cmd {
	now := m.client.Now()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
		defer cancel()

		// Same tokens as the filter grammar, parsed on the way in. Anything
		// the parser does not recognize stays in the title.
		capture := query.ParseCapture(line, now)
		if capture.Title == "" {
			return actionMsg{err: errors.New("that is all tags and no task")}
		}
		task, err := m.client.Create(ctx, api.TaskCreate{
			ID: newID(), Title: capture.Title, Priority: capture.Priority,
			DueAt: capture.Due, StartAt: capture.Start,
			Tags: capture.Tags, People: capture.People,
		})
		if err != nil {
			return actionMsg{err: err}
		}
		return actionMsg{
			status: "added  " + itoa(task.Num) + "  " + shorten(task.Title) + "  in " + task.Status,
			reload: true,
		}
	}
}

func (m *Model) setFold(t api.Task, collapsed bool) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
		defer cancel()
		if err := m.client.SetFold(ctx, t.ID, collapsed); err != nil {
			return actionMsg{err: err}
		}
		return actionMsg{reload: true}
	}
}

// setFoldAll folds or unfolds every parent in view in one pass.
func (m *Model) setFoldAll(collapsed bool) tea.Cmd {
	var ids []string
	for _, row := range m.rows {
		if row.Task.ChildrenTotal > 0 && m.collapsed[row.Task.ID] != collapsed {
			ids = append(ids, row.Task.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 30*time.Second)
		defer cancel()
		for _, id := range ids {
			if err := m.client.SetFold(ctx, id, collapsed); err != nil {
				return actionMsg{err: err}
			}
		}
		return actionMsg{reload: true}
	}
}

// --- helpers ---

// statusTitleWidth is how much of a title fits in a status line message
// alongside the verb and the count.
const statusTitleWidth = 40

// shorten cuts a title down for the status line, counting runes so a
// multi-byte title does not get sliced mid-character.
func shorten(s string) string {
	r := []rune(s)
	if len(r) <= statusTitleWidth {
		return s
	}
	return string(r[:statusTitleWidth-1]) + "…"
}

func plural(n int, word string) string {
	out := itoa(int64(n)) + " " + word
	if n != 1 {
		out += "s"
	}
	return out
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}

// currentTask is the task under the cursor, if there is one.
func (m *Model) currentTask() (api.Task, bool) {
	if m.cursor < 0 || m.cursor >= len(m.rows) {
		return api.Task{}, false
	}
	return m.rows[m.cursor].Task, true
}

// arrange rebuilds the visible rows from the loaded tasks, applying the fold
// state.
//
// A collapsed parent hides its children, and the 2/5 count becomes the only
// signal they exist, which is why that count is always drawn. A parent
// expands automatically when the filter matched one of its children directly:
// search never hides a match.
func (m *Model) arrange() {
	all := query.Arrange(m.tasks)

	// A child present at depth 0 was matched directly by the filter rather
	// than lifted under its parent, so its parent is not in the result set
	// and folding does not apply to it.
	m.rows = make([]query.Row, 0, len(all))
	hidden := ""
	for _, row := range all {
		if row.Depth == 0 {
			hidden = ""
			if m.collapsed[row.Task.ID] {
				hidden = row.Task.ID
			}
			m.rows = append(m.rows, row)
			continue
		}
		if row.Task.ParentID != nil && *row.Task.ParentID == hidden {
			continue
		}
		m.rows = append(m.rows, row)
	}
	m.clampCursor()
}

func (m *Model) clampCursor() {
	if m.cursor >= len(m.rows) {
		m.cursor = len(m.rows) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	m.scrollToCursor()
}

// listHeight is how many rows fit between the chrome.
func (m *Model) listHeight() int {
	// Top border, filter row, separator, then the list, then a separator, the
	// status line, and the bottom border.
	h := m.height - 6
	if h < 1 {
		return 1
	}
	return h
}

func (m *Model) scrollToCursor() {
	h := m.listHeight()
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+h {
		m.offset = m.cursor - h + 1
	}
	m.clampOffset()
}

func (m *Model) clampOffset() {
	maxOffset := len(m.rows) - m.listHeight()
	if maxOffset < 0 {
		maxOffset = 0
	}
	if m.offset > maxOffset {
		m.offset = maxOffset
	}
	if m.offset < 0 {
		m.offset = 0
	}
}

// countsOf summarizes the whole list for the top bar. The inbox count is the
// entire point of hiding the inbox from the home view: it nags without
// cluttering.
type counts struct{ inbox, waiting, overdue int }

func (m *Model) counts() counts {
	var c counts
	today := m.client.Now().Format(query.DateLayout)
	loc := m.client.Now().Location()

	for _, t := range m.tasks {
		switch t.Status {
		case api.StatusInbox:
			c.inbox++
		case api.StatusWaiting:
			c.waiting++
		}
		if t.DueAt != nil && t.Status != api.StatusDone && t.Status != api.StatusDropped {
			if d := query.LocalDate(*t.DueAt, loc); d != "" && d < today {
				c.overdue++
			}
		}
	}
	return c
}

// describeFilter names the current filter for the title bar, preferring the
// saved filter's name when the query matches one.
func (m *Model) describeFilter() string {
	if m.filterName != "" {
		return m.filterName
	}
	for _, f := range m.saved {
		if f.Query == m.filter {
			return f.Name
		}
	}
	if strings.TrimSpace(m.filter) == "" {
		return "Everything"
	}
	return "Search"
}
