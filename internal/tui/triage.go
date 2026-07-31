package tui

import (
	"context"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/harpchad/td/internal/api"
)

// TriageFilter is what triage works through. The inbox is the triage bucket;
// quick-add always lands there and nothing else does.
const TriageFilter = "is:inbox"

// Triage is a dedicated mode, not a view: one task at a time, large, with
// single-key actions. Getting from 20 to 0 should take two minutes, and a
// list view cannot do that because every decision needs the eye to find the
// row again.
//
// The keys are the same letters they are everywhere else. A separate triage
// keymap would be a second thing to learn for the one screen where speed is
// the entire point.
func (m *Model) enterTriage() tea.Cmd {
	m.mode = modeTriage
	m.triageIndex = 0
	m.status = ""
	return m.loadTriage()
}

func (m *Model) loadTriage() tea.Cmd {
	return func() tea.Msg {
		list, err := m.fetch(TriageFilter, 0)
		if err != nil {
			return actionMsg{err: err}
		}
		return triageMsg{tasks: list.Tasks}
	}
}

// triageMsg carries a fresh inbox, and optionally what just happened to it.
type triageMsg struct {
	tasks  []api.Task
	status string
}

// triageTask is what the big card is showing, if anything.
func (m *Model) triageTask() (api.Task, bool) {
	if m.triageIndex < 0 || m.triageIndex >= len(m.triage) {
		return api.Task{}, false
	}
	return m.triage[m.triageIndex], true
}

// triageKey handles one keystroke in triage.
//
// Promote, drop and skip advance on their own. The edits (priority, due,
// tags, person) do not: setting a priority and then wanting to add a tag is
// the common case, and a mode that jumped away after every keystroke would
// make that two passes.
func (m *Model) triageKey(key string) (tea.Model, tea.Cmd) {
	t, ok := m.triageTask()
	if !ok {
		switch key {
		case "q", "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		case "esc", "enter":
			m.mode = modeList
			return m, m.reload()
		}
		return m, nil
	}

	switch key {
	case "q", "ctrl+c":
		m.quitting = true
		return m, tea.Quit

	case "esc":
		m.mode = modeList
		return m, m.reload()

	case "n", "j", "right", "tab":
		m.advance(1)
	case "k", "left", "shift+tab":
		m.advance(-1)

	case "1", "2", "3", "4":
		// A priority is one of the two things that lets a task leave the
		// inbox, so setting one here is the common move.
		return m, m.triagePriority(t, key)

	case "P", "enter":
		return m, m.promote(t)

	case "x":
		return m, m.triageDrop(t)

	case "d":
		return m, m.triageComplete(t)

	case "s":
		m.openPromptFor(promptSnooze, t)
	case "e":
		m.openPromptFor(promptEdit, t)
	case "p":
		m.openPromptFor(promptPriority, t)
	case "t":
		m.openPromptFor(promptTags, t)
	case "w":
		m.openPromptFor(promptWaiting, t)
	case "@":
		m.openPromptFor(promptPerson, t)

	case "N":
		m.editingNote = true
		m.promptTask = t
		m.notesInput.SetValue(t.Notes)
		m.notesInput.Focus()
		m.status = ""

	case "u":
		return m, m.undo()

	case "?":
		m.mode = modeHelp
	}
	return m, nil
}

// advance moves through the queue without wrapping. Wrapping in a queue you
// are trying to empty hides whether you are done.
func (m *Model) advance(delta int) {
	m.triageIndex += delta
	if m.triageIndex < 0 {
		m.triageIndex = 0
	}
	if m.triageIndex > len(m.triage) {
		m.triageIndex = len(m.triage)
	}
}

// drop removes the task at the cursor from the local queue, so the next one
// is on screen before the server answers. The reload that follows is what
// makes it true.
func (m *Model) dropFromQueue() {
	if m.triageIndex < 0 || m.triageIndex >= len(m.triage) {
		return
	}
	m.triage = append(m.triage[:m.triageIndex], m.triage[m.triageIndex+1:]...)
	if m.triageIndex > len(m.triage) {
		m.triageIndex = len(m.triage)
	}
}

func (m *Model) triagePriority(t api.Task, key string) tea.Cmd {
	p := int(key[0] - '0')

	// Promotion needs a priority or a due date, and the priority is what was
	// just set, so this is one round trip rather than two.
	status := api.StatusTodo
	patch := api.TaskPatch{
		Priority: &p, Status: &status,
		Presence: map[string]bool{"priority": true},
	}
	m.dropFromQueue()
	return m.triagePatch(t, patch, "p"+key+" and out of the inbox")
}

func (m *Model) promote(t api.Task) tea.Cmd {
	status := api.StatusTodo
	m.dropFromQueue()
	return m.triagePatch(t, api.TaskPatch{Status: &status}, "promoted")
}

func (m *Model) triageDrop(t api.Task) tea.Cmd {
	m.dropFromQueue()
	status := api.StatusDropped
	return m.triagePatch(t, api.TaskPatch{Status: &status}, "dropped")
}

func (m *Model) triageComplete(t api.Task) tea.Cmd {
	m.dropFromQueue()
	status := api.StatusDone
	return m.triagePatch(t, api.TaskPatch{Status: &status}, "done")
}

// triagePatch applies a change and refreshes the queue behind it.
//
// One command rather than a sequence: the reload has to happen after the
// write lands, and the queue on screen has already moved on optimistically,
// so the refresh is what makes it true rather than what makes it visible.
func (m *Model) triagePatch(t api.Task, patch api.TaskPatch, status string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
		defer cancel()
		if _, err := m.client.PatchTyped(ctx, t.ID, patch, ""); err != nil {
			return actionMsg{err: err}
		}
		list, err := m.client.List(ctx, TriageFilter, 0)
		if err != nil {
			return actionMsg{status: status}
		}
		return triageMsg{tasks: list.Tasks, status: status}
	}
}

// renderTriage draws the one-task card.
func (m *Model) renderTriage() string {
	width := max(m.width, 40)
	inner := width - 2

	left := " triage"
	if len(m.triage) > 0 {
		left += "  " + itoa(int64(m.triageIndex+1)) + " of " + itoa(int64(len(m.triage)))
	}
	lines := []string{
		m.rule(width, "┌", "┐"),
		m.boxed(inner, accent.Render(padRight(left, inner))),
		m.rule(width, "├", "┤"),
	}

	t, ok := m.triageTask()
	if !ok {
		lines = append(lines,
			m.boxed(inner, ""),
			m.boxed(inner, " "+truncate("Inbox zero.", inner-1)),
			m.boxed(inner, ""),
			m.boxed(inner, " "+dim.Render(truncate("esc goes back to the list.", inner-1))),
		)
	} else {
		// One task, big. The title gets its own lines rather than a truncated
		// row, because deciding about something you cannot read is the thing
		// triage is meant to stop.
		lines = append(lines, m.boxed(inner, ""))
		for _, line := range wrap(t.Title, inner-4) {
			lines = append(lines, m.boxed(inner, "  "+padRight(line, inner-2)))
		}
		lines = append(lines, m.boxed(inner, ""))

		meta := []string{}
		if t.Priority != nil {
			meta = append(meta, priorityLabel(t.Priority))
		}
		if t.DueAt != nil && *t.DueAt != "" {
			meta = append(meta, "due "+*t.DueAt)
		}
		if len(t.Tags) > 0 {
			meta = append(meta, "#"+strings.Join(t.Tags, " #"))
		}
		for _, p := range t.People {
			meta = append(meta, "@"+firstWordLower(p.Name))
		}
		if len(meta) > 0 {
			lines = append(lines, m.boxed(inner, "  "+dim.Render(truncate(strings.Join(meta, "  "), inner-3))))
		}
		if t.Notes != "" {
			lines = append(lines, m.boxed(inner, ""))
			for _, line := range wrap(t.Notes, inner-4) {
				lines = append(lines, m.boxed(inner, "  "+dim.Render(padRight(line, inner-2))))
			}
		}
	}

	for len(lines) < m.height-3 {
		lines = append(lines, m.boxed(inner, ""))
	}
	lines = append(lines, m.rule(width, "├", "┤"))
	lines = append(lines, m.boxed(inner, " "+dim.Render(truncate(
		"1-4 priority  P promote  t tags  @ person  s snooze  x drop  n next  esc done", inner-1))))
	lines = append(lines, m.rule(width, "└", "┘"))
	return strings.Join(lines, "\n")
}

// wrap breaks text onto lines of at most width, on word boundaries where it
// can and mid-word where a single word is longer than the line.
func wrap(text string, width int) []string {
	if width < 1 {
		width = 1
	}
	var out []string
	for _, paragraph := range strings.Split(text, "\n") {
		line := ""
		for _, word := range strings.Fields(paragraph) {
			for len(word) > width {
				if line != "" {
					out = append(out, line)
					line = ""
				}
				out = append(out, word[:width])
				word = word[width:]
			}
			switch {
			case line == "":
				line = word
			case len(line)+1+len(word) <= width:
				line += " " + word
			default:
				out = append(out, line)
				line = word
			}
		}
		out = append(out, line)
	}
	return out
}
