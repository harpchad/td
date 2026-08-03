package tui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/client"
	"github.com/harpchad/td/internal/query"
)

// applyRepeat is the E key: editing the series rather than the instance.
//
// Section 3 is explicit that these are two different actions and that the
// product must never guess which one was meant. Everything else in the TUI
// edits the task under the cursor; this one edits the rule behind it, and it
// leaves the instance already in the list exactly as it is.
func (m *Model) applyRepeat(t api.Task, value string) tea.Cmd {
	if value == "" {
		return nil
	}
	rule, err := query.ParseRecurrence(value)
	if err != nil {
		return func() tea.Msg { return actionMsg{status: err.Error()} }
	}

	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
		defer cancel()

		in := client.Series{
			RRule: rule,
			Template: api.TaskCreate{
				Title:    t.Title,
				Notes:    t.Notes,
				Priority: t.Priority,
				DueAt:    t.DueAt,
				Tags:     t.Tags,
			},
		}

		var res client.SeriesResult
		if t.SeriesID != nil && *t.SeriesID != "" {
			res, err = m.client.UpdateSeries(ctx, *t.SeriesID, in)
		} else {
			// RepeatTask, not CreateSeries. The task under the cursor becomes
			// the first instance; CreateSeries would build a second one from
			// the template and leave this one sitting beside it.
			res, err = m.client.RepeatTask(ctx, t.ID, in)
		}
		if err != nil {
			return actionMsg{err: err}
		}
		return actionMsg{status: "repeats " + res.Series.RRule, reload: true}
	}
}

// openRepeat prefills the prompt with the rule the task already has, so
// pressing E on a recurring task shows what it does before changing it.
func (m *Model) openRepeat() tea.Cmd {
	t, ok := m.currentTask()
	if !ok {
		return nil
	}
	m.openPromptFor(promptRepeat, t)
	if t.SeriesID == nil || *t.SeriesID == "" {
		return nil
	}
	id := *t.SeriesID
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
		defer cancel()
		res, err := m.client.Series(ctx, id)
		if err != nil {
			return actionMsg{err: err}
		}
		return seriesMsg{rrule: res.Series.RRule}
	}
}

// seriesMsg carries the existing rule back into the open prompt.
type seriesMsg struct {
	rrule string
}
