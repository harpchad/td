package web

import (
	"context"
	"net/http"
	"net/url"
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

// seriesWriter is what the repeat form needs. Separate from seriesReader so a
// deployment wired for reading alone renders the rule and simply offers no
// form, rather than offering one that fails on submit.
type seriesWriter interface {
	RepeatTask(ctx context.Context, actor, taskID string, in store.Series, now time.Time) (store.Series, api.Task, error)
	UpdateSeries(ctx context.Context, in store.Series) (store.Series, error)
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

// repeat is the web's equivalent of the TUI's E key.
//
// Section 3 is explicit that editing an instance and editing the series are
// two different actions and that the product must never guess which was meant.
// Everything else on this page edits the task; this one edits the rule behind
// it and leaves the instance in the list exactly as it is. That is why it is
// its own form with its own button rather than a field in the edit form.
func (u *UI) repeat(w http.ResponseWriter, r *http.Request) {
	writer, ok := u.svc.(seriesWriter)
	if !ok {
		u.detailBack(w, r, "", "recurrence is not available on this server")
		return
	}
	if err := r.ParseForm(); err != nil {
		u.detailBack(w, r, "", "could not read that form")
		return
	}

	id, err := u.svc.Resolve(r.Context(), r.PathValue("id"))
	if err != nil {
		u.fail(w, r, err)
		return
	}
	task, err := u.svc.Get(r.Context(), id)
	if err != nil {
		u.fail(w, r, err)
		return
	}

	said := strings.TrimSpace(r.PostFormValue("repeat"))
	if said == "" {
		// Matching the TUI, where an empty prompt is a cancel. Stopping a
		// series is a different action from editing one and neither client
		// offers it yet.
		u.detailBack(w, r, task.ID, "")
		return
	}

	rule, err := query.ParseRecurrence(said)
	if err != nil {
		// The parser's own message names what it could not read, which is more
		// use than anything this handler could invent.
		u.detailBack(w, r, task.ID, err.Error())
		return
	}

	// The template is the task as it stands, so the next instance looks like
	// this one. Everything locally owned travels; status and completion do not.
	in := store.Series{
		RRule: rule,
		Template: api.TaskCreate{
			Title:    task.Title,
			Notes:    task.Notes,
			Priority: task.Priority,
			DueAt:    task.DueAt,
			Tags:     task.Tags,
		},
	}
	if task.SeriesID != nil && *task.SeriesID != "" {
		in.ID = *task.SeriesID
		_, err = writer.UpdateSeries(r.Context(), in)
	} else {
		// RepeatTask rather than CreateSeries: this task becomes the first
		// instance. CreateSeries materializes from the template, which from
		// here would leave an exact duplicate sitting next to the original.
		_, _, err = writer.RepeatTask(r.Context(), u.actor(r), task.ID, in, u.Now())
	}
	if err != nil {
		u.detailBack(w, r, task.ID, err.Error())
		return
	}
	u.detailBack(w, r, task.ID, "repeats "+query.DescribeRecurrence(rule))
}

// detailBack returns to the task, carrying a message for the page to show.
func (u *UI) detailBack(w http.ResponseWriter, r *http.Request, taskID, msg string) {
	target := "/"
	if taskID != "" {
		target = "/t/" + url.PathEscape(taskID)
	}
	if msg != "" {
		target += "?m=" + url.QueryEscape(msg)
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}
