package tui

import (
	"context"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/query"
)

// prompt is which one-line editor is open. Editing reuses the capture
// grammar rather than a field-by-field form: the row already reads as the
// grammar, so editing it as the grammar is one thing to learn rather than
// two.
type prompt int

const (
	promptNone prompt = iota
	promptEdit
	promptPriority
	promptTags
	promptSnooze
	promptWaiting
	promptPerson
	promptRepeat
	promptSaveFilter
)

func (p prompt) label() string {
	switch p {
	case promptEdit:
		return "edit: "
	case promptPriority:
		return "priority: "
	case promptTags:
		return "tags: "
	case promptSnooze:
		return "snooze: "
	case promptWaiting:
		return "waiting on: "
	case promptPerson:
		return "person: "
	case promptRepeat:
		return "repeats: "
	case promptSaveFilter:
		return "save filter: "
	default:
		return ""
	}
}

func (p prompt) placeholder() string {
	switch p {
	case promptPriority:
		return "1 to 4, or empty to clear"
	case promptTags:
		return "certs ops"
	case promptSnooze:
		return "1h, 2d, or friday"
	case promptWaiting:
		return "a handle, or empty to stop waiting"
	case promptPerson:
		return "handle, or handle:role"
	case promptRepeat:
		return "every monday, every 2 weeks, monthly on the 1st"
	case promptSaveFilter:
		return "slot 1-9 and a name; a slot alone clears it"
	default:
		return ""
	}
}

// captureLine renders a task back into the form quick-add accepts, so editing
// it round-trips through one grammar.
func captureLine(t api.Task) string {
	parts := []string{t.Title}
	for _, tag := range t.Tags {
		parts = append(parts, "#"+tag)
	}
	for _, p := range t.People {
		if p.Role == api.RoleWaiting {
			continue
		}
		parts = append(parts, "@"+firstWordLower(p.Name))
	}
	if t.Priority != nil {
		parts = append(parts, "p"+itoa(int64(*t.Priority)))
	}
	if t.DueAt != nil && *t.DueAt != "" {
		parts = append(parts, "due:"+query.LocalDate(*t.DueAt, time.Local))
	}
	return strings.Join(parts, " ")
}

// openPrompt starts a one-line editor for the task under the cursor.
func (m *Model) openPrompt(kind prompt) {
	t, ok := m.currentTask()
	if !ok {
		return
	}
	m.openPromptFor(kind, t)
}

// openPromptFor starts the editor on a named task. Triage has no cursor into
// the list, so it names the task it is showing.
func (m *Model) openPromptFor(kind prompt, t api.Task) {
	m.prompt = kind
	m.promptTask = t
	m.status = ""

	m.promptInput.Prompt = kind.label()
	m.promptInput.Placeholder = kind.placeholder()

	switch kind {
	case promptEdit:
		m.promptInput.SetValue(captureLine(t))
	case promptPriority:
		if t.Priority != nil {
			m.promptInput.SetValue(itoa(int64(*t.Priority)))
		} else {
			m.promptInput.SetValue("")
		}
	case promptTags:
		m.promptInput.SetValue(strings.Join(t.Tags, " "))
	default:
		m.promptInput.SetValue("")
	}
	m.promptInput.Focus()
	m.promptInput.CursorEnd()
}

// openSaveFilter starts the editor that binds the current query to a number
// key. Not openPromptFor: every other prompt edits the task under the cursor,
// and this one edits the view itself.
func (m *Model) openSaveFilter() {
	m.prompt = promptSaveFilter
	m.promptTask = api.Task{}
	m.status = ""

	m.promptInput.Prompt = promptSaveFilter.label()
	m.promptInput.Placeholder = promptSaveFilter.placeholder()
	m.promptInput.SetValue(m.saveFilterPrefill())
	m.promptInput.Focus()
	m.promptInput.CursorEnd()
}

// saveFilterPrefill suggests where the save lands. A query already on a key
// offers its own slot and name, so re-saving is a rename; anything else
// offers the lowest free slot.
func (m *Model) saveFilterPrefill() string {
	taken := map[int]bool{}
	for _, f := range m.saved {
		if f.Query == m.filter {
			return itoa(int64(f.Slot)) + " " + f.Name
		}
		taken[f.Slot] = true
	}
	for slot := 1; slot <= 9; slot++ {
		if !taken[slot] {
			return itoa(int64(slot)) + " "
		}
	}
	return ""
}

// applySaveFilter parses "5 vpn work": a slot, then the name. The name is
// what makes the slot legible in a status bar a month later, so a slot with
// no name is read as clearing it rather than as saving an unlabeled query.
func (m *Model) applySaveFilter(value string) tea.Cmd {
	slotStr, name, _ := strings.Cut(value, " ")
	name = strings.TrimSpace(name)
	if len(slotStr) != 1 || slotStr[0] < '1' || slotStr[0] > '9' {
		m.status = "save filter takes a slot 1 to 9, then a name"
		return nil
	}
	slot := int(slotStr[0] - '0')
	query := m.filter

	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
		defer cancel()

		status := "saved  " + slotStr + " " + name
		if name == "" {
			if err := m.client.DeleteFilter(ctx, slot); err != nil {
				return actionMsg{err: err}
			}
			status = "cleared slot " + slotStr
		} else {
			f := api.SavedFilter{Slot: slot, Name: name, Query: query}
			if _, err := m.client.PutFilter(ctx, f); err != nil {
				return actionMsg{err: err}
			}
		}

		filters, err := m.client.Filters(ctx)
		if err != nil {
			return actionMsg{err: err}
		}
		return filterSavedMsg{filters: filters, status: status}
	}
}

// submitPrompt applies whatever the open editor holds.
func (m *Model) submitPrompt() tea.Cmd {
	value := strings.TrimSpace(m.promptInput.Value())
	kind, t := m.prompt, m.promptTask

	m.prompt = promptNone
	m.promptInput.Blur()

	switch kind {
	case promptEdit:
		return m.applyCapture(t, value)
	case promptPriority:
		return m.applyPriority(t, value)
	case promptTags:
		return m.applyTags(t, value)
	case promptSnooze:
		return m.applySnooze(t, value)
	case promptWaiting:
		return m.applyWaiting(t, value)
	case promptPerson:
		return m.applyPerson(t, value)
	case promptRepeat:
		return m.applyRepeat(t, value)
	case promptSaveFilter:
		return m.applySaveFilter(value)
	}
	return nil
}

// applyWaiting moves a task to waiting on someone, or back out of it.
//
// waiting needs the person link: "waiting on Mikah since the 12th" is the
// state you actually live in, and a waiting task with nobody attached cannot
// answer the question the state exists for.
func (m *Model) applyWaiting(t api.Task, handle string) tea.Cmd {
	handle = strings.TrimPrefix(strings.TrimSpace(handle), "@")

	if handle == "" {
		if t.Status != api.StatusWaiting {
			return nil
		}
		// Leaving waiting clears the link and the age with it.
		todo := api.StatusTodo
		return m.patchTyped(t, api.TaskPatch{Status: &todo}, "back to todo")
	}

	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
		defer cancel()

		person, err := m.client.Person(ctx, handle)
		if err != nil {
			return actionMsg{status: "no person @" + handle}
		}
		waiting := api.StatusWaiting
		patch := api.TaskPatch{
			Status: &waiting, WaitingOn: &person.ID,
			Presence: map[string]bool{"waiting_on": true},
		}
		if _, err := m.client.PatchTyped(ctx, t.ID, patch, ""); err != nil {
			return actionMsg{err: err}
		}
		return actionMsg{status: "waiting on @" + person.Handle, reload: true}
	}
}

// applyPerson links a person to the task, optionally in a named role.
func (m *Model) applyPerson(t api.Task, value string) tea.Cmd {
	value = strings.TrimPrefix(strings.TrimSpace(value), "@")
	if value == "" {
		return nil
	}
	handle, role, ok := strings.Cut(value, ":")
	if !ok {
		role = api.RoleInvolved
	}

	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
		defer cancel()
		if _, err := m.client.LinkPerson(ctx, t.ID, handle, role); err != nil {
			return actionMsg{err: err}
		}
		return actionMsg{status: "@" + handle + " is " + role, reload: true}
	}
}

// patchTyped is the common shape for a typed patch.
func (m *Model) patchTyped(t api.Task, patch api.TaskPatch, status string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
		defer cancel()
		if _, err := m.client.PatchTyped(ctx, t.ID, patch, ""); err != nil {
			return actionMsg{err: err}
		}
		return actionMsg{status: status, reload: true}
	}
}

// applyCapture parses the edited line with the capture grammar and patches
// everything it names. Tags and people are a replacement rather than an
// addition, because the line is the whole task: what is not on it is gone.
func (m *Model) applyCapture(t api.Task, line string) tea.Cmd {
	if line == "" {
		return nil
	}
	now := m.client.Now()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
		defer cancel()

		capture := query.ParseCapture(line, now)
		if capture.Title == "" {
			return actionMsg{status: "that is all tags and no title"}
		}

		patch := map[string]any{
			"title": capture.Title,
			"tags":  capture.Tags,
		}
		// A token dropped from the line clears the field it set.
		patch["priority"] = nil
		if capture.Priority != nil {
			patch["priority"] = *capture.Priority
		}
		patch["due_at"] = nil
		if capture.Due != nil {
			patch["due_at"] = *capture.Due
		}
		if capture.Start != nil {
			patch["start_at"] = *capture.Start
		}

		if _, err := m.client.Patch(ctx, t.ID, patch, ""); err != nil {
			return actionMsg{err: err}
		}
		return actionMsg{status: "edited  " + shorten(capture.Title), reload: true}
	}
}

func (m *Model) applyPriority(t api.Task, value string) tea.Cmd {
	var priority any
	if value != "" {
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > 4 {
			m.status = "priority is 1 to 4, or empty to clear"
			return nil
		}
		priority = n
	}
	return m.patch(t, map[string]any{"priority": priority}, priorityStatus(value))
}

func priorityStatus(value string) string {
	if value == "" {
		return "priority cleared"
	}
	return "priority p" + value
}

func (m *Model) applyTags(t api.Task, value string) tea.Cmd {
	tags := []string{}
	for _, tag := range strings.Fields(value) {
		tag = strings.TrimPrefix(tag, "#")
		if tag != "" {
			tags = append(tags, strings.ToLower(tag))
		}
	}
	status := "tags cleared"
	if len(tags) > 0 {
		status = "tags #" + strings.Join(tags, " #")
	}
	return m.patch(t, map[string]any{"tags": tags}, status)
}

// applySnooze accepts a duration or a date, the same two forms the API takes.
func (m *Model) applySnooze(t api.Task, value string) tea.Cmd {
	if value == "" {
		return nil
	}
	now := m.client.Now()
	req := api.SnoozeRequest{}
	if _, err := time.ParseDuration(value); err == nil {
		req.Duration = value
	} else {
		date, err := query.ResolveDate(value, now)
		if err != nil {
			m.status = "snooze takes a duration like 1h, or a date like friday"
			return nil
		}
		req.Until = date + "T00:00:00Z"
	}

	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
		defer cancel()
		if _, err := m.client.Snooze(ctx, t.ID, req); err != nil {
			return actionMsg{err: err}
		}
		return actionMsg{status: "snoozed  " + shorten(t.Title), reload: true}
	}
}

// patch is the common shape: send a partial update, report it, reload.
func (m *Model) patch(t api.Task, body map[string]any, status string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
		defer cancel()
		if _, err := m.client.Patch(ctx, t.ID, body, ""); err != nil {
			return actionMsg{err: err}
		}
		return actionMsg{status: status, reload: true}
	}
}

// saveNotes writes the notes buffer back.
func (m *Model) saveNotes(t api.Task, notes string) tea.Cmd {
	return m.patch(t, map[string]any{"notes": notes}, "notes saved")
}
