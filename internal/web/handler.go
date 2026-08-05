package web

import (
	"context"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/query"
	"github.com/harpchad/td/internal/store"
)

// ThemeCookie remembers the picked theme. It is a preference rather than
// data, so it lives in a cookie rather than in the database.
const ThemeCookie = "td_theme"

// ThemeAuto is re-exported so the templates and the server agree on the name.
const ThemeAuto = "auto"

// Service is what the web UI needs from the rest of the server. The handlers
// go through the same store every other client's requests do, so a bug shows
// up everywhere at once instead of hiding in the path you use least.
type Service interface {
	List(ctx context.Context, filter string, now time.Time) ([]api.Task, error)
	Get(ctx context.Context, id string) (api.Task, error)
	Resolve(ctx context.Context, ref string) (string, error)
	Create(ctx context.Context, actor string, in api.TaskCreate, now time.Time) (api.Task, error)
	Complete(ctx context.Context, actor, id string, now time.Time) (api.CompleteResult, error)
	Drop(ctx context.Context, actor, id string, now time.Time) (api.Task, error)
	Undo(ctx context.Context, actor string, now time.Time) (api.UndoResult, error)
	SavedFilters(ctx context.Context) ([]api.SavedFilter, error)
	CollapsedTasks(ctx context.Context) ([]string, error)
	SetCollapsed(ctx context.Context, taskID string, collapsed bool) error
	CurrentFilter(ctx context.Context) (string, bool, error)
	SetCurrentFilter(ctx context.Context, filter string) error
	Tokens(ctx context.Context) ([]api.Token, error)
	RevokeToken(ctx context.Context, id string, now time.Time) error
	HasAccount(ctx context.Context) (bool, error)
}

// UI serves the browser interface.
type UI struct {
	svc    Service
	assets *Assets
	tmpl   map[string]*template.Template
	log    *slog.Logger

	// Now is the clock every page renders against, and the same one the sort
	// order used.
	Now func() time.Time
	// ThemeDir is shown on the settings page so a file drop is discoverable.
	ThemeDir string
	// AssetVersion busts the cache when the assets change. It is a hash of
	// them rather than a build number, because they are served immutable.
	AssetVersion string
}

// New builds the web UI over a service and a set of assets.
func New(svc Service, assets *Assets, log *slog.Logger, now func() time.Time) (*UI, error) {
	if log == nil {
		log = slog.Default()
	}
	tmpl, err := parseTemplates()
	if err != nil {
		return nil, err
	}
	return &UI{
		svc: svc, assets: assets, tmpl: tmpl, log: log, Now: now,
		AssetVersion: assets.Version,
	}, nil
}

func parseTemplates() (map[string]*template.Template, error) {
	pages := []string{
		"home", "detail", "login", "help", "settings", "person", "triage",
		"consent", "oautherror",
	}
	// Fragments htmx swaps in. They render the "body" template on their own,
	// without the page around them.
	pages = append(pages, "connect")
	out := map[string]*template.Template{}

	for _, page := range pages {
		files := []string{"templates/layout.html", "templates/" + page + ".html"}
		if page == "home" {
			files = append(files, "templates/list.html")
		}
		t, err := template.New("layout").Funcs(templateFuncs()).ParseFS(templateFS, files...)
		if err != nil {
			return nil, err
		}
		out[page] = t
	}

	// The list fragment is rendered on its own for htmx swaps.
	frag, err := template.New("list").Funcs(templateFuncs()).ParseFS(templateFS, "templates/list.html")
	if err != nil {
		return nil, err
	}
	out["list"] = frag
	return out, nil
}

// templateFuncs is the whole template vocabulary. dict exists so a section
// partial can be handed a title and a list without a named type per section.
func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"add": func(a, b int) int { return a + b },
		"dict": func(pairs ...any) (map[string]any, error) {
			if len(pairs)%2 != 0 {
				return nil, errors.New("dict takes pairs")
			}
			out := make(map[string]any, len(pairs)/2)
			for i := 0; i < len(pairs); i += 2 {
				key, ok := pairs[i].(string)
				if !ok {
					return nil, errors.New("dict keys are strings")
				}
				out[key] = pairs[i+1]
			}
			return out, nil
		},
	}
}

// Routes registers the browser routes on a mux.
func (u *UI) Routes(mux *http.ServeMux) {
	// Registered for every method, not just GET. With "GET /" the mux answers
	// 405 for a POST to any unregistered path, which says "this exists but
	// not for that verb" about paths that do not exist at all. 404 is the
	// true answer, and it is what the no-registration-route test asserts.
	mux.HandleFunc("/", u.home)
	mux.HandleFunc("GET /t/{ref}", u.detail)
	mux.HandleFunc("GET /help", u.help)
	mux.HandleFunc("GET /settings", u.settings)
	mux.HandleFunc("GET /p/{ref}", u.person)
	mux.HandleFunc("GET /triage", u.triage)

	mux.HandleFunc("POST /w/add", u.add)
	mux.HandleFunc("POST /w/complete/{id}", u.complete)
	mux.HandleFunc("POST /w/drop/{id}", u.drop)
	mux.HandleFunc("POST /w/edit/{id}", u.edit)
	mux.HandleFunc("POST /w/snooze/{id}", u.snooze)
	mux.HandleFunc("POST /w/snooze/{id}/clear", u.wake)
	mux.HandleFunc("POST /w/undo", u.undo)
	mux.HandleFunc("POST /w/triage/{id}", u.triageAction)
	mux.HandleFunc("POST /w/sub/{id}", u.addSubtask)
	mux.HandleFunc("POST /w/repeat/{id}", u.repeat)
	mux.HandleFunc("POST /w/fold/{id}", u.fold)
	mux.HandleFunc("POST /w/theme", u.setTheme)
	mux.HandleFunc("POST /w/tokens/{id}/revoke", u.revokeToken)
	mux.HandleFunc("POST /w/grants/{id}/revoke", u.RevokeGrant)
	mux.HandleFunc("POST /w/plugins/{name}", u.savePlugin)

	mux.HandleFunc("GET /static/td.css", u.asset(u.assets.CSS, "text/css; charset=utf-8"))
	mux.HandleFunc("GET /static/td.js", u.asset(u.assets.Script, "text/javascript; charset=utf-8"))
	mux.HandleFunc("GET /static/htmx.min.js", u.asset(u.assets.HTMX, "text/javascript; charset=utf-8"))
}

func (u *UI) asset(body []byte, contentType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		// Immutable because the URL carries the build version.
		if r.URL.Query().Get("v") != "" {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		_, _ = w.Write(body)
	}
}

// pageData is everything a template can read.
type pageData struct {
	Title        string
	Theme        string
	AssetVersion string

	// Consent screen: who is asking, and where the code would actually go.
	RedirectHost  string
	LoopbackOnly  bool
	SelfDescribed bool

	Filter     string
	FilterName string
	Saved      []api.SavedFilter
	Counts     Counts
	Rows       []Row
	InboxZero  bool

	// Triage is a dedicated screen rather than a filter preset. The position
	// lives in the URL so reloading lands in the same place and the back
	// button walks back through the queue.
	TriageTotal int
	TriageIndex int
	TriageNext  int
	TriagePrev  int
	Priorities  []notifyChoice

	// Attachments on the detail page. Every download goes through the API's
	// auth check; there is no static handler over the blob directory.
	Attachments []attachmentRow
	Children    []Row
	// SnoozedUntil is the wake time, shown so a snoozed task says so. There is
	// no prefill for the field: it takes a duration, and last time's "2h"
	// means nothing now.
	SnoozedUntil string
	// StartValue prefills the start date in the edit form.
	StartValue string

	Repeats string
	// RepeatRule is the stored RRULE behind Repeats, shown small next to the
	// readable form: the description is for people and the rule is what the
	// server will actually run.
	RepeatRule  string
	RepeatValue string

	Task        api.Task
	Done        bool
	Overdue     bool
	Due         string
	Tags        []Token
	People      []DetailPerson
	ExternalURL string

	PriorityClass string
	PriorityLabel string
	// The edit form's current values, as the form wants them.
	PriorityValue string
	DueValue      string
	TagValue      string
	NotifyChoices []notifyChoice

	// The person page: a first-class screen rather than a filter preset.
	Page        api.PersonPage
	Assigned    []Row
	Owed        []Row
	Involved    []Row
	Agenda      []Row
	GroupTasks  []Row
	Waiting     []waitingRow
	PersonQuery string

	Themes   []themeChoice
	ThemeDir string
	Tokens   []tokenRow
	Keys     []keyRow

	// The OAuth consent screen. The scopes are checkboxes because the point
	// of a consent screen is that it can grant less than was asked for.
	// The Planner mirror on the settings page, and the device code panel
	// htmx swaps in while you sign in.
	Plugins []pluginView
	Connect ConnectCode

	ClientName string
	Request    string
	// Next is where the login form returns to. Same-origin paths only.
	Next          string
	ConsentScopes []consentScope
	Grants        []grantRow

	NoAccount bool
	Status    string
	Error     string
}

// DetailPerson is a person link on the detail page, carrying its role.
type DetailPerson struct {
	Role  string
	Label string
	Query string
	Href  string
}

// notifyChoice is one radio of the tri-state.
type notifyChoice struct {
	Value    string
	Label    string
	Selected bool
}

// waitingRow carries the age alongside the row, which is the whole reason
// the waiting section is worth having.
type waitingRow struct {
	Row Row
	Age string
	// Stale marks the ones that have been waiting long enough to chase. The
	// spec calls "waiting more than 7 days" a view worth building on day one.
	Stale bool
}

// attachmentRow is one file on the detail page.
type attachmentRow struct {
	ID, Filename, Size, Mime, Href string
}

type themeChoice struct {
	Name     string
	Label    string
	BuiltIn  bool
	Selected bool
}

// grantRow is one OAuth grant on the settings page, next to the static
// tokens and with the same revoke button. claude.ai holds a refresh token for
// your task list and you want one place to cut it off.
type grantRow struct {
	ID, Client, Scopes, Resource, Created, LastUsed string
	Revoked                                         bool
}

type tokenRow struct {
	ID, Name, Prefix, Actor, Scopes, LastUsed string
	Revoked                                   bool
}

type keyRow struct {
	Keys string
	Help string
	// When says where an unimplemented key lands. A sentence rather than a
	// phase number: section 16 does not schedule all of them, and the TUI and
	// this page disagreed about the invented ones until they both stopped
	// inventing.
	When string
}

func (u *UI) base(r *http.Request, title string) pageData {
	return pageData{
		Title:        title,
		Theme:        u.themeOf(r),
		AssetVersion: u.AssetVersion,
	}
}

// themeOf returns the picked theme, or auto.
//
// Auto renders no data-theme attribute at all, which is what lets the
// prefers-color-scheme rule apply. Defaulting to light would mean a browser
// set to dark gets a light page until someone visits settings, and the
// system preference is the better first guess than either fixed palette.
func (u *UI) themeOf(r *http.Request) string {
	c, err := r.Cookie(ThemeCookie)
	if err != nil || c.Value == "" || c.Value == ThemeAuto {
		return ThemeAuto
	}
	for _, t := range u.assets.Themes {
		if t.Name == c.Value {
			return c.Value
		}
	}
	// A cookie naming a theme that has since been removed or rejected falls
	// back rather than rendering an undefined palette.
	return ThemeAuto
}

func (u *UI) home(w http.ResponseWriter, r *http.Request) {
	// This pattern catches everything unmatched, so anything that is not the
	// root is a 404 rather than the home page.
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "", http.StatusMethodNotAllowed)
		return
	}

	data := u.base(r, "Today")

	saved, err := u.svc.SavedFilters(r.Context())
	if err != nil {
		u.fail(w, r, err)
		return
	}
	data.Saved = saved

	// A `q` that is present but empty is somebody clearing the box, which is a
	// choice. A `q` that is absent is no choice at all, which is what every
	// back link and every bookmark of the bare root is.
	remembered, known := u.rememberedFilter(r)
	filter, chosen := r.URL.Query().Get("q"), r.URL.Query().Has("q")
	if !chosen {
		filter, chosen = remembered, known
	}
	if !chosen {
		filter = slotOne(saved)
	}
	u.fillList(r, &data, filter, "")

	// Only when it changed, so reading the same list twice is not two writes,
	// and only after it parsed: remembering a filter that does not is how you
	// wedge yourself out of your own home page with no way back but the URL bar.
	if chosen && data.Error == "" && (!known || remembered != filter) {
		if err := u.svc.SetCurrentFilter(r.Context(), filter); err != nil {
			u.log.Error("remembering the filter", "err", err)
		}
	}

	u.render(w, "home", data)
}

// rememberedFilter is where the list you were reading comes back from.
//
// The filter is server state rather than a query string that every link has to
// carry, which is what makes `href="/"` on a back link land you where you
// were. Threading `?q=` through six templates would have fixed the back links
// and still lost the filter when you closed the tab.
func (u *UI) rememberedFilter(r *http.Request) (string, bool) {
	filter, ok, err := u.svc.CurrentFilter(r.Context())
	if err != nil {
		u.log.Error("reading the remembered filter", "err", err)
		return "", false
	}
	return filter, ok
}

// slotOne is the first-run filter, before anybody has chosen anything.
func slotOne(saved []api.SavedFilter) string {
	for _, f := range saved {
		if f.Slot == 1 {
			return f.Query
		}
	}
	return ""
}

// fillList loads the list into the page data, and reports a filter that does
// not parse as an error rather than an empty list.
func (u *UI) fillList(r *http.Request, data *pageData, filter, status string) {
	data.Filter = filter
	data.Status = status
	data.FilterName = "Everything"

	if saved := data.Saved; saved != nil {
		for _, f := range saved {
			if f.Query == filter {
				data.FilterName = f.Name
				break
			}
		}
	}
	data.InboxZero = filter == "is:inbox"

	now := u.Now()
	tasks, err := u.svc.List(r.Context(), filter, now)
	if err != nil {
		var parseErr *query.ParseError
		if errors.As(err, &parseErr) {
			data.Error = parseErr.Msg
			return
		}
		u.log.Error("web list", "err", err)
		data.Error = "the server could not read that list"
		return
	}

	collapsed := map[string]bool{}
	if ids, err := u.svc.CollapsedTasks(r.Context()); err == nil {
		for _, id := range ids {
			collapsed[id] = true
		}
	}

	data.Counts = countOf(tasks, now)
	data.Rows = buildRows(tasks, collapsed, now)
}

// listFragment re-renders just the list, which is what every htmx action
// swaps in.
func (u *UI) listFragment(w http.ResponseWriter, r *http.Request, status string) {
	data := u.base(r, "Today")
	saved, err := u.svc.SavedFilters(r.Context())
	if err == nil {
		data.Saved = saved
	}

	// Parsed up front, because the difference between an absent `q` and an
	// empty one is the whole point and r.Form only exists once this has run.
	_ = r.ParseForm()

	// The same order as home, one step longer: an htmx action posts to its own
	// URL, so the list it came from is in the referer rather than in the form.
	filter, chosen := r.Form.Get("q"), r.Form.Has("q")
	if !chosen {
		if fromReferer := filterFromReferer(r); fromReferer != "" {
			filter, chosen = fromReferer, true
		}
	}
	if !chosen {
		filter, chosen = u.rememberedFilter(r)
	}
	if !chosen {
		filter = slotOne(data.Saved)
	}
	u.fillList(r, &data, filter, status)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := u.tmpl["list"].ExecuteTemplate(w, "list", data); err != nil {
		u.log.Error("render list fragment", "err", err)
	}
}

// filterFromReferer recovers the filter an htmx action was fired from, so
// completing a task on a filtered list re-renders that same list.
func filterFromReferer(r *http.Request) string {
	ref := r.Header.Get("HX-Current-URL")
	if ref == "" {
		ref = r.Referer()
	}
	if ref == "" {
		return ""
	}
	_, rawQuery, ok := strings.Cut(ref, "?")
	if !ok {
		return ""
	}
	values, err := parseQuery(rawQuery)
	if err != nil {
		return ""
	}
	return values.Get("q")
}

func (u *UI) detail(w http.ResponseWriter, r *http.Request) {
	id, err := u.svc.Resolve(r.Context(), r.PathValue("ref"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	task, err := u.svc.Get(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	data := u.base(r, task.Title)
	data.Status = r.URL.Query().Get("m")
	data.Task = task
	data.Done = task.Status == api.StatusDone
	data.PriorityClass = priorityClass(task.Priority)
	data.PriorityLabel = priorityLabel(task.Priority)
	if task.DueAt != nil {
		data.Due, data.Overdue = dueLabel(task, u.Now())
	}
	if task.ExternalURL != nil {
		data.ExternalURL = *task.ExternalURL
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

	data.Attachments = u.attachmentRows(r.Context(), task.ID)
	data.Children = u.childRows(r.Context(), task, u.Now())
	if task.SnoozeUntil != nil && *task.SnoozeUntil != "" {
		data.SnoozedUntil = query.LocalDate(*task.SnoozeUntil, u.Now().Location())
	}
	if task.SeriesID != nil && *task.SeriesID != "" {
		// Stored as RRULE, shown as English. FREQ=WEEKLY;INTERVAL=2 is a fact
		// about a standard, not an answer to "how often does this happen".
		data.RepeatRule = u.repeatRule(r.Context(), *task.SeriesID)
		data.Repeats = query.DescribeRecurrence(data.RepeatRule)
	}
	// Prefilled with what it already does, so the form shows the current rule
	// before changing it, the same way the TUI's E key does.
	data.RepeatValue = data.Repeats

	if task.Priority != nil {
		data.PriorityValue = itoa(int64(*task.Priority))
	}
	if task.DueAt != nil {
		data.DueValue = query.LocalDate(*task.DueAt, u.Now().Location())
	}
	if task.StartAt != nil {
		data.StartValue = query.LocalDate(*task.StartAt, u.Now().Location())
	}
	data.TagValue = strings.Join(task.Tags, " ")
	for _, mode := range []string{api.NotifyAuto, api.NotifyOn, api.NotifyOff} {
		data.NotifyChoices = append(data.NotifyChoices, notifyChoice{
			Value: mode, Label: mode, Selected: task.Notify == mode,
		})
	}

	u.render(w, "detail", data)
}

// StaleWaitingDays is when a waiting task starts reading as something to
// chase. Section 5 calls the derived view worth building on day one.
const StaleWaitingDays = 7

// person renders the screen you open before a 1:1.
func (u *UI) person(w http.ResponseWriter, r *http.Request) {
	people, ok := u.svc.(peopleService)
	if !ok {
		http.NotFound(w, r)
		return
	}
	found, err := people.ResolvePerson(r.Context(), r.PathValue("ref"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	page, err := people.PersonPage(r.Context(), found.ID, u.Now())
	if err != nil {
		u.fail(w, r, err)
		return
	}

	now := u.Now()
	data := u.base(r, found.Name)
	data.Page = page
	data.PersonQuery = "@" + found.Handle

	rows := func(tasks []api.Task) []Row {
		out := make([]Row, 0, len(tasks))
		for _, t := range tasks {
			out = append(out, prepareRow(t, false, false, now))
		}
		return out
	}
	data.Assigned = rows(page.Assigned)
	data.Owed = rows(page.Owed)
	data.Involved = rows(page.Involved)
	data.Agenda = rows(page.Agenda)
	data.GroupTasks = rows(page.GroupTasks)

	for i, t := range page.Waiting {
		days := 0
		if i < len(page.WaitingDays) {
			days = page.WaitingDays[i]
		}
		data.Waiting = append(data.Waiting, waitingRow{
			Row:   prepareRow(t, false, false, now),
			Age:   plural(days, "day"),
			Stale: days >= StaleWaitingDays,
		})
	}

	u.render(w, "person", data)
}

// peopleService is the part of the store the person page needs.
type peopleService interface {
	ResolvePerson(ctx context.Context, ref string) (api.Person, error)
	PersonPage(ctx context.Context, personID string, now time.Time) (api.PersonPage, error)
}

func plural(n int, word string) string {
	out := itoa(int64(n)) + " " + word
	if n != 1 {
		out += "s"
	}
	return out
}

func (u *UI) help(w http.ResponseWriter, r *http.Request) {
	data := u.base(r, "Keys")
	data.Keys = keymap()
	u.render(w, "help", data)
}

func (u *UI) settings(w http.ResponseWriter, r *http.Request) {
	data := u.base(r, "Settings")
	data.ThemeDir = u.ThemeDir
	data.Status = r.URL.Query().Get("e")

	for _, t := range u.assets.Themes {
		data.Themes = append(data.Themes, themeChoice{
			Name: t.Name, Label: t.Label, BuiltIn: t.BuiltIn,
			Selected: t.Name == data.Theme,
		})
	}

	tokens, err := u.svc.Tokens(r.Context())
	if err != nil {
		u.fail(w, r, err)
		return
	}
	for _, t := range tokens {
		row := tokenRow{
			ID: t.ID, Name: t.Name, Prefix: t.Prefix, Actor: t.Actor,
			Scopes: strings.Join(t.Scopes, ", "), LastUsed: "never",
			Revoked: t.RevokedAt != nil,
		}
		if t.LastUsedAt != nil {
			row.LastUsed = *t.LastUsedAt
		}
		data.Tokens = append(data.Tokens, row)
	}
	data.Grants = u.GrantRows(r.Context())
	data.Plugins = u.pluginSections(r.Context())

	u.render(w, "settings", data)
}

// --- actions -----------------------------------------------------------

func (u *UI) add(w http.ResponseWriter, r *http.Request) {
	line := strings.TrimSpace(r.FormValue("line"))
	if line == "" {
		u.after(w, r, "")
		return
	}

	now := u.Now()
	// Same tokens as the filter grammar, parsed on the way in. Anything the
	// parser does not recognize stays in the title.
	capture := query.ParseCapture(line, now)
	if capture.Title == "" {
		u.after(w, r, "that is all tags and no task")
		return
	}

	task, err := u.svc.Create(r.Context(), u.actor(r), api.TaskCreate{
		Title: capture.Title, Priority: capture.Priority,
		DueAt: capture.Due, StartAt: capture.Start,
		Tags: capture.Tags, People: capture.People,
	}, now)
	if err != nil {
		u.after(w, r, humanError(err))
		return
	}
	u.after(w, r, "added "+itoa(task.Num)+" in "+task.Status)
}

func (u *UI) complete(w http.ResponseWriter, r *http.Request) {
	id, ok := u.resolve(w, r)
	if !ok {
		return
	}
	task, err := u.svc.Get(r.Context(), id)
	if err != nil {
		u.after(w, r, humanError(err))
		return
	}

	// The checkbox is a toggle: a checked row reopens.
	if task.Status == api.StatusDone {
		u.after(w, r, u.reopen(r, id))
		return
	}

	res, err := u.svc.Complete(r.Context(), u.actor(r), id, u.Now())
	if err != nil {
		u.after(w, r, humanError(err))
		return
	}
	status := "done " + itoa(res.Task.Num)
	if res.ChildrenOpen > 0 {
		// The server never cascades. The parent is the commitment and the
		// children are steps.
		status += ", " + itoa(int64(res.ChildrenOpen)) + " subtask(s) still open"
	}
	u.after(w, r, status)
}

func (u *UI) reopen(r *http.Request, id string) string {
	// Reopening is a status change rather than its own verb, and the state
	// machine only allows done to todo.
	patcher, ok := u.svc.(taskPatcher)
	if !ok {
		return "reopening is not available"
	}
	todo := api.StatusTodo
	task, err := patcher.Patch(r.Context(), u.actor(r), id, api.TaskPatch{Status: &todo}, "", u.Now())
	if err != nil {
		return humanError(err)
	}
	return "reopened " + itoa(task.Num)
}

func (u *UI) drop(w http.ResponseWriter, r *http.Request) {
	id, ok := u.resolve(w, r)
	if !ok {
		return
	}
	task, err := u.svc.Drop(r.Context(), u.actor(r), id, u.Now())
	if err != nil {
		u.after(w, r, humanError(err))
		return
	}
	u.after(w, r, "dropped "+itoa(task.Num)+", u undoes it")
}

// edit applies the detail page's form.
//
// Every field is present in the form, so every field is in the patch: an
// empty priority means clear it rather than leave it, which is what the
// person looking at an empty box expects.
func (u *UI) edit(w http.ResponseWriter, r *http.Request) {
	id, ok := u.resolve(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		u.after(w, r, "could not read the form")
		return
	}

	patch := api.TaskPatch{Presence: map[string]bool{}}
	title := r.PostFormValue("title")
	notes := r.PostFormValue("notes")
	patch.Title, patch.Notes = &title, &notes

	patch.Presence["priority"] = true
	if raw := strings.TrimSpace(r.PostFormValue("priority")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 4 {
			u.redirectToTask(w, r, id, "priority is 1 to 4, or empty to clear")
			return
		}
		patch.Priority = &n
	}

	patch.Presence["due_at"] = true
	if raw := strings.TrimSpace(r.PostFormValue("due")); raw != "" {
		// Keywords work here for the same reason they work in the filter bar:
		// there is one date vocabulary.
		resolved, err := query.ResolveDate(raw, u.Now())
		if err != nil {
			u.redirectToTask(w, r, id, "could not read that date")
			return
		}
		patch.DueAt = &resolved
	}

	// Defer, not schedule. The start date hides a task until the day it can
	// actually begin, and it says nothing about when it is due or when to work
	// on it. Same date vocabulary as everything else.
	patch.Presence["start_at"] = true
	if raw := strings.TrimSpace(r.PostFormValue("start")); raw != "" {
		resolved, err := query.ResolveDate(raw, u.Now())
		if err != nil {
			u.redirectToTask(w, r, id, err.Error())
			return
		}
		patch.StartAt = &resolved
	}

	tags := []string{}
	for _, tag := range strings.Fields(r.PostFormValue("tags")) {
		if tag = strings.TrimPrefix(tag, "#"); tag != "" {
			tags = append(tags, strings.ToLower(tag))
		}
	}
	patch.Tags = &tags

	if mode := r.PostFormValue("notify"); mode != "" {
		patch.Presence["notify"] = true
		patch.Notify = &mode
	}

	patcher, ok := u.svc.(taskPatcher)
	if !ok {
		u.redirectToTask(w, r, id, "editing is not available")
		return
	}
	if _, err := patcher.Patch(r.Context(), u.actor(r), id, patch, "", u.Now()); err != nil {
		u.redirectToTask(w, r, id, humanError(err))
		return
	}
	u.redirectToTask(w, r, id, "")
}

// snooze hides a task for a while from the detail page.
// wake clears a snooze, putting the task back in the list now.
//
// Its own route rather than an empty snooze field, because an empty field is
// somebody who has not typed yet and must not silently mean "do something".
func (u *UI) wake(w http.ResponseWriter, r *http.Request) {
	id, ok := u.resolve(w, r)
	if !ok {
		return
	}
	patcher, ok := u.svc.(taskPatcher)
	if !ok {
		u.after(w, r, "waking is not available")
		return
	}
	empty := ""
	if _, err := patcher.Patch(r.Context(), u.actor(r), id, api.TaskPatch{
		SnoozeUntil: &empty,
		Presence:    map[string]bool{"snooze_until": true},
	}, "", u.Now()); err != nil {
		u.redirectToTask(w, r, id, humanError(err))
		return
	}
	u.redirectToTask(w, r, id, "awake")
}

// snoozeUntil reads what somebody typed into the snooze field.
//
// A duration and nothing else. Snooze is "not now" about a task that is ready:
// you keep skimming past it, or the reminder landed mid-meeting. Hiding it
// until a date is a different statement, that the work cannot begin yet, and
// that is what the start date on the task is for. A date typed here is
// answered with where it goes rather than quietly accepted, because the two
// look alike from the outside and behave nothing alike.
func (u *UI) snoozeUntil(raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, errors.New("say how long: 2h, 30m, 1h30m")
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		if _, dateErr := query.ResolveDate(raw, u.Now()); dateErr == nil {
			return time.Time{}, errors.New(
				"snooze takes how long, like 2h or 30m. To hide it until a date, " +
					"set the start date instead")
		}
		return time.Time{}, errors.New("say how long: 2h, 30m, 1h30m")
	}
	if d <= 0 {
		return time.Time{}, errors.New("snoozing needs a time in the future")
	}
	return u.Now().Add(d), nil
}

func (u *UI) snooze(w http.ResponseWriter, r *http.Request) {
	id, ok := u.resolve(w, r)
	if !ok {
		return
	}
	snoozer, ok := u.svc.(taskSnoozer)
	if !ok {
		u.after(w, r, "snoozing is not available")
		return
	}

	// One field, two kinds of answer. "2h" is how long, "friday" is until
	// when, and nothing reads as both: Go has no day unit, so 3d is never a
	// duration, and no date keyword parses as one either.
	raw := strings.TrimSpace(r.PostFormValue("duration"))
	if raw == "" {
		raw = strings.TrimSpace(r.PostFormValue("until"))
	}
	until, err := u.snoozeUntil(raw)
	if err != nil {
		u.redirectToTask(w, r, id, err.Error())
		return
	}
	if _, err := snoozer.Snooze(r.Context(), u.actor(r), id, until, u.Now()); err != nil {
		u.redirectToTask(w, r, id, humanError(err))
		return
	}
	u.after(w, r, "snoozed until "+query.LocalDate(until.Format(time.RFC3339), u.Now().Location()))
}

// redirectToTask sends the browser back to the detail page it came from,
// carrying a message when there is one.
func (u *UI) redirectToTask(w http.ResponseWriter, r *http.Request, id, message string) {
	task, err := u.svc.Get(r.Context(), id)
	if err != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	target := "/t/" + itoa(task.Num)
	if message != "" {
		target += "?m=" + urlEncode(message)
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// taskPatcher and taskSnoozer are the parts of the store the edit routes use.
// They are asserted rather than added to Service so a caller that only reads
// can still satisfy the interface.
type taskPatcher interface {
	Patch(ctx context.Context, actor, id string, p api.TaskPatch, ifMatch string, now time.Time) (api.Task, error)
}

type taskSnoozer interface {
	Snooze(ctx context.Context, actor, id string, until, now time.Time) (api.Task, error)
}

func (u *UI) undo(w http.ResponseWriter, r *http.Request) {
	res, err := u.svc.Undo(r.Context(), u.actor(r), u.Now())
	if err != nil {
		var apiErr *api.Error
		if errors.As(err, &apiErr) && apiErr.Code == api.ErrNothingToUndo {
			u.after(w, r, "nothing left to undo")
			return
		}
		u.after(w, r, humanError(err))
		return
	}
	u.after(w, r, "undid "+res.Kind)
}

func (u *UI) fold(w http.ResponseWriter, r *http.Request) {
	id, ok := u.resolve(w, r)
	if !ok {
		return
	}
	collapsed, err := u.svc.CollapsedTasks(r.Context())
	if err != nil {
		u.after(w, r, humanError(err))
		return
	}
	isCollapsed := false
	for _, c := range collapsed {
		if c == id {
			isCollapsed = true
			break
		}
	}
	if err := u.svc.SetCollapsed(r.Context(), id, !isCollapsed); err != nil {
		u.after(w, r, humanError(err))
		return
	}
	u.after(w, r, "")
}

func (u *UI) setTheme(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("theme")
	known := false
	for _, t := range u.assets.Themes {
		if t.Name == name {
			known = true
			break
		}
	}
	if !known {
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: ThemeCookie, Value: name, Path: "/",
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
		MaxAge: 365 * 24 * 60 * 60,
	})
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func (u *UI) revokeToken(w http.ResponseWriter, r *http.Request) {
	if err := u.svc.RevokeToken(r.Context(), r.PathValue("id"), u.Now()); err != nil {
		u.log.Error("revoke token", "err", err)
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

// --- plumbing ----------------------------------------------------------

func (u *UI) resolve(w http.ResponseWriter, r *http.Request) (string, bool) {
	id, err := u.svc.Resolve(r.Context(), r.PathValue("id"))
	if err != nil {
		u.after(w, r, "no task with that id")
		return "", false
	}
	return id, true
}

// after answers an action. htmx gets the refreshed list; a browser without
// JavaScript gets a redirect, so every action works with the script off.
func (u *UI) after(w http.ResponseWriter, r *http.Request, status string) {
	if r.Header.Get("HX-Request") == "true" {
		u.listFragment(w, r, status)
		return
	}
	target := "/"
	if filter := filterFromReferer(r); filter != "" {
		target = "/?q=" + urlEncode(filter)
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (u *UI) actor(_ *http.Request) string { return "me" }

func (u *UI) render(w http.ResponseWriter, page string, data pageData) {
	t, ok := u.tmpl[page]
	if !ok {
		http.Error(w, "no such page", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, "layout", data); err != nil {
		u.log.Error("render", "page", page, "err", err)
	}
}

func (u *UI) fail(w http.ResponseWriter, _ *http.Request, err error) {
	u.log.Error("web", "err", err)
	http.Error(w, "the server could not complete that", http.StatusInternalServerError)
}

// humanError turns a store error into something worth reading. Errors say
// what failed and what to do; they do not apologize.
func humanError(err error) string {
	var apiErr *api.Error
	if errors.As(err, &apiErr) {
		if apiErr.Message != "" {
			return apiErr.Message
		}
		return apiErr.Code
	}
	if errors.Is(err, store.ErrNotFound) {
		return "no task with that id"
	}
	return "that did not go through"
}

// Login renders the sign-in page. It is the only route that renders anything
// to an unauthenticated request.
func (u *UI) Login(w http.ResponseWriter, r *http.Request, message string) {
	data := u.base(r, "Sign in")
	data.Error = message
	data.Next = safeNext(r.URL.Query().Get("next"))

	if ok, err := u.svc.HasAccount(r.Context()); err == nil && !ok {
		data.NoAccount = true
	}
	u.render(w, "login", data)
}

// parseQuery and urlEncode keep net/url out of the template path, which
// otherwise tempts someone into building URLs by concatenation.
func parseQuery(raw string) (url.Values, error) { return url.ParseQuery(raw) }

func urlEncode(s string) string { return url.QueryEscape(s) }

// keymap is the help page's content. It is the TUI's keymap, key for key:
// section 11 says vim-flavored and identical in the web UI, and the point is
// that whichever client is in front of you takes the same keys.
func keymap() []keyRow {
	return []keyRow{
		{Keys: "j k", Help: "move"},
		{Keys: "g G", Help: "top, bottom"},
		{Keys: "enter", Help: "open the detail view"},
		{Keys: "space d", Help: "toggle done"},
		{Keys: "x", Help: "drop"},
		{Keys: "a", Help: "add a task"},
		{Keys: "z", Help: "fold the row under the cursor"},
		{Keys: "Z", Help: "fold every parent in view"},
		{Keys: "/", Help: "edit the filter"},
		{Keys: "1-9", Help: "saved filters"},
		{Keys: "u", Help: "undo"},
		{Keys: "r", Help: "reload"},
		{Keys: "?", Help: "this help"},
		{Keys: "esc", Help: "back"},
		{Keys: "e", Help: "edit", When: "not scheduled yet"},
		{Keys: "p", Help: "set priority", When: "not scheduled yet"},
		{Keys: "t", Help: "tags", When: "not scheduled yet"},
		{Keys: "s", Help: "snooze", When: "with reminders, in phase 5"},
		{Keys: "w", Help: "waiting on someone", When: "with people, in phase 6"},
		{Keys: "@", Help: "people", When: "with people, in phase 6"},
	}
}

// safeNext keeps the post-login redirect on this origin. An absolute URL here
// would be an open redirect on the login form.
func safeNext(raw string) string {
	if raw == "" || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "" || u.Host != "" {
		return ""
	}
	return u.String()
}
