// Package planner mirrors Microsoft Planner into td.
//
// One way. Section 8 fixes v1 as a read-only mirror: every mirrored task
// carries its external URL and one keystroke opens the real thing, which
// removes the callback path, the action queue, and the whole class of
// failures where a remote write half-succeeds and the two systems disagree
// about what happened.
//
// Planner over Jira because the Graph API is cleaner. "Cleaner" has one large
// exception, handled below: Planner has no delta query for tasks, so every run
// reads the whole plan and idempotence rests entirely on the ETag.
package planner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/sync"
)

// Source is the value written to task.source and the suffix of the scope a
// plugin token needs: sync:planner.
const Source = "planner"

// BatchSize is how many items go in one POST. The API caps a request body at
// a megabyte, and a plan large enough to exceed that should arrive in pieces
// rather than as one failed request.
const BatchSize = 200

// GraphTask is a plannerTask as Graph returns it.
//
// Only the fields the mirror reads are declared. Planner sends a lot more,
// and unmarshalling into a narrow struct is what keeps a new upstream field
// from becoming a new column here by accident.
type GraphTask struct {
	ETag              string `json:"@odata.etag"`
	ID                string `json:"id"`
	PlanID            string `json:"planId"`
	Title             string `json:"title"`
	DueDateTime       string `json:"dueDateTime"`
	StartDateTime     string `json:"startDateTime"`
	CompletedDateTime string `json:"completedDateTime"`
	// PercentComplete is Planner's whole notion of status: 0 is not started,
	// 1 to 99 is in progress, 100 is complete. There are no other states, so
	// waiting has no upstream equivalent and is never set by a sync.
	PercentComplete int `json:"percentComplete"`
	// Priority is Planner's 0 to 10, and is deliberately not mapped onto td's
	// priority. See the comment on Translate.
	Priority  int              `json:"priority"`
	CreatedBy GraphIdentitySet `json:"createdBy"`
	// Assignments is keyed by the assignee's user id, which is the only place
	// the assignee appears: there is no assignees array.
	Assignments map[string]GraphAssignment `json:"assignments"`
}

// GraphAssignment is one plannerAssignment.
type GraphAssignment struct {
	AssignedBy       GraphIdentitySet `json:"assignedBy"`
	AssignedDateTime string           `json:"assignedDateTime"`
}

// GraphIdentitySet is Graph's identitySet.
type GraphIdentitySet struct {
	User GraphIdentity `json:"user"`
}

// GraphIdentity is one identity. DisplayName is routinely null on Planner
// responses, which is why the plugin fetches users separately.
type GraphIdentity struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
}

// GraphTaskList is the collection wrapper.
type GraphTaskList struct {
	Value    []GraphTask `json:"value"`
	NextLink string      `json:"@odata.nextLink"`
}

// GraphPlan is a plannerPlan.
type GraphPlan struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// GraphUser is enough of a user to name a person.
type GraphUser struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	// Mail is the address, which is what lets the server attach this identity
	// to somebody td already knows. UserPrincipalName stands in when Mail is
	// empty, which happens for accounts with no mailbox.
	Mail              string `json:"mail"`
	UserPrincipalName string `json:"userPrincipalName"`
}

// GraphUserList is the collection wrapper.
type GraphUserList struct {
	Value []GraphUser `json:"value"`
}

// Config is the [planner] block of the client's config.toml.
type Config struct {
	// PlanIDs are the plans to mirror. Empty mirrors nothing, which is the
	// default: a plugin that guessed which plans you meant would import
	// somebody else's board.
	PlanIDs []string `toml:"plans"`
	// GraphToken is a Microsoft Graph access token with Tasks.Read and
	// User.ReadBasic.All. Obtaining it is out of scope: paste one, or point
	// GraphTokenCommand at something that prints one.
	GraphToken string `toml:"graph_token"`
	// GraphTokenCommand is run to obtain a token when GraphToken is empty,
	// so a short-lived token does not have to live in a file.
	GraphTokenCommand string `toml:"graph_token_command"`
	// TaskURLTemplate builds the deep link a mirrored task carries. %s is the
	// task id. Planner's web address has moved once already, so this is
	// configuration rather than a constant.
	TaskURLTemplate string `toml:"task_url_template"`
	// Endpoint is the Graph base URL, overridable for a sovereign cloud and
	// for tests.
	Endpoint string `toml:"endpoint"`

	// AssignedTo keeps only the tasks assigned to one directory object id.
	// Empty mirrors the whole plan.
	//
	// A Planner plan is a team board. Mirroring all of it puts everybody
	// else's work in a list whose entire job is answering "what should I do
	// next", so the default is the assignments and the whole board is the
	// thing you opt into.
	AssignedTo string `toml:"-"`

	// Relink drops the revision from every item so the server re-applies the
	// whole plan instead of skipping what has not moved.
	//
	// It exists because idempotence has one cost: an item whose rev matches is
	// not looked at, so a person link that could now be resolved is not
	// resolved until something upstream happens to change. After mapping an
	// identity you want the backfill now, not whenever somebody edits the
	// card. Not a TOML field: it is a decision about one run.
	Relink bool `toml:"-"`
}

// DefaultTaskURL is the documented Planner deep link.
const DefaultTaskURL = "https://tasks.office.com/Home/Task/%s"

// DefaultEndpoint is Graph v1.0.
const DefaultEndpoint = "https://graph.microsoft.com/v1.0"

// Enabled reports whether there is anything to mirror.
func (c Config) Enabled() bool { return len(c.PlanIDs) > 0 }

// Client reads Planner over Graph.
type Client struct {
	Config Config
	HTTP   *http.Client
}

// New builds a client.
func New(cfg Config) *Client {
	if cfg.Endpoint == "" {
		cfg.Endpoint = DefaultEndpoint
	}
	if cfg.TaskURLTemplate == "" {
		cfg.TaskURLTemplate = DefaultTaskURL
	}
	return &Client{Config: cfg, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// Tasks reads every task in a plan, following pagination.
func (c *Client) Tasks(ctx context.Context, planID string) ([]GraphTask, error) {
	var out []GraphTask
	next := c.Config.Endpoint + "/planner/plans/" + url.PathEscape(planID) + "/tasks"

	// Bounded, because a nextLink loop from a confused proxy would otherwise
	// spin forever against somebody's tenant.
	for page := 0; next != "" && page < 100; page++ {
		var body GraphTaskList
		if err := c.get(ctx, next, &body); err != nil {
			return nil, err
		}
		out = append(out, body.Value...)
		next = body.NextLink
	}
	return out, nil
}

// Plan reads a plan's metadata.
func (c *Client) Plan(ctx context.Context, planID string) (GraphPlan, error) {
	var out GraphPlan
	err := c.get(ctx, c.Config.Endpoint+"/planner/plans/"+url.PathEscape(planID), &out)
	return out, err
}

// Users reads the directory entries for a set of ids.
//
// Planner leaves displayName null on the identities embedded in a task, so
// the names have to come from somewhere. They are what lets the server create
// a person the first time an unknown identity appears rather than dropping
// the link.
func (c *Client) Users(ctx context.Context, ids []string) (map[string]GraphUser, error) {
	out := map[string]GraphUser{}
	for _, id := range ids {
		var user GraphUser
		if err := c.get(ctx, c.Config.Endpoint+"/users/"+url.PathEscape(id), &user); err != nil {
			// A user who has left the tenant is a 404, and losing the whole
			// sync over one departed colleague would be the wrong trade. The
			// link is skipped and the task still arrives.
			continue
		}
		out[user.ID] = user
	}
	return out, nil
}

func (c *Client) get(ctx context.Context, endpoint string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if c.Config.GraphToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.Config.GraphToken)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("graph answered %s for %s: %s",
			resp.Status, endpoint, strings.TrimSpace(string(detail)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Translate turns Graph tasks into the plugin contract's items.
//
// What is deliberately not translated:
//
// Planner's priority is 0 to 10 and is set by whoever created the card. td's
// priority is your answer to "what should I do next", which is a different
// question with a different answer, and section 8 puts priority firmly in the
// locally-owned column. Mapping one onto the other would overwrite your
// answer with somebody else's every fifteen minutes.
//
// Planner's description is not read either. It is the field most likely to
// contain text written by an external reporter, td's notes are yours, and the
// two would fight over the same column.
func Translate(tasks []GraphTask, users map[string]GraphUser, urlTemplate string, loc *time.Location, relink bool) []sync.Item {
	out := make([]sync.Item, 0, len(tasks))

	for _, t := range tasks {
		item := sync.Item{
			ExternalID: t.ID,
			Title:      strings.TrimSpace(t.Title),
			Status:     statusOf(t),
			Rev:        strings.TrimSpace(t.ETag),
		}
		if relink {
			// No rev means the server cannot tell this from a change, so it
			// re-applies the item and resolves the people again.
			item.Rev = ""
		}
		if item.Title == "" {
			item.Title = "(untitled Planner task)"
		}
		if urlTemplate != "" {
			item.URL = fmt.Sprintf(urlTemplate, t.ID)
		}
		if due := localDate(t.DueDateTime, loc); due != "" {
			item.DueAt = &due
		}

		// Assignments is a map, so iterating it is unordered. The items are
		// sorted so two runs over an unchanged plan produce byte-identical
		// payloads, which is what makes a replay provably a no-op.
		assignees := make([]string, 0, len(t.Assignments))
		for id := range t.Assignments {
			assignees = append(assignees, id)
		}
		sort.Strings(assignees)

		for _, id := range assignees {
			item.People = append(item.People, sync.ItemPerson{
				Role: api.RoleAssignee, SourceUser: id,
				Name: nameOf(users, id), Email: emailOf(users, id),
			})
		}
		// Whoever put it on your board is the assigner, and that is the
		// person you go back to when it turns out to be the wrong thing.
		if by := t.CreatedBy.User.ID; by != "" && !containsAssignee(assignees, by) {
			item.People = append(item.People, sync.ItemPerson{
				Role: api.RoleAssigner, SourceUser: by,
				Name: nameOf(users, by), Email: emailOf(users, by),
			})
		}

		out = append(out, item)
	}
	return out
}

// statusOf maps percentComplete onto td's vocabulary.
//
// Planner has three states and td has six. The two td states with no upstream
// equivalent, waiting and dropped, are never produced here: a mirror must not
// invent a state the source cannot express, and both of those are things you
// say about your own work rather than things Planner knows.
func statusOf(t GraphTask) string {
	switch {
	case t.PercentComplete >= 100:
		return api.StatusDone
	case t.PercentComplete > 0:
		return api.StatusDoing
	default:
		return api.StatusTodo
	}
}

// localDate turns Graph's UTC instant into the calendar date Planner meant.
//
// Planner stores a due date as midnight UTC on the day the user picked. Read
// as an instant in a timezone west of UTC it becomes the previous evening,
// and a task due Friday shows up as due Thursday. The date is what was meant,
// so the date is what is kept.
func localDate(value string, loc *time.Location) string {
	value = strings.TrimSpace(value)
	if value == "" || value == "null" {
		return ""
	}
	at, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return ""
	}
	if at.Hour() == 0 && at.Minute() == 0 && at.Second() == 0 {
		return at.Format("2006-01-02")
	}
	if loc == nil {
		loc = time.UTC
	}
	return at.In(loc).Format(time.RFC3339)
}

func nameOf(users map[string]GraphUser, id string) string {
	if user, ok := users[id]; ok {
		if user.DisplayName != "" {
			return user.DisplayName
		}
		return user.UserPrincipalName
	}
	return ""
}

// emailOf is the address to match a person on. Mail first, then the
// principal name, which is an address in every tenant that has not gone out
// of its way.
func emailOf(users map[string]GraphUser, id string) string {
	user, ok := users[id]
	if !ok {
		return ""
	}
	if user.Mail != "" {
		return user.Mail
	}
	if strings.Contains(user.UserPrincipalName, "@") {
		return user.UserPrincipalName
	}
	return ""
}

func containsAssignee(assignees []string, id string) bool {
	for _, a := range assignees {
		if a == id {
			return true
		}
	}
	return false
}

// UserIDs collects every identity a batch refers to, so they can be looked up
// in one pass rather than one request per task.
func UserIDs(tasks []GraphTask) []string {
	seen := map[string]bool{}
	var out []string
	add := func(id string) {
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		out = append(out, id)
	}
	for _, t := range tasks {
		add(t.CreatedBy.User.ID)
		for id := range t.Assignments {
			add(id)
		}
	}
	sort.Strings(out)
	return out
}

// Gone works out what disappeared.
//
// Planner has no delta query for tasks and no tombstones, so "gone" is
// whatever td has for this source that the plan no longer contains. That
// makes the read authoritative, which in turn makes it dangerous: a partial
// read would mark everything it did not see as gone. The caller must pass the
// complete set, and Reconcile below refuses to compute this from an empty
// one.
func Gone(mirrored []string, present []GraphTask) []string {
	here := make(map[string]bool, len(present))
	for _, t := range present {
		here[t.ID] = true
	}
	var out []string
	for _, id := range mirrored {
		if !here[id] {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// AssignedTo keeps the tasks assigned to one person.
//
// Planner keys assignments by directory object id and stores nothing else
// about them, so this is an exact match on a map key rather than anything
// fuzzier. A task with no assignees at all is not yours: an unassigned card on
// a shared board is a card nobody has picked up.
func AssignedTo(tasks []GraphTask, userID string) []GraphTask {
	if userID == "" {
		return tasks
	}
	out := make([]GraphTask, 0, len(tasks))
	for _, t := range tasks {
		if _, ok := t.Assignments[userID]; ok {
			out = append(out, t)
		}
	}
	return out
}
