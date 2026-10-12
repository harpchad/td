package server_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/harpchad/td/internal/api"
)

// The seed has one parent and one child: 113 sits under 101. These cover
// moving a task between parents in place, which used to take a drop and a
// recreate that lost the history and the number.

func getTask(t *testing.T, ts *harness, ref string) api.Task {
	t.Helper()
	resp, body := do(t, ts, http.MethodGet, "/api/v1/tasks/"+ref, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %d %s", ref, resp.StatusCode, body)
	}
	var task api.Task
	decodeInto(t, body, &task)
	return task
}

func patchTask(t *testing.T, ts *harness, ref string, body any) (int, []byte) {
	t.Helper()
	resp, out := do(t, ts, http.MethodPatch, "/api/v1/tasks/"+ref, body)
	return resp.StatusCode, out
}

func wantError(t *testing.T, status int, body []byte, wantStatus int, wantCode string) {
	t.Helper()
	if status != wantStatus {
		t.Fatalf("status = %d, want %d (%s)", status, wantStatus, body)
	}
	var e api.Error
	decodeInto(t, body, &e)
	if e.Code != wantCode {
		t.Errorf("error = %q, want %q (%s)", e.Code, wantCode, e.Message)
	}
}

func TestPatchSetsAParent(t *testing.T) {
	ts := newServer(t)
	parent := getTask(t, ts, "102")
	before := getTask(t, ts, "103")

	status, body := patchTask(t, ts, "103", map[string]any{"parent_id": parent.ID})
	if status != http.StatusOK {
		t.Fatalf("status = %d: %s", status, body)
	}
	var moved api.Task
	decodeInto(t, body, &moved)
	if moved.ParentID == nil || *moved.ParentID != parent.ID {
		t.Fatalf("parent_id = %v, want %s", moved.ParentID, parent.ID)
	}
	// Moved in place, not recreated: the id and the number survive.
	if moved.ID != before.ID || moved.Num != before.Num {
		t.Errorf("moved to %s/%d, want %s/%d kept", moved.ID, moved.Num, before.ID, before.Num)
	}
	// Tags copy only at creation. A move does not rewrite them.
	if strings.Join(moved.Tags, ",") != strings.Join(before.Tags, ",") {
		t.Errorf("tags = %v, a move must leave them as %v", moved.Tags, before.Tags)
	}
	if got := getTask(t, ts, "102").ChildrenTotal; got != 1 {
		t.Errorf("102 children_total = %d, want 1", got)
	}
}

// TestPatchMovesAChildAndBothParentsCountIt covers the counts the parent rows
// draw as 2/5: the old parent loses the child and the new one gains it.
func TestPatchMovesAChildAndBothParentsCountIt(t *testing.T) {
	ts := newServer(t)
	oldParent := getTask(t, ts, "101")
	if oldParent.ChildrenTotal != 1 {
		t.Fatalf("seed: 101 children_total = %d, want 1", oldParent.ChildrenTotal)
	}
	// Finish the child first so children_done has something to move too.
	if resp, body := do(t, ts, http.MethodPost, "/api/v1/tasks/113/complete", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("complete 113: %d %s", resp.StatusCode, body)
	}
	if got := getTask(t, ts, "101"); got.ChildrenDone != 1 {
		t.Fatalf("101 children_done = %d, want 1", got.ChildrenDone)
	}

	newParent := getTask(t, ts, "104")
	if status, body := patchTask(t, ts, "113", map[string]any{"parent_id": newParent.ID}); status != http.StatusOK {
		t.Fatalf("status = %d: %s", status, body)
	}

	from := getTask(t, ts, "101")
	if from.ChildrenTotal != 0 || from.ChildrenDone != 0 {
		t.Errorf("old parent counts %d/%d, want 0/0", from.ChildrenDone, from.ChildrenTotal)
	}
	to := getTask(t, ts, "104")
	if to.ChildrenTotal != 1 || to.ChildrenDone != 1 {
		t.Errorf("new parent counts %d/%d, want 1/1", to.ChildrenDone, to.ChildrenTotal)
	}
}

func TestPatchNullLiftsATaskToTheTop(t *testing.T) {
	ts := newServer(t)

	status, body := patchTask(t, ts, "113", map[string]any{"parent_id": nil})
	if status != http.StatusOK {
		t.Fatalf("status = %d: %s", status, body)
	}
	var lifted api.Task
	decodeInto(t, body, &lifted)
	if lifted.ParentID != nil {
		t.Errorf("parent_id = %s, an explicit null must clear it", *lifted.ParentID)
	}
	if got := getTask(t, ts, "101").ChildrenTotal; got != 0 {
		t.Errorf("101 children_total = %d, want 0", got)
	}
}

func TestPatchWithoutParentIDLeavesTheParentAlone(t *testing.T) {
	ts := newServer(t)
	parent := getTask(t, ts, "101")

	status, body := patchTask(t, ts, "113", map[string]any{"title": "Draft the runbook"})
	if status != http.StatusOK {
		t.Fatalf("status = %d: %s", status, body)
	}
	var kept api.Task
	decodeInto(t, body, &kept)
	if kept.ParentID == nil || *kept.ParentID != parent.ID {
		t.Errorf("parent_id = %v, an absent key must leave it as %s", kept.ParentID, parent.ID)
	}
}

func TestPatchRefusesASelfParent(t *testing.T) {
	ts := newServer(t)
	self := getTask(t, ts, "103")

	status, body := patchTask(t, ts, "103", map[string]any{"parent_id": self.ID})
	wantError(t, status, body, http.StatusUnprocessableEntity, api.ErrParentCycle)
	if getTask(t, ts, "103").ParentID != nil {
		t.Error("a refused move must change nothing")
	}
}

// TestPatchRefusesACycle moves 101 under its own child, 113.
func TestPatchRefusesACycle(t *testing.T) {
	ts := newServer(t)
	child := getTask(t, ts, "113")

	status, body := patchTask(t, ts, "101", map[string]any{"parent_id": child.ID})
	wantError(t, status, body, http.StatusUnprocessableEntity, api.ErrParentCycle)
	if getTask(t, ts, "101").ParentID != nil {
		t.Error("a refused move must change nothing")
	}
}

// TestPatchKeepsSubtasksOneLevelDeep covers both ways a move could make a
// second level: under a parent that is itself a subtask, and carrying a task
// that already has subtasks of its own.
func TestPatchKeepsSubtasksOneLevelDeep(t *testing.T) {
	ts := newServer(t)
	sub := getTask(t, ts, "113")
	status, body := patchTask(t, ts, "103", map[string]any{"parent_id": sub.ID})
	wantError(t, status, body, http.StatusUnprocessableEntity, api.ErrNestingTooDeep)

	other := getTask(t, ts, "103")
	status, body = patchTask(t, ts, "101", map[string]any{"parent_id": other.ID})
	wantError(t, status, body, http.StatusUnprocessableEntity, api.ErrNestingTooDeep)
}

func TestPatchRefusesAParentThatDoesNotExist(t *testing.T) {
	ts := newServer(t)
	status, body := patchTask(t, ts, "103", map[string]any{"parent_id": "01J0000000000000000000GONE"})
	wantError(t, status, body, http.StatusNotFound, api.ErrNotFound)
}

// TestPatchAllowsAFinishedParent is the documented choice: is:orphan exists to
// find subtasks under a done or dropped parent, so that shape is legitimate.
func TestPatchAllowsAFinishedParent(t *testing.T) {
	ts := newServer(t)
	for _, ref := range []string{"111", "112"} { // done, dropped
		parent := getTask(t, ts, ref)
		status, body := patchTask(t, ts, "103", map[string]any{"parent_id": parent.ID})
		if status != http.StatusOK {
			t.Fatalf("under %s (%s): status = %d: %s", ref, parent.Status, status, body)
		}
	}
}

// TestUndoPutsAMovedTaskBack is the history the drop-and-recreate workaround
// threw away: the move is an event, so it reverses.
func TestUndoPutsAMovedTaskBack(t *testing.T) {
	ts := newServer(t)
	oldParent := getTask(t, ts, "101")
	newParent := getTask(t, ts, "104")
	if status, body := patchTask(t, ts, "113", map[string]any{"parent_id": newParent.ID}); status != http.StatusOK {
		t.Fatalf("status = %d: %s", status, body)
	}
	if resp, body := do(t, ts, http.MethodPost, "/api/v1/undo", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("undo: %d %s", resp.StatusCode, body)
	}
	back := getTask(t, ts, "113")
	if back.ParentID == nil || *back.ParentID != oldParent.ID {
		t.Errorf("parent_id = %v after undo, want %s", back.ParentID, oldParent.ID)
	}
}

// TestPatchRefusesUnknownKeys matches POST: a key the patch does not know is a
// 400, not a 200 that changed nothing. The three spellings are the ones a
// caller actually tried.
func TestPatchRefusesUnknownKeys(t *testing.T) {
	ts := newServer(t)
	parent := getTask(t, ts, "102")
	before := getTask(t, ts, "103")

	for _, key := range []string{"parent", "parentId", "due_is_date"} {
		t.Run(key, func(t *testing.T) {
			status, body := patchTask(t, ts, "103", map[string]any{key: parent.ID})
			wantError(t, status, body, http.StatusBadRequest, api.ErrBadRequest)
			var e api.Error
			decodeInto(t, body, &e)
			if !strings.Contains(e.Message, key) {
				t.Errorf("message %q does not name the key %q", e.Message, key)
			}
		})
	}

	// A known key beside an unknown one is refused whole, not half-applied.
	status, body := patchTask(t, ts, "103", map[string]any{"title": "renamed", "colour": "red"})
	wantError(t, status, body, http.StatusBadRequest, api.ErrBadRequest)
	if got := getTask(t, ts, "103"); got.Title != before.Title || got.UpdatedAt != before.UpdatedAt {
		t.Errorf("a refused patch changed the task: %q at %s", got.Title, got.UpdatedAt)
	}
}

// TestTaskPatchDecodesParentIDPresence checks the decoder directly, since the
// client and the MCP tool build patches without going through HTTP.
func TestTaskPatchDecodesParentIDPresence(t *testing.T) {
	cases := map[string]struct {
		present bool
		null    bool
	}{
		`{}`:                 {present: false},
		`{"parent_id":null}`: {present: true, null: true},
		`{"parent_id":"X"}`:  {present: true},
	}
	for in, want := range cases {
		var p api.TaskPatch
		if err := json.Unmarshal([]byte(in), &p); err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if p.Presence["parent_id"] != want.present {
			t.Errorf("%s: presence = %v, want %v", in, p.Presence["parent_id"], want.present)
		}
		if want.present && (p.ParentID == nil) != want.null {
			t.Errorf("%s: parent_id = %v, want null=%v", in, p.ParentID, want.null)
		}
	}
}
