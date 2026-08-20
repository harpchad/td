package mcpsrv

import (
	"context"
	"errors"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/query"
	"github.com/harpchad/td/internal/store"
)

// The tool set is section 10's list, no more. Each one maps onto a store call
// the REST API already makes; none of them reaches past the service layer.

// TaskView is what every tool returns for a task.
//
// It is a projection rather than api.Task because a model reading a list does
// not need updated_at, due_is_date, or an external revision, and every field
// that goes over the wire is a field it can be confused by. The id is the
// short number, which is what a person types and what the model should quote
// back.
type TaskView struct {
	Num      int64    `json:"num" jsonschema:"the short number you refer to this task by"`
	ID       string   `json:"id" jsonschema:"the stable id, for tools that take one"`
	Title    string   `json:"title"`
	Status   string   `json:"status" jsonschema:"inbox, todo, doing, waiting, done, or dropped"`
	Priority *int     `json:"priority,omitempty" jsonschema:"1 is highest, 4 is lowest, absent is unset"`
	DueAt    *string  `json:"due_at,omitempty"`
	StartAt  *string  `json:"start_at,omitempty" jsonschema:"hidden from whats_next and default lists before this date"`
	Repeats  string   `json:"repeats,omitempty" jsonschema:"the RFC 5545 rule this task recurs on, when it is a series instance"`
	Tags     []string `json:"tags,omitempty"`
	People   []string `json:"people,omitempty" jsonschema:"handles with their role, as name:role"`
	Notes    string   `json:"notes,omitempty"`
	URL      string   `json:"external_url,omitempty" jsonschema:"the source item, when this task is a mirror"`
	Source   string   `json:"source" jsonschema:"local, or the system this was mirrored from"`

	Subtasks     int `json:"subtasks,omitempty"`
	SubtasksDone int `json:"subtasks_done,omitempty"`

	New bool `json:"new,omitempty" jsonschema:"the owner has not looked at this since it arrived from a sync, a plugin, or an agent. Reading it here does not clear it"`
}

func view(t api.Task) TaskView {
	out := TaskView{
		Num: t.Num, ID: t.ID, Title: t.Title, Status: t.Status,
		Priority: t.Priority, DueAt: t.DueAt, StartAt: t.StartAt, Tags: t.Tags,
		Notes: t.Notes, Source: t.Source,
		Subtasks: t.ChildrenTotal, SubtasksDone: t.ChildrenDone,
		New: t.New,
	}
	if t.ExternalURL != nil {
		out.URL = *t.ExternalURL
	}
	for _, p := range t.People {
		out.People = append(out.People, p.Name+":"+p.Role)
	}
	return out
}

func views(tasks []api.Task) []TaskView {
	out := make([]TaskView, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, view(t))
	}
	return out
}

// --- arguments and results ---

type searchArgs struct {
	Query string `json:"query" jsonschema:"a td filter, for example: is:open #certs @stacey p:2 due:friday. An empty query matches everything"`
	Limit int    `json:"limit,omitempty" jsonschema:"stop after this many tasks. 0 means no limit"`
}

type taskListResult struct {
	Tasks []TaskView `json:"tasks"`
	Total int        `json:"total"`
	// Filter is the query that produced this list. whats_next applies one of
	// its own, and a caller comparing its count against a search of is:open
	// has no way to see why they differ unless the answer says what it asked.
	Filter string `json:"filter,omitempty"`
}

type refArgs struct {
	ID string `json:"id" jsonschema:"a task number or id"`
}

type taskResult struct {
	Task TaskView `json:"task"`
}

type captureArgs struct {
	Title string `json:"title" jsonschema:"one line, as a person would type it. #tag @person p:2 due:friday are read out of it"`
	Notes string `json:"notes,omitempty"`
}

type createArgs struct {
	Title    string   `json:"title"`
	Notes    string   `json:"notes,omitempty"`
	Priority *int     `json:"priority,omitempty" jsonschema:"1 to 4, 1 is highest"`
	DueAt    string   `json:"due_at,omitempty" jsonschema:"a date, or a keyword like friday or tomorrow"`
	StartAt  string   `json:"start_at,omitempty" jsonschema:"defer until: hidden from whats_next and default lists before this date. A date or a keyword"`
	Repeat   string   `json:"repeat,omitempty" jsonschema:"make it recurring, in words: every monday, every 2 weeks, every month on the 3rd. The due date is the first occurrence and anchors the rule; a start date recurs with it, the same distance ahead of each due"`
	Tags     []string `json:"tags,omitempty"`
	People   []string `json:"people,omitempty" jsonschema:"handles, optionally as handle:role"`
	ParentID string   `json:"parent_id,omitempty" jsonschema:"make this a subtask of that task"`
}

type updateArgs struct {
	ID       string   `json:"id" jsonschema:"a task number or id"`
	Title    string   `json:"title,omitempty"`
	Notes    string   `json:"notes,omitempty"`
	Status   string   `json:"status,omitempty" jsonschema:"todo, doing, waiting, done, or dropped"`
	Priority *int     `json:"priority,omitempty" jsonschema:"1 to 4"`
	DueAt    string   `json:"due_at,omitempty" jsonschema:"a date or a keyword. Pass \"none\" to clear it"`
	StartAt  string   `json:"start_at,omitempty" jsonschema:"defer until this date. Pass \"none\" to clear it"`
	Repeat   string   `json:"repeat,omitempty" jsonschema:"set or change how this task repeats, in words: every month on the 3rd. Edits the series behind the task, from the next occurrence on"`
	Tags     []string `json:"tags,omitempty" jsonschema:"replaces the whole set"`
}

type noteArgs struct {
	ID   string `json:"id" jsonschema:"a task number or id"`
	Text string `json:"text" jsonschema:"appended to the existing notes, never replacing them"`
}

type personArgs struct {
	Person string `json:"person" jsonschema:"a handle, with or without the leading @"`
}

type limitArgs struct {
	Limit int `json:"limit,omitempty" jsonschema:"how many tasks to return. Defaults to 5"`
}

type sinceArgs struct {
	Since int64 `json:"since,omitempty" jsonschema:"return events after this sequence number. 0 starts at the beginning"`
	Limit int   `json:"limit,omitempty" jsonschema:"how many events. Defaults to 50"`
}

type peopleResult struct {
	People []PersonView `json:"people"`
}

// PersonView is a person as a model reads them.
type PersonView struct {
	Handle string   `json:"handle"`
	Name   string   `json:"name"`
	Groups []string `json:"groups,omitempty"`
}

type agendaResult struct {
	Person   PersonView `json:"person"`
	Assigned []TaskView `json:"assigned" jsonschema:"what they owe you"`
	Owed     []TaskView `json:"owed" jsonschema:"what you owe them"`
	Waiting  []TaskView `json:"waiting" jsonschema:"tasks blocked on them"`
	Agenda   []TaskView `json:"agenda" jsonschema:"things tagged for the next conversation"`
}

// EventView is one entry in the activity feed.
type EventView struct {
	Seq   int64  `json:"seq"`
	At    string `json:"at"`
	Actor string `json:"actor" jsonschema:"me, mcp:<name>, plugin:<source>, or scheduler"`
	Kind  string `json:"kind"`
	Task  int64  `json:"task,omitempty" jsonschema:"the task number, when the event has one"`
}

type activityResult struct {
	Events []EventView `json:"events"`
	// Cursor is where a poll resumes. The event log is the change feed, so a
	// client that keeps this does not have to re-list.
	Cursor int64 `json:"cursor"`
}

// ToolScopes is the scope each tool needs, and the only place that is written
// down. The handlers check it through requireScope and the HTTP layer reads it
// to answer a scope failure with a step-up challenge before the call is
// dispatched, so the two cannot disagree about what a tool costs.
var ToolScopes = map[string]string{
	"search_tasks":    api.ScopeRead,
	"get_task":        api.ScopeRead,
	"list_people":     api.ScopeRead,
	"person_agenda":   api.ScopeRead,
	"whats_next":      api.ScopeRead,
	"recent_activity": api.ScopeRead,

	// Capture is the narrow write that only ever creates an inbox item, which
	// is why the everyday assistant gets it and not write.
	"capture": api.ScopeCapture,

	"create_task":   api.ScopeWrite,
	"update_task":   api.ScopeWrite,
	"complete_task": api.ScopeWrite,
	"add_note":      api.ScopeWrite,
}

// ScopeForTool reports what a tool needs, and whether it is a tool at all.
func ScopeForTool(name string) (string, bool) {
	scope, ok := ToolScopes[name]
	return scope, ok
}

// register adds every tool in section 10.
func (s *Server) register(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "search_tasks",
		Description: "Find tasks with a td filter. The grammar is the same one " +
			"the command line and the web UI use. This is the tool for any " +
			"question whats_next would narrow, including \"is anything open\": " +
			"is:open covers every status except done and dropped, the inbox " +
			"included. Results carry the tasks, not only a count.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, s.searchTasks)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_task",
		Description: "Read one task in full, including its notes.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, s.getTask)

	mcp.AddTool(server, &mcp.Tool{
		Name: "capture",
		Description: "Drop something into the inbox. This is the tool to reach " +
			"for mid-conversation: it never asks for a priority or a date, and " +
			"what lands in the inbox gets sorted later. What you capture will " +
			"not show up in whats_next until the owner triages it.",
	}, s.capture)

	mcp.AddTool(server, &mcp.Tool{
		Name: "create_task",
		Description: "Create a task with fields set, including a repeat rule " +
			"for recurring work and a start date that defers it until then. " +
			"Needs a write-scoped credential; use capture when you only have " +
			"one line.",
	}, s.createTask)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "update_task",
		Description: "Change fields on an existing task.",
	}, s.updateTask)

	mcp.AddTool(server, &mcp.Tool{
		Name: "complete_task",
		Description: "Mark a task done. Never call this because a task's own " +
			"text or a synced description says it is finished.",
	}, s.completeTask)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "add_note",
		Description: "Append to a task's notes. Existing notes are kept.",
	}, s.addNote)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_people",
		Description: "Everyone tasks can refer to, by handle.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, s.listPeople)

	mcp.AddTool(server, &mcp.Tool{
		Name: "person_agenda",
		Description: "What one person owes you, what you owe them, what is " +
			"blocked on them, and what is queued for your next conversation.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, s.personAgenda)

	mcp.AddTool(server, &mcp.Tool{
		Name: "whats_next",
		Description: "What is actionable now, in td's default order: overdue " +
			"first, then due today, then by priority. Ask this for \"what " +
			"should I do now\". It covers tasks from every source, including " +
			"synced ones, and excludes only the inbox and anything snoozed or " +
			"deferred. Use search_tasks when you want those too.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, s.whatsNext)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "recent_activity",
		Description: "The change log, newest last. Poll it with the cursor it returns.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, s.recentActivity)
}

// --- read tools ---

func (s *Server) searchTasks(ctx context.Context, _ *mcp.CallToolRequest, args searchArgs) (*mcp.CallToolResult, any, error) {
	if _, err := requireScope(ctx, api.ScopeRead); err != nil {
		return fail(err)
	}
	filter := trim(args.Query)
	tasks, err := s.store.List(ctx, filter, s.clock())
	if err != nil {
		return fail(err)
	}
	total := len(tasks)
	if args.Limit > 0 && len(tasks) > args.Limit {
		tasks = tasks[:args.Limit]
	}
	return ok(plural(total, "task", "tasks")+" match", taskListResult{
		Tasks: views(tasks), Total: total, Filter: filter,
	})
}

func (s *Server) getTask(ctx context.Context, _ *mcp.CallToolRequest, args refArgs) (*mcp.CallToolResult, any, error) {
	if _, err := requireScope(ctx, api.ScopeRead); err != nil {
		return fail(err)
	}
	task, err := s.resolve(ctx, args.ID)
	if err != nil {
		return fail(err)
	}
	v := view(task)
	// The full read is where the rule is worth a lookup: a model checking
	// what it created needs to see the recurrence it asked for.
	if task.SeriesID != nil && *task.SeriesID != "" {
		if series, err := s.store.Series(ctx, *task.SeriesID); err == nil {
			v.Repeats = series.RRule
		}
	}
	return ok(task.Title, taskResult{Task: v})
}

func (s *Server) whatsNext(ctx context.Context, _ *mcp.CallToolRequest, args limitArgs) (*mcp.CallToolResult, any, error) {
	if _, err := requireScope(ctx, api.ScopeRead); err != nil {
		return fail(err)
	}
	limit := args.Limit
	if limit <= 0 {
		limit = 5
	}

	// Everything actionable, whoever it came from. Snoozed and deferred tasks
	// are hidden because you decided they are not for now, and the inbox is
	// hidden because it is a pile to sort rather than a list to work.
	//
	// Mirrors are deliberately included, which is a departure from section 16
	// note 4 and from the web home this used to copy. That note's reasoning
	// was that a read-only backlog buries your own list. When the mirror is
	// where the real work lives, the opposite happens: the tool for "what
	// should I do now" answers "nothing" while a board report is due.
	// DECISIONS.md carries the argument.
	const nextFilter = "is:open -is:inbox -is:snoozed -is:deferred"
	tasks, err := s.store.List(ctx, nextFilter, s.clock())
	if err != nil {
		return fail(err)
	}
	total := len(tasks)
	if len(tasks) > limit {
		tasks = tasks[:limit]
	}

	// The summary names the filter rather than claiming something about every
	// task. "0 tasks are open" was read, reasonably, as a statement about the
	// whole list, and it disagreed with what search_tasks said about is:open a
	// minute later. This tool answers a narrower question and now says so.
	summary := plural(total, "task is", "tasks are") + " actionable now (" + nextFilter + ")"
	if total == 0 {
		summary = s.emptySummary(ctx, nextFilter)
	}
	return ok(summary, taskListResult{Tasks: views(tasks), Total: total, Filter: nextFilter})
}

// emptySummary says what an empty answer is hiding.
//
// "Nothing to do" is the wrong answer when a mirrored board is where all your
// real work lives, and it is worse than wrong: it reads as reassurance. If the
// only reason this list is empty is a term in the filter, the count that term
// removed is the useful part of the reply, so it is counted and named.
func (s *Server) emptySummary(ctx context.Context, nextFilter string) string {
	out := "nothing is actionable now (" + nextFilter + ")"

	var hidden []string
	if n := s.count(ctx, "is:inbox"); n > 0 {
		hidden = append(hidden, plural(n, "is", "are")+" waiting in the inbox")
	}
	if n := s.count(ctx, "is:open is:snoozed"); n > 0 {
		hidden = append(hidden, plural(n, "is", "are")+" snoozed")
	}
	if n := s.count(ctx, "is:open is:deferred"); n > 0 {
		hidden = append(hidden, plural(n, "has", "have")+" not started yet")
	}
	if len(hidden) == 0 {
		return out + ". Nothing else is open either."
	}
	return out + ", but " + strings.Join(hidden, " and ") +
		". Use search_tasks to read them."
}

// count runs a filter for its size alone, and reports zero rather than an
// error: this only ever decorates a summary, and a failed count must not turn
// a working answer into a failure.
func (s *Server) count(ctx context.Context, filter string) int {
	tasks, err := s.store.List(ctx, filter, s.clock())
	if err != nil {
		return 0
	}
	return len(tasks)
}

func (s *Server) listPeople(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
	if _, err := requireScope(ctx, api.ScopeRead); err != nil {
		return fail(err)
	}
	people, err := s.store.People(ctx)
	if err != nil {
		return fail(err)
	}
	out := make([]PersonView, 0, len(people))
	for _, p := range people {
		out = append(out, PersonView{Handle: p.Handle, Name: p.Name})
	}
	return ok(plural(len(out), "person", "people"), peopleResult{People: out})
}

func (s *Server) personAgenda(ctx context.Context, _ *mcp.CallToolRequest, args personArgs) (*mcp.CallToolResult, any, error) {
	if _, err := requireScope(ctx, api.ScopeRead); err != nil {
		return fail(err)
	}
	person, err := s.store.ResolvePerson(ctx, strings.TrimPrefix(trim(args.Person), "@"))
	if err != nil {
		return fail(err)
	}
	page, err := s.store.PersonPage(ctx, person.ID, s.clock())
	if err != nil {
		return fail(err)
	}
	return ok(page.Person.Name, agendaResult{
		Person: PersonView{
			Handle: page.Person.Handle, Name: page.Person.Name, Groups: page.Groups,
		},
		Assigned: views(page.Assigned),
		Owed:     views(page.Owed),
		Waiting:  views(page.Waiting),
		Agenda:   views(page.Agenda),
	})
}

func (s *Server) recentActivity(ctx context.Context, _ *mcp.CallToolRequest, args sinceArgs) (*mcp.CallToolResult, any, error) {
	if _, err := requireScope(ctx, api.ScopeRead); err != nil {
		return fail(err)
	}
	limit := args.Limit
	if limit <= 0 {
		limit = 50
	}
	events, err := s.store.Events(ctx, args.Since, limit)
	if err != nil {
		return fail(err)
	}

	// Numbers rather than ids, because a number is what the rest of the tool
	// output uses and a model that has to join two id spaces will get it
	// wrong eventually.
	nums := map[string]int64{}
	out := make([]EventView, 0, len(events))
	cursor := args.Since
	for _, e := range events {
		item := EventView{Seq: e.Seq, At: e.At, Actor: e.Actor, Kind: e.Kind}
		if e.TaskID != "" {
			num, ok := nums[e.TaskID]
			if !ok {
				if task, err := s.store.Get(ctx, e.TaskID); err == nil {
					num = task.Num
				}
				nums[e.TaskID] = num
			}
			item.Task = num
		}
		out = append(out, item)
		if e.Seq > cursor {
			cursor = e.Seq
		}
	}
	return ok(plural(len(out), "event", "events"), activityResult{Events: out, Cursor: cursor})
}

// --- write tools ---

func (s *Server) capture(ctx context.Context, _ *mcp.CallToolRequest, args captureArgs) (*mcp.CallToolResult, any, error) {
	p, err := requireScope(ctx, api.ScopeCapture)
	if err != nil {
		return fail(err)
	}
	line := trim(args.Title)
	if line == "" {
		return fail(errors.New("say what to capture"))
	}

	now := s.clock()
	capture := query.ParseCapture(line, now)
	if capture.Title == "" {
		return fail(errors.New("that is all tags and no task"))
	}

	task, err := s.store.Create(ctx, p.Actor, api.TaskCreate{
		Title: capture.Title, Notes: trim(args.Notes),
		Priority: capture.Priority, DueAt: capture.Due, StartAt: capture.Start,
		Tags: capture.Tags, People: capture.People,
	}, now)
	if err != nil {
		return fail(err)
	}
	return ok("captured "+itoa(int(task.Num))+" in "+task.Status, taskResult{Task: view(task)})
}

func (s *Server) createTask(ctx context.Context, _ *mcp.CallToolRequest, args createArgs) (*mcp.CallToolResult, any, error) {
	p, err := requireScope(ctx, api.ScopeWrite)
	if err != nil {
		return fail(err)
	}
	if trim(args.Title) == "" {
		return fail(errors.New("a task needs a title"))
	}

	now := s.clock()
	in := api.TaskCreate{
		Title: trim(args.Title), Notes: trim(args.Notes),
		Priority: args.Priority, Tags: args.Tags, People: args.People,
	}
	if due := trim(args.DueAt); due != "" {
		resolved, err := query.ResolveDate(due, now)
		if err != nil {
			return fail(err)
		}
		in.DueAt = &resolved
	}
	if start := trim(args.StartAt); start != "" {
		resolved, err := query.ResolveDate(start, now)
		if err != nil {
			return fail(err)
		}
		in.StartAt = &resolved
	}
	if ref := trim(args.ParentID); ref != "" {
		parent, err := s.store.Resolve(ctx, ref)
		if err != nil {
			return fail(err)
		}
		in.ParentID = &parent
	}

	if repeat := trim(args.Repeat); repeat != "" {
		rule, err := query.ParseRecurrence(repeat)
		if err != nil {
			return fail(err)
		}
		series, task, err := s.store.CreateSeries(ctx, p.Actor,
			store.Series{RRule: rule, Template: in}, now)
		if err != nil {
			return fail(err)
		}
		v := view(task)
		v.Repeats = series.RRule
		return ok("created "+itoa(int(task.Num))+", repeats "+series.RRule,
			taskResult{Task: v})
	}

	task, err := s.store.Create(ctx, p.Actor, in, now)
	if err != nil {
		return fail(err)
	}
	return ok("created "+itoa(int(task.Num)), taskResult{Task: view(task)})
}

func (s *Server) updateTask(ctx context.Context, _ *mcp.CallToolRequest, args updateArgs) (*mcp.CallToolResult, any, error) {
	p, err := requireScope(ctx, api.ScopeWrite)
	if err != nil {
		return fail(err)
	}
	id, err := s.store.Resolve(ctx, trim(args.ID))
	if err != nil {
		return fail(err)
	}

	now := s.clock()
	patch := api.TaskPatch{Presence: map[string]bool{}}
	if title := trim(args.Title); title != "" {
		patch.Title = &title
	}
	if notes := trim(args.Notes); notes != "" {
		patch.Notes = &notes
	}
	if status := trim(args.Status); status != "" {
		patch.Status = &status
	}
	if args.Priority != nil {
		patch.Priority, patch.Presence["priority"] = args.Priority, true
	}
	if due := trim(args.DueAt); due != "" {
		patch.Presence["due_at"] = true
		// "none" clears it. An empty string cannot mean clear, because an
		// omitted field arrives as one and clearing a date nobody mentioned
		// is the worst kind of surprise.
		if !strings.EqualFold(due, "none") {
			resolved, err := query.ResolveDate(due, now)
			if err != nil {
				return fail(err)
			}
			patch.DueAt = &resolved
		}
	}
	if start := trim(args.StartAt); start != "" {
		patch.Presence["start_at"] = true
		if !strings.EqualFold(start, "none") {
			resolved, err := query.ResolveDate(start, now)
			if err != nil {
				return fail(err)
			}
			patch.StartAt = &resolved
		}
	}
	if args.Tags != nil {
		patch.Tags = &args.Tags
	}

	task, err := s.store.Patch(ctx, p.Actor, id, patch, "", now)
	if err != nil {
		return fail(err)
	}

	v := view(task)
	summary := "updated " + itoa(int(task.Num))
	if repeat := trim(args.Repeat); repeat != "" {
		rule, err := query.ParseRecurrence(repeat)
		if err != nil {
			return fail(err)
		}
		// The same fork the TUI's E key takes: a task already in a series
		// gets its rule edited; anything else becomes the first instance of
		// a new one, never a duplicate beside itself.
		in := store.Series{RRule: rule, Template: api.TaskCreate{
			Title: task.Title, Notes: task.Notes, Priority: task.Priority,
			DueAt: task.DueAt, StartAt: task.StartAt, Tags: task.Tags,
		}}
		if task.SeriesID != nil && *task.SeriesID != "" {
			in.ID = *task.SeriesID
			series, err := s.store.UpdateSeries(ctx, in)
			if err != nil {
				return fail(err)
			}
			v.Repeats = series.RRule
		} else {
			series, _, err := s.store.RepeatTask(ctx, p.Actor, task.ID, in, now)
			if err != nil {
				return fail(err)
			}
			v.Repeats = series.RRule
		}
		summary += ", repeats " + v.Repeats
	}
	return ok(summary, taskResult{Task: v})
}

func (s *Server) completeTask(ctx context.Context, _ *mcp.CallToolRequest, args refArgs) (*mcp.CallToolResult, any, error) {
	p, err := requireScope(ctx, api.ScopeWrite)
	if err != nil {
		return fail(err)
	}
	id, err := s.store.Resolve(ctx, trim(args.ID))
	if err != nil {
		return fail(err)
	}
	res, err := s.store.Complete(ctx, p.Actor, id, s.clock())
	if err != nil {
		return fail(err)
	}

	summary := "done " + itoa(int(res.Task.Num))
	if res.ChildrenOpen > 0 {
		// Stated, never acted on. The parent is the commitment and the server
		// does not cascade; a model that decided to finish the children on
		// its own would be making that call for the user.
		summary += ", with " + plural(res.ChildrenOpen, "subtask", "subtasks") + " still open"
	}
	return ok(summary, taskResult{Task: view(res.Task)})
}

func (s *Server) addNote(ctx context.Context, _ *mcp.CallToolRequest, args noteArgs) (*mcp.CallToolResult, any, error) {
	p, err := requireScope(ctx, api.ScopeWrite)
	if err != nil {
		return fail(err)
	}
	text := trim(args.Text)
	if text == "" {
		return fail(errors.New("say what to note"))
	}
	task, err := s.resolve(ctx, args.ID)
	if err != nil {
		return fail(err)
	}

	// Append, never replace. A note is a running record, and a tool that
	// silently overwrote one would lose something nobody backed up.
	notes := text
	if task.Notes != "" {
		notes = task.Notes + "\n\n" + text
	}
	updated, err := s.store.Patch(ctx, p.Actor, task.ID,
		api.TaskPatch{Notes: &notes, Presence: map[string]bool{}}, "", s.clock())
	if err != nil {
		return fail(err)
	}
	return ok("noted on "+itoa(int(updated.Num)), taskResult{Task: view(updated)})
}

// resolve turns a number or an id into a task.
func (s *Server) resolve(ctx context.Context, ref string) (api.Task, error) {
	id, err := s.store.Resolve(ctx, trim(ref))
	if err != nil {
		return api.Task{}, err
	}
	return s.store.Get(ctx, id)
}
