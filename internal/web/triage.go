package web

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/query"
)

// TriageFilter is what the triage screen works through. The inbox is the
// triage bucket, and nothing but quick-add lands there.
const TriageFilter = "is:inbox"

// triage renders one inbox task at a time.
//
// A dedicated screen rather than a filtered list, the same as the TUI. The
// list view cannot get you from 20 to 0 in two minutes because every decision
// makes the eye hunt for the next row; one card and single-key actions can.
func (u *UI) triage(w http.ResponseWriter, r *http.Request) {
	tasks, err := u.svc.List(r.Context(), TriageFilter, u.Now())
	if err != nil {
		u.fail(w, r, err)
		return
	}

	data := u.base(r, "Triage")
	data.Status = r.URL.Query().Get("m")
	data.TriageTotal = len(tasks)

	// The position is in the URL rather than in a session: reloading the page
	// has to land in the same place, and the back button has to walk back
	// through the queue.
	at, _ := strconv.Atoi(r.URL.Query().Get("i"))
	if at < 0 {
		at = 0
	}
	if at >= len(tasks) {
		data.InboxZero = true
		data.TriageIndex = len(tasks)
		u.render(w, "triage", data)
		return
	}

	task := tasks[at]
	data.TriageIndex = at
	data.TriageNext = at + 1
	data.TriagePrev = max(at-1, 0)
	data.Task = task
	data.PriorityClass = priorityClass(task.Priority)
	data.PriorityLabel = priorityLabel(task.Priority)
	if task.DueAt != nil {
		data.Due, data.Overdue = dueLabel(task, u.Now())
		data.DueValue = query.LocalDate(*task.DueAt, u.Now().Location())
	}
	for _, tag := range task.Tags {
		data.Tags = append(data.Tags, Token{Label: "#" + tag, Query: "#" + tag})
	}
	for _, p := range task.People {
		handle := firstWordLower(p.Name)
		data.People = append(data.People, DetailPerson{
			Role: p.Role, Label: "@" + handle, Query: "@" + handle, Href: "/p/" + handle,
		})
	}
	data.TagValue = strings.Join(task.Tags, " ")

	// Radios rather than four buttons: the current value has to be visible,
	// and "no priority yet" is a state the control must be able to show.
	for _, p := range []struct{ value, label string }{
		{"", "none"}, {"1", "p1"}, {"2", "p2"}, {"3", "p3"}, {"4", "p4"},
	} {
		selected := p.value == ""
		if task.Priority != nil {
			selected = p.value == itoa(int64(*task.Priority))
		}
		data.Priorities = append(data.Priorities, notifyChoice{
			Value: p.value, Label: p.label, Selected: selected,
		})
	}

	u.render(w, "triage", data)
}

// triageAction applies one decision and moves to the next card.
//
// The index does not advance when the queue shrinks: removing the task at
// position 3 makes what was position 4 the new 3, so staying put is what puts
// the next task on screen.
func (u *UI) triageAction(w http.ResponseWriter, r *http.Request) {
	id, err := u.svc.Resolve(r.Context(), r.PathValue("id"))
	if err != nil {
		u.backToTriage(w, r, 0, "no task with that id")
		return
	}
	at, _ := strconv.Atoi(r.PostFormValue("at"))

	patcher, ok := u.svc.(taskPatcher)
	if !ok {
		u.backToTriage(w, r, at, "triage is not available")
		return
	}

	switch action := r.PostFormValue("do"); action {
	case "skip":
		u.backToTriage(w, r, at+1, "")

	case "drop":
		if _, err := u.svc.Drop(r.Context(), u.actor(r), id, u.Now()); err != nil {
			u.backToTriage(w, r, at, humanError(err))
			return
		}
		u.backToTriage(w, r, at, "dropped")

	case "done":
		if _, err := u.svc.Complete(r.Context(), u.actor(r), id, u.Now()); err != nil {
			u.backToTriage(w, r, at, humanError(err))
			return
		}
		u.backToTriage(w, r, at, "done")

	case "promote":
		status := api.StatusTodo
		patch := api.TaskPatch{Status: &status, Presence: map[string]bool{}}

		// A priority or a due date is what lets a task leave the inbox, so
		// setting one here promotes in the same request rather than making
		// the user press two buttons that always go together.
		if raw := strings.TrimSpace(r.PostFormValue("priority")); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 || n > 4 {
				u.backToTriage(w, r, at, "priority is 1 to 4")
				return
			}
			patch.Priority, patch.Presence["priority"] = &n, true
		}
		if raw := strings.TrimSpace(r.PostFormValue("due")); raw != "" {
			resolved, err := query.ResolveDate(raw, u.Now())
			if err != nil {
				u.backToTriage(w, r, at, "could not read that date")
				return
			}
			patch.DueAt, patch.Presence["due_at"] = &resolved, true
		}
		if raw := r.PostFormValue("tags"); raw != "" {
			tags := []string{}
			for _, tag := range strings.Fields(raw) {
				if tag = strings.TrimPrefix(tag, "#"); tag != "" {
					tags = append(tags, strings.ToLower(tag))
				}
			}
			patch.Tags = &tags
		}

		if _, err := patcher.Patch(r.Context(), u.actor(r), id, patch, "", u.Now()); err != nil {
			u.backToTriage(w, r, at, humanError(err))
			return
		}
		u.backToTriage(w, r, at, "promoted")

	default:
		u.backToTriage(w, r, at, "")
	}
}

func (u *UI) backToTriage(w http.ResponseWriter, r *http.Request, at int, status string) {
	target := "/triage?i=" + strconv.Itoa(max(at, 0))
	if status != "" {
		target += "&m=" + urlEncode(status)
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}
