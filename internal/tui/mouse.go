package tui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	zone "github.com/lrstanley/bubblezone/v2"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/query"
)

// The pointer does the same things the web pointer does, in the terminal, so
// both clients behave the same way when your hand is already on the trackpad.
// It is not a fallback for people who cannot remember the keys.
//
// Hit regions come from the renderer rather than from column arithmetic in
// this file. Recomputing columns in the input handler works until the first
// truncated title and then drifts silently, so the render marks each region
// while drawing it and events are tested against those marks.

// doubleClickWindow is how close two clicks have to be to open the detail
// view rather than select twice.
const doubleClickWindow = 400 * time.Millisecond

// zone id prefixes. Each is marked during render and tested here.
const (
	zoneRow      = "row:"
	zoneCheckbox = "check:"
	zoneFold     = "fold:"
	zoneTag      = "tag:"
	zonePerson   = "person:"
	zoneHint     = "hint:"
	zoneFilter   = "filterbar"
)

type clickState struct {
	lastRow int
	lastAt  time.Time
}

func (m *Model) onClick(msg tea.MouseClickMsg) (tea.Model, tea.Cmd) {
	if !m.mouseEnabled {
		return m, nil
	}
	mouse := msg.Mouse()

	// No right-click menu. Terminal emulators intercept the right button
	// inconsistently, and the keyed menu already exists.
	if mouse.Button != tea.MouseLeft {
		return m, nil
	}

	// Clicking the filter bar edits it.
	if z := zone.Get(zoneFilter); z != nil && !z.IsZero() && z.InBounds(msg) {
		return m.listKey("/")
	}

	// The bottom bar is a real toolbar: clicking a hint runs the key it names.
	for _, b := range hints() {
		if z := zone.Get(zoneHint + b.keys[0]); z != nil && !z.IsZero() && z.InBounds(msg) {
			return m.listKey(b.keys[0])
		}
	}

	// Clicking a tag or a person filters by it. This is the one thing the
	// mouse does better than the keyboard here.
	for _, rv := range m.visibleRows() {
		for _, tag := range rv.Row.Task.Tags {
			id := zoneTag + rv.Row.Task.ID + ":" + tag
			if z := zone.Get(id); z != nil && !z.IsZero() && z.InBounds(msg) {
				return m, m.filterBy("#" + tag)
			}
		}
		for _, p := range rv.Row.Task.People {
			id := zonePerson + rv.Row.Task.ID + ":" + p.PersonID + ":" + p.Role
			if z := zone.Get(id); z != nil && !z.IsZero() && z.InBounds(msg) {
				return m, m.filterBy("@" + firstWordLower(p.Name))
			}
		}
	}

	// The checkbox cell toggles done, and the fold cell folds. Both are
	// tested before the row itself, since they sit inside it.
	for _, rv := range m.visibleRows() {
		t := rv.Row.Task
		if z := zone.Get(zoneCheckbox + t.ID); z != nil && !z.IsZero() && z.InBounds(msg) {
			m.cursor = rv.Index
			m.scrollToCursor()
			if t.Status == api.StatusDone {
				return m, m.reopen(t)
			}
			return m, m.complete(t)
		}
		if t.ChildrenTotal > 0 {
			if z := zone.Get(zoneFold + t.ID); z != nil && !z.IsZero() && z.InBounds(msg) {
				m.cursor = rv.Index
				m.scrollToCursor()
				return m, m.setFold(t, !m.collapsed[t.ID])
			}
		}
	}

	// Click a row to select it. Double-click opens the detail view.
	for _, rv := range m.visibleRows() {
		if z := zone.Get(zoneRow + rv.Row.Task.ID); z == nil || z.IsZero() || !z.InBounds(msg) {
			continue
		}
		now := time.Now()
		doubled := m.click.lastRow == rv.Index && now.Sub(m.click.lastAt) < doubleClickWindow
		m.click.lastRow, m.click.lastAt = rv.Index, now

		m.cursor = rv.Index
		m.scrollToCursor()
		if doubled {
			m.detail = rv.Row.Task
			m.mode = modeDetail
		}
		return m, nil
	}

	return m, nil
}

// onWheel scrolls the viewport without moving the selection, the same as
// every pager and the same as the web list.
func (m *Model) onWheel(msg tea.MouseWheelMsg) (tea.Model, tea.Cmd) {
	if !m.mouseEnabled {
		return m, nil
	}
	const step = 3
	switch msg.Mouse().Button {
	case tea.MouseWheelUp:
		m.offset -= step
	case tea.MouseWheelDown:
		m.offset += step
	default:
		return m, nil
	}
	m.clampOffset()
	return m, nil
}

// filterBy replaces the filter with a single token, which is what clicking a
// tag or a person means.
func (m *Model) filterBy(token string) tea.Cmd {
	return m.chooseFilter(token, "")
}

// visibleRows is the window the list is currently showing, each row carrying
// its index in the full list so a click can move the cursor to it.
func (m *Model) visibleRows() []rowView {
	end := min(m.offset+m.listHeight(), len(m.rows))
	if m.offset >= end {
		return nil
	}
	out := make([]rowView, 0, end-m.offset)
	for i := m.offset; i < end; i++ {
		out = append(out, rowView{Row: m.rows[i], Index: i})
	}
	return out
}

type rowView struct {
	Row   query.Row
	Index int
}

func firstWordLower(name string) string {
	fields := strings.Fields(name)
	if len(fields) == 0 {
		return ""
	}
	return strings.ToLower(fields[0])
}
