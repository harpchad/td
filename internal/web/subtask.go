package web

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/query"
	"github.com/harpchad/td/internal/store"
)

// attachmentLister is the part of the store the detail page needs for files.
// A narrow optional interface rather than a wider Service, so a deployment
// with no blob store still serves every other page.
type attachmentLister interface {
	Attachments(ctx context.Context, taskID string) ([]api.Attachment, error)
}

// addSubtask creates a task under another one.
//
// The same one-line grammar as quick-add: the row already reads as the
// grammar, so writing one is one thing to learn rather than two. The parent
// is the commitment, and completing it never cascades to the children.
func (u *UI) addSubtask(w http.ResponseWriter, r *http.Request) {
	id, ok := u.resolve(w, r)
	if !ok {
		return
	}
	line := strings.TrimSpace(r.PostFormValue("line"))
	if line == "" {
		u.redirectToTask(w, r, id, "")
		return
	}

	now := u.Now()
	capture := query.ParseCapture(line, now)
	if capture.Title == "" {
		u.redirectToTask(w, r, id, "that is all tags and no task")
		return
	}

	// Tags stay nil when none were typed, which is what tells the store to
	// copy the parent's. The copy is taken once at creation: live inheritance
	// would mean editing a parent silently rewrites its children.
	var tags []string
	if len(capture.Tags) > 0 {
		tags = capture.Tags
	}

	if _, err := u.svc.Create(r.Context(), u.actor(r), api.TaskCreate{
		Title: capture.Title, Priority: capture.Priority,
		DueAt: capture.Due, StartAt: capture.Start,
		Tags: tags, People: capture.People,
		ParentID: &id,
	}, now); err != nil {
		u.redirectToTask(w, r, id, humanError(err))
		return
	}
	u.redirectToTask(w, r, id, "added a subtask")
}

// attachmentRows loads a task's files for the detail page. A store without
// attachment support returns nothing rather than an error: the page is still
// worth rendering.
func (u *UI) attachmentRows(ctx context.Context, taskID string) []attachmentRow {
	lister, ok := u.svc.(attachmentLister)
	if !ok {
		return nil
	}
	files, err := lister.Attachments(ctx, taskID)
	if err != nil {
		u.log.Error("loading attachments", "task", taskID, "err", err)
		return nil
	}

	out := make([]attachmentRow, 0, len(files))
	for _, f := range files {
		out = append(out, attachmentRow{
			ID: f.ID, Filename: f.Filename, Mime: f.Mime,
			Size: humanBytes(f.Bytes),
			// Through the API, which is behind the same auth as everything
			// else. There is no static handler over the blob directory,
			// because a guessable path under one is a download that skipped
			// the check.
			Href: "/api/v1/tasks/" + taskID + "/attachments/" + f.ID,
		})
	}
	return out
}

// childLister is the part of the store the detail page needs for subtasks.
type childLister interface {
	Children(ctx context.Context, parentID string, now time.Time) ([]api.Task, error)
}

// childRows loads a task's subtasks for the detail page.
func (u *UI) childRows(ctx context.Context, task api.Task, now time.Time) []Row {
	if task.ChildrenTotal == 0 {
		return nil
	}
	lister, ok := u.svc.(childLister)
	if !ok {
		return nil
	}
	children, err := lister.Children(ctx, task.ID, now)
	if err != nil {
		u.log.Error("loading subtasks", "task", task.ID, "err", err)
		return nil
	}
	out := make([]Row, 0, len(children))
	for _, child := range children {
		out = append(out, prepareRow(child, true, false, now))
	}
	return out
}

// humanBytes is for a column in a table, not for arithmetic.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return itoa(n) + " B"
	}
	div, exp := int64(unit), 0
	for size := n / unit; size >= unit; size /= unit {
		div *= unit
		exp++
	}
	return strconv.FormatFloat(float64(n)/float64(div), 'f', 1, 64) +
		" " + string("KMGT"[exp]) + "B"
}

// seriesReader is the part of the store the detail page needs for the
// recurrence line.
type seriesReader interface {
	Series(ctx context.Context, id string) (store.Series, error)
}

// repeatRule is the rule a recurring task's instance came from, for display.
// Editing it is a separate action: section 3 says editing an instance edits
// that instance, and the detail page is the instance.
func (u *UI) repeatRule(ctx context.Context, seriesID string) string {
	reader, ok := u.svc.(seriesReader)
	if !ok {
		return ""
	}
	series, err := reader.Series(ctx, seriesID)
	if err != nil {
		return ""
	}
	return series.RRule
}
