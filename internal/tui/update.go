package tui

import (
	"errors"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/client"
	"github.com/harpchad/td/internal/query"
)

// Update handles one message.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.scrollToCursor()
		return m, nil

	case tasksMsg:
		m.onTasks(msg)
		return m, nil

	case filtersMsg:
		if msg.err == nil {
			m.saved = msg.filters
		}
		m.savedLoaded = true
		return m, m.startFilter()

	case filterSavedMsg:
		m.saved = msg.filters
		m.status = msg.status
		return m, nil

	case viewFilterMsg:
		// A filter named on the command line outranks the remembered one: it is
		// the more recent instruction, and `td -filter ...` that ignored its own
		// argument would be absurd.
		if msg.err == nil && msg.chosen && !m.filterChosen {
			m.filterChosen = true
			m.filter, m.filterName = msg.filter, ""
			m.viewLoaded = true
			return m, m.reload()
		}
		m.viewLoaded = true
		return m, m.startFilter()

	case foldsMsg:
		if msg.err == nil {
			m.collapsed = make(map[string]bool, len(msg.collapsed))
			for _, id := range msg.collapsed {
				m.collapsed[id] = true
			}
			m.arrange()
		}
		return m, nil

	case seriesMsg:
		// The rule arrived after the prompt opened. Only fill it if the user
		// has not started typing over it.
		if m.prompt == promptRepeat && m.promptInput.Value() == "" {
			m.promptInput.SetValue(msg.rrule)
			m.promptInput.CursorEnd()
		}
		return m, nil

	case triageMsg:
		m.triage = msg.tasks
		if m.triageIndex > len(m.triage) {
			m.triageIndex = len(m.triage)
		}
		if msg.status != "" {
			m.status = msg.status
		}
		return m, nil

	case actionMsg:
		return m, m.onAction(msg)

	case tea.KeyPressMsg:
		return m.onKey(msg)

	// Bubble Tea v2 splits mouse input into separate message types rather
	// than one event with a discriminator. Motion is not handled at all:
	// there is no hover in the TUI by design, and all-motion reporting floods
	// the event loop over SSH to buy it.
	case tea.MouseClickMsg:
		return m.onClick(msg)
	case tea.MouseWheelMsg:
		return m.onWheel(msg)
	}

	return m, nil
}

func (m *Model) onTasks(msg tasksMsg) {
	m.loading = false
	if msg.err != nil {
		// Offline is a state, not an error. Reads keep working from what is
		// already on screen and the status line says so.
		if errors.Is(msg.err, client.ErrOffline) {
			m.offline = true
			m.err = nil
			return
		}
		m.err = msg.err
		return
	}
	m.offline = false
	m.err = nil
	m.tasks = msg.tasks
	m.arrange()
}

func (m *Model) onAction(msg actionMsg) tea.Cmd {
	if msg.err != nil {
		if errors.Is(msg.err, client.ErrOffline) {
			m.offline = true
			m.status = "offline, that change did not go through"
			return nil
		}
		m.err = msg.err
		return nil
	}
	m.err = nil
	if msg.status != "" {
		m.status = msg.status
	}
	if msg.reload {
		return tea.Batch(m.reload(), m.loadFolds())
	}
	return nil
}

func (m *Model) onKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	// The text inputs own every key while they are open, except the two that
	// close them.
	// A prompt or the notes editor owns every key while it is open, except
	// the two that close it.
	if m.prompt != promptNone {
		return m.updatePrompt(msg, key)
	}
	if m.editingNote {
		return m.updateNotes(msg, key)
	}

	switch m.mode {
	case modeAdd:
		return m.updateAdd(msg, key)
	case modeFilter:
		return m.updateFilter(msg, key)
	case modeTriage:
		return m.triageKey(key)
	case modeHelp, modeDetail:
		if key == "esc" || key == "q" || key == "enter" {
			m.mode = modeList
			return m, nil
		}
		if key == "ctrl+c" {
			m.quitting = true
			return m, tea.Quit
		}
		if m.mode == modeDetail {
			return m.listKey(key)
		}
		return m, nil
	}

	return m.listKey(key)
}

func (m *Model) listKey(key string) (tea.Model, tea.Cmd) {
	// A key that is specified but not built yet says which phase it lands in,
	// because a key that silently does nothing reads as a bug.
	if b, ok := deferredKeys[key]; ok {
		m.status = key + " (" + b.help + "): " + b.when
		return m, nil
	}

	switch key {
	case "q", "ctrl+c":
		m.quitting = true
		return m, tea.Quit

	case "j", "down":
		m.moveCursor(1)
	case "k", "up":
		m.moveCursor(-1)
	case "g", "home":
		m.cursor = 0
		m.scrollToCursor()
	case "G", "end":
		m.cursor = len(m.rows) - 1
		m.clampCursor()
	case "ctrl+d":
		m.moveCursor(m.listHeight() / 2)
	case "ctrl+u":
		m.moveCursor(-m.listHeight() / 2)

	case "enter":
		if t, ok := m.currentTask(); ok {
			m.detail = t
			m.mode = modeDetail
		}

	case "space", "d":
		if t, ok := m.currentTask(); ok {
			if t.Status == api.StatusDone {
				return m, m.reopen(t)
			}
			return m, m.complete(t)
		}

	case "x":
		if t, ok := m.currentTask(); ok {
			return m, m.drop(t)
		}

	case "z":
		if t, ok := m.currentTask(); ok && t.ChildrenTotal > 0 {
			return m, m.setFold(t, !m.collapsed[t.ID])
		}

	case "Z":
		// Fold everything if anything is open, otherwise unfold everything.
		anyOpen := false
		for _, row := range m.rows {
			if row.Task.ChildrenTotal > 0 && !m.collapsed[row.Task.ID] {
				anyOpen = true
				break
			}
		}
		return m, m.setFoldAll(anyOpen)

	case "a":
		m.mode = modeAdd
		m.addInput.SetValue("")
		m.addInput.Focus()
		m.status = ""

	case "/":
		m.mode = modeFilter
		m.filterInput.SetValue(m.filter)
		m.filterInput.Focus()
		m.filterInput.CursorEnd()
		m.status = ""

	case "e":
		m.openPrompt(promptEdit)
	case "p":
		m.openPrompt(promptPriority)
	case "t":
		m.openPrompt(promptTags)
	case "s":
		m.openPrompt(promptSnooze)
	case "w":
		m.openPrompt(promptWaiting)
	case "@":
		m.openPrompt(promptPerson)
	case "E":
		// The series, not the instance. Editing an instance edits that
		// instance; the rule behind it needs its own action.
		return m, m.openRepeat()

	case "S":
		m.openSaveFilter()

	case "N":
		// Notes are multi-line, so they get a textarea rather than the
		// one-line prompt the other edits share.
		if t, ok := m.currentTask(); ok {
			m.editingNote = true
			m.promptTask = t
			m.notesInput.SetValue(t.Notes)
			m.notesInput.Focus()
			m.status = ""
		}

	case "u":
		return m, m.undo()

	case "r":
		m.loading = true
		return m, tea.Batch(m.reload(), m.loadFolds())

	case "T":
		return m, m.enterTriage()

	case "?":
		m.mode = modeHelp

	case "esc":
		m.mode = modeList
		m.status = ""

	default:
		if len(key) == 1 && key >= "1" && key <= "9" {
			return m, m.applySavedFilter(int(key[0] - '0'))
		}
	}
	return m, nil
}

func (m *Model) updatePrompt(msg tea.KeyPressMsg, key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc":
		m.prompt = promptNone
		m.promptInput.Blur()
		return m, nil
	case "enter":
		return m, m.submitPrompt()
	}
	var cmd tea.Cmd
	m.promptInput, cmd = m.promptInput.Update(msg)
	return m, cmd
}

func (m *Model) updateNotes(msg tea.KeyPressMsg, key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc":
		m.editingNote = false
		m.notesInput.Blur()
		return m, nil
	case "ctrl+s":
		notes := m.notesInput.Value()
		m.editingNote = false
		m.notesInput.Blur()
		return m, m.saveNotes(m.promptTask, notes)
	}
	var cmd tea.Cmd
	m.notesInput, cmd = m.notesInput.Update(msg)
	return m, cmd
}

func (m *Model) applySavedFilter(slot int) tea.Cmd {
	for _, f := range m.saved {
		if f.Slot == slot {
			return m.chooseFilter(f.Query, f.Name)
		}
	}
	m.status = "no saved filter on " + itoa(int64(slot))
	return nil
}

func (m *Model) updateAdd(msg tea.KeyPressMsg, key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc":
		m.mode = modeList
		m.addInput.Blur()
		return m, nil
	case "enter":
		line := strings.TrimSpace(m.addInput.Value())
		m.mode = modeList
		m.addInput.Blur()
		if line == "" {
			return m, nil
		}
		return m, m.add(line)
	}
	var cmd tea.Cmd
	m.addInput, cmd = m.addInput.Update(msg)
	return m, cmd
}

func (m *Model) updateFilter(msg tea.KeyPressMsg, key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc":
		m.mode = modeList
		m.filterInput.Blur()
		m.err = nil
		return m, nil
	case "enter":
		candidate := strings.TrimSpace(m.filterInput.Value())
		// Parse locally first. The client and the server share one grammar,
		// so a typo reports its own message here without a round trip and the
		// server cannot disagree about what it means.
		if _, err := query.ParseAt(candidate, m.client.Now()); err != nil {
			m.err = err
			return m, nil
		}
		m.err = nil
		m.mode = modeList
		m.filterInput.Blur()
		return m, m.chooseFilter(candidate, "")
	}

	var cmd tea.Cmd
	m.filterInput, cmd = m.filterInput.Update(msg)

	// Report a syntax error as it is typed rather than on submit.
	if _, err := query.ParseAt(strings.TrimSpace(m.filterInput.Value()), m.client.Now()); err != nil {
		m.err = err
	} else {
		m.err = nil
	}
	return m, cmd
}

func (m *Model) moveCursor(delta int) {
	if len(m.rows) == 0 {
		return
	}
	m.cursor += delta
	m.clampCursor()
}
