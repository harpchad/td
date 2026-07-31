// Package sync applies the plugin contract from section 8: a plugin posts
// what it read upstream, and the server decides what that is allowed to
// change.
//
// Field ownership is the whole of it. "My priority got wiped by a sync" kills
// trust in one incident and no amount of later care gets it back, so the
// division is written down here as data rather than spread through an upsert
// function where it can drift a field at a time.
package sync

import "github.com/harpchad/td/internal/api"

// Upstream fields are owned by the source system. A sync overwrites them
// every time, without asking and without merging: the remote copy is the
// truth about what the ticket says.
//
// Note what is not here. Due date is upstream, because a Jira due date is a
// commitment somebody else made and the mirror should show it. Status is
// upstream for the same reason, with one exception handled in Apply: a task
// you completed locally stays completed, because completing something in td
// is a statement about your own work.
var Upstream = []string{
	"title",
	"status",
	"due_at",
	"external_url",
	"external_rev",
}

// Local fields are yours. A sync never touches them, whatever the upstream
// item says, including when the upstream item has a field of the same name.
//
// Priority is the important one and the reason this list exists. Jira
// priorities are set by whoever filed the ticket and mean nothing about what
// you should do next; td's priority is your answer to that question, and a
// sync that overwrote it would make the whole list untrustworthy.
var Local = []string{
	"priority",
	"notes",
	"tags",
	"people",
	"snooze_until",
	"start_at",
	"notify",
	"remind_before",
	"effort",
	"parent_id",
	"series_id",
}

// Item is one upstream record as a plugin reports it.
//
// Every field is a pointer or a slice so "absent" and "empty" stay different.
// A plugin that cannot see a field must omit it rather than send a zero, or
// the first sync from a partial reader silently blanks the mirror.
type Item struct {
	ExternalID string `json:"external_id"`
	Title      string `json:"title"`
	// Status is the upstream status already mapped onto td's vocabulary. The
	// mapping lives in the plugin, because only the plugin knows what its
	// system's states mean.
	Status string  `json:"status,omitempty"`
	URL    string  `json:"url,omitempty"`
	Rev    string  `json:"rev,omitempty"`
	DueAt  *string `json:"due_at,omitempty"`

	// People carry the source's own user identifier, not a name. Resolving it
	// onto a person is the server's job through person_identity, which is
	// what makes "everything involving Brandiss" span three systems.
	People []ItemPerson `json:"people,omitempty"`
}

// ItemPerson is one upstream person link.
type ItemPerson struct {
	Role string `json:"role"`
	// SourceUser is the identifier in the source system: a Jira account id, a
	// Graph object id. It is not an email and not a display name, both of
	// which change.
	SourceUser string `json:"source_user"`
	// Name is what to call them if td has never seen this identity before. A
	// plugin that supplies it lets the server create the person rather than
	// dropping the link.
	Name string `json:"name,omitempty"`
}

// Request is the body of POST /api/v1/sync/{source}.
type Request struct {
	// Cursor is where the plugin resumed from. The server echoes a new one
	// back; what it means is entirely the plugin's business.
	Cursor string `json:"cursor,omitempty"`
	Items  []Item `json:"items"`
	// Gone is what disappeared upstream. Those are marked rather than
	// deleted: a ticket you can no longer see is not a ticket that never
	// existed, and something in your notes probably refers to it.
	Gone []string `json:"gone,omitempty"`
}

// Result is what the server answers with.
type Result struct {
	Cursor  string `json:"cursor"`
	Created int    `json:"created"`
	Updated int    `json:"updated"`
	Gone    int    `json:"gone"`
	// Unchanged counts items whose rev matched what was already stored. A
	// plugin replaying its whole window should see mostly this, which is how
	// idempotence is visible rather than merely claimed.
	Unchanged int `json:"unchanged"`
}

// LocalStatus reports whether a status is one the mirror must not overwrite.
//
// Completing a mirrored task in td is a statement about your own work: you
// have done your part, whatever the ticket still says. A sync that reopened
// it every fifteen minutes would make the mirror an argument rather than a
// list. Dropping is the same statement in the other direction.
func LocalStatus(status string) bool {
	return status == api.StatusDone || status == api.StatusDropped
}
