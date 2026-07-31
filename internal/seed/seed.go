// Package seed reads testdata/seed.json, the fixture dataset every case in
// testdata/ evaluates against. It is the one place that knows the file's
// shape, so `make seed` and the store tests load identical data.
package seed

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// Data is the whole fixture file.
type Data struct {
	Now      string   `json:"now"`
	Timezone string   `json:"timezone"`
	People   []Person `json:"people"`
	Groups   []Group  `json:"groups"`
	Tasks    []Task   `json:"tasks"`
}

// Person is one seeded person. Key doubles as the @handle the filter grammar
// matches on.
type Person struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

// Group is one seeded group and its static membership.
type Group struct {
	Key     string   `json:"key"`
	Members []string `json:"members"`
}

// Task is one seeded task. People maps a role to the person keys holding it.
type Task struct {
	Num          int64               `json:"num"`
	Title        string              `json:"title"`
	Status       string              `json:"status"`
	Priority     *int                `json:"priority"`
	DueAt        *string             `json:"due_at"`
	Tags         []string            `json:"tags"`
	People       map[string][]string `json:"people"`
	Source       string              `json:"source"`
	ParentNum    *int64              `json:"parent_num"`
	StartAt      *string             `json:"start_at"`
	SnoozeUntil  *string             `json:"snooze_until"`
	WaitingOn    *string             `json:"waiting_on"`
	WaitingSince *string             `json:"waiting_since"`
	Notify       string              `json:"notify"`
	Groups       []string            `json:"groups"`
	Attachments  int                 `json:"attachments"`
	Notes        string              `json:"notes"`
	CompletedAt  *string             `json:"completed_at"`
	ExternalID   *string             `json:"external_id"`
	CreatedAt    string              `json:"created_at"`
}

// Load reads and parses a seed file.
func Load(path string) (*Data, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var d Data
	if err := json.Unmarshal(body, &d); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &d, nil
}

// Clock returns the fixed instant the fixture pins, in the fixture's own
// timezone. Every case in testdata/ evaluates against it, so a test that uses
// time.Now() instead will pass today and fail tomorrow.
func (d *Data) Clock() (time.Time, *time.Location, error) {
	loc, err := time.LoadLocation(d.Timezone)
	if err != nil {
		return time.Time{}, nil, fmt.Errorf("seed timezone %q: %w", d.Timezone, err)
	}
	now, err := time.Parse(time.RFC3339, d.Now)
	if err != nil {
		return time.Time{}, nil, fmt.Errorf("seed now %q: %w", d.Now, err)
	}
	return now.In(loc), loc, nil
}
