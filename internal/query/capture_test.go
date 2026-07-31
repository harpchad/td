package query_test

import (
	"strings"
	"testing"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/query"
)

func TestParseCapture(t *testing.T) {
	now := seedClock(t)

	tests := []struct {
		name     string
		line     string
		title    string
		tags     []string
		people   []string
		priority int
		due      string
	}{
		{
			name:  "a plain thought stays a plain thought",
			line:  "call the dealer about the alignment",
			title: "call the dealer about the alignment",
		},
		{
			name:     "the filter tokens are read inline",
			line:     `renew wildcard cert #certs @stacey p:2 due:friday`,
			title:    "renew wildcard cert",
			tags:     []string{"certs"},
			people:   []string{"stacey"},
			priority: 2,
			due:      "2026-08-07",
		},
		{
			name:   "a role can be given with the handle",
			line:   "chase the quote @mikah:assignee",
			title:  "chase the quote",
			people: []string{"mikah:assignee"},
		},
		{
			name:  "anything the parser does not recognize stays in the title",
			line:  "look at foo:bar and OPS-1421",
			title: "look at foo:bar and OPS-1421",
		},
		{
			name:  "a p: value outside 1-4 is title text, not an error",
			line:  "score p:9 on the test",
			title: "score p:9 on the test",
		},
		{
			name:  "a date that does not resolve stays in the title",
			line:  "ship due:nextweek",
			title: "ship due:nextweek",
		},
		{
			name:     "tokens can appear before the title",
			line:     "#hr p:1 write the onboarding doc",
			title:    "write the onboarding doc",
			tags:     []string{"hr"},
			priority: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := query.ParseCapture(tc.line, now)
			if got.Title != tc.title {
				t.Errorf("title = %q, want %q", got.Title, tc.title)
			}
			if strings.Join(got.Tags, ",") != strings.Join(tc.tags, ",") {
				t.Errorf("tags = %v, want %v", got.Tags, tc.tags)
			}
			if strings.Join(got.People, ",") != strings.Join(tc.people, ",") {
				t.Errorf("people = %v, want %v", got.People, tc.people)
			}
			if tc.priority == 0 {
				if got.Priority != nil {
					t.Errorf("priority = %d, want unset", *got.Priority)
				}
			} else if got.Priority == nil || *got.Priority != tc.priority {
				t.Errorf("priority = %v, want %d", got.Priority, tc.priority)
			}
			if tc.due == "" {
				if got.Due != nil {
					t.Errorf("due = %s, want unset", *got.Due)
				}
			} else if got.Due == nil || *got.Due != tc.due {
				t.Errorf("due = %v, want %s", got.Due, tc.due)
			}
		})
	}
}

// TestArrangeLiftsSubtasksUnderTheirParent locks the rule that sort order and
// display order are different: in the home view fixture task 113 sorts
// seventh and displays fourth.
func TestArrangeLiftsSubtasksUnderTheirParent(t *testing.T) {
	parentID := "parent-101"
	mk := func(num int64, parent *string) api.Task {
		return api.Task{ID: "task-" + string(rune('a'+num)), Num: num, ParentID: parent}
	}
	p := mk(101, nil)
	p.ID = parentID
	child := mk(113, &parentID)

	sorted := []api.Task{mk(104, nil), mk(102, nil), p, mk(114, nil), mk(108, nil), mk(106, nil), child, mk(103, nil)}

	rows := query.Arrange(sorted)
	wantNums := []int64{104, 102, 101, 113, 114, 108, 106, 103}
	if len(rows) != len(wantNums) {
		t.Fatalf("got %d rows, want %d", len(rows), len(wantNums))
	}
	for i, want := range wantNums {
		if rows[i].Task.Num != want {
			t.Errorf("row %d = %d, want %d", i, rows[i].Task.Num, want)
		}
	}
	if rows[3].Depth != 1 {
		t.Errorf("113 depth = %d, want 1", rows[3].Depth)
	}
}

// TestArrangeKeepsAnOrphanedMatchInPlace covers the other half: a filter that
// matches a child directly still shows it, at the top level, because its
// parent is not in the result set.
func TestArrangeKeepsAnOrphanedMatchInPlace(t *testing.T) {
	absent := "not-in-the-set"
	child := api.Task{ID: "c", Num: 113, ParentID: &absent}
	rows := query.Arrange([]api.Task{child})
	if len(rows) != 1 || rows[0].Depth != 0 {
		t.Errorf("rows = %+v, want one row at depth 0", rows)
	}
}
