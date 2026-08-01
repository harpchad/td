package store_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/recur"
	"github.com/harpchad/td/internal/seed"
	"github.com/harpchad/td/internal/store"
)

// "A task system you cannot get your data out of is a hostage situation."
// Getting it out is only half of that: the round trip is what makes the file
// a backup rather than a report, and it is a v1 done criterion.

// fresh opens an empty store in the same timezone as the fixture.
func fresh(t *testing.T) *store.Store {
	t.Helper()
	d, err := seed.Load(filepath.Join("..", "..", "testdata", "seed.json"))
	if err != nil {
		t.Fatal(err)
	}
	_, loc, err := d.Clock()
	if err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(":memory:", store.Options{Location: loc})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestExportRoundTripsWithNoLoss is the assertion the done criterion names.
// The database is exercised first so the export has something of everything
// in it, and then two exports either side of a restore are compared byte for
// byte.
func TestExportRoundTripsWithNoLoss(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()

	exerciseEverything(t, s, now)

	first, err := s.Export(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Tasks) == 0 || len(first.Events) == 0 || len(first.People) == 0 {
		t.Fatalf("the export is thin: %d tasks, %d events, %d people",
			len(first.Tasks), len(first.Events), len(first.People))
	}

	// Through JSON, because that is what actually gets written to a file.
	body, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	var decoded store.Export
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}

	restored := fresh(t)
	if err := restored.Import(ctx, decoded); err != nil {
		t.Fatalf("import: %v", err)
	}

	second, err := restored.Export(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	again, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}

	if string(again) != string(body) {
		t.Errorf("the round trip lost or changed something:\n%s", firstDifference(string(body), string(again)))
	}
}

// exerciseEverything puts one of each kind of thing into the database, so the
// round trip has something to lose.
func exerciseEverything(t *testing.T, s *store.Store, now time.Time) {
	t.Helper()
	ctx := context.Background()

	task, err := s.GetByNum(ctx, 103)
	if err != nil {
		t.Fatal(err)
	}

	// Every locally-owned field, so a dropped column shows up.
	p2, effort := 2, 5
	notes := "Discount tire quoted 780 for the set.\n\nSecond paragraph."
	snooze := "2026-08-05T09:00:00-05:00"
	if _, err := s.Patch(ctx, actor, task.ID, api.TaskPatch{
		Priority: &p2, Effort: &effort, Notes: &notes, SnoozeUntil: &snooze,
		Tags:     &[]string{"truck", "garage"},
		Presence: map[string]bool{"priority": true, "effort": true, "snooze_until": true},
	}, "", now); err != nil {
		t.Fatal(err)
	}

	// A subtask, so parent ordering on import is exercised.
	if _, err := s.Create(ctx, actor, api.TaskCreate{
		Title: "Call the dealer", ParentID: &task.ID, Priority: &p2,
	}, now); err != nil {
		t.Fatal(err)
	}

	// A person link and an identity.
	brandiss, err := s.PersonByHandle(ctx, "brandiss")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.LinkPerson(ctx, actor, task.ID, brandiss.ID, api.RoleInvolved, now); err != nil {
		t.Fatal(err)
	}
	if err := s.LinkIdentity(ctx, brandiss.ID, "planner", "graph-object-id-1"); err != nil {
		t.Fatal(err)
	}

	// A series with a live instance.
	due := now.Format(recur.DateLayout)
	if _, _, err := s.CreateSeries(ctx, actor, store.Series{
		RRule: "FREQ=WEEKLY", Mode: recur.ModeFixed, TZ: now.Location().String(),
		Template: api.TaskCreate{Title: "Weekly review", DueAt: &due},
	}, now); err != nil {
		t.Fatal(err)
	}

	// An attachment row.
	if _, err := s.AddAttachment(ctx, actor, api.Attachment{
		TaskID: task.ID, SHA256: strings.Repeat("a", 64),
		Filename: "quote.pdf", Bytes: 1024, Mime: "application/pdf",
	}, now); err != nil {
		t.Fatal(err)
	}

	// A saved filter.
	if _, err := s.PutSavedFilter(ctx, api.SavedFilter{
		Slot: 4, Name: "Truck", Query: "#truck",
	}); err != nil {
		t.Fatal(err)
	}

	// A completed task, so completed_at and a status event travel.
	other, err := s.GetByNum(ctx, 104)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Complete(ctx, actor, other.ID, now); err != nil {
		t.Fatal(err)
	}
}

// TestFoldStateIsNotExported. Section 11 says view state must not generate
// events, appear in an export, or sync to a plugin. Leaving it out of the
// type is the structural half; this is the half that would notice somebody
// adding it back.
func TestFoldStateIsNotExported(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()

	task, err := s.GetByNum(ctx, 101)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetCollapsed(ctx, task.ID, true); err != nil {
		t.Fatal(err)
	}
	// The filter somebody is reading is the same class of thing as a fold, and
	// travels no further.
	const reading = "#a-filter-somebody-was-reading"
	if err := s.SetCurrentFilter(ctx, reading); err != nil {
		t.Fatal(err)
	}

	out, err := s.Export(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, word := range []string{"collapsed", "ui_state", "fold", "view_state", reading} {
		if strings.Contains(string(body), word) {
			t.Errorf("the export mentions %q", word)
		}
	}
}

// TestAnExportCarriesNoCredential. A backup gets copied to object storage,
// and a file that can log somebody in is a different kind of object.
func TestAnExportCarriesNoCredential(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()

	tok, err := s.CreateToken(ctx, "test", "me", []string{api.ScopeRead}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnsureSigningKeys(ctx, now); err != nil {
		t.Fatal(err)
	}

	out, err := s.Export(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(string(body), tok.Secret) {
		t.Fatal("the export contains a live token")
	}
	for _, word := range []string{
		"password_hash", "totp_secret", "token_hash", "recovery",
		"PRIVATE KEY", "secret_hash", "refresh_token",
	} {
		if strings.Contains(string(body), word) {
			t.Errorf("the export mentions %q", word)
		}
	}
}

// TestImportRefusesANonEmptyDatabase. Merging two task sets means deciding
// what a colliding number means, and there is no answer to that which is not
// somebody's data quietly disappearing.
func TestImportRefusesANonEmptyDatabase(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()

	out, err := s.Export(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Import(ctx, out); err == nil {
		t.Error("importing into a populated database was allowed")
	}
}

// TestImportRefusesAnUnknownVersion, because a backup that silently
// half-restores is worse than one that refuses.
func TestImportRefusesAnUnknownVersion(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()

	out, err := s.Export(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	out.Version = store.ExportVersion + 1

	if err := fresh(t).Import(ctx, out); err == nil {
		t.Error("an export from a future version was accepted")
	}
}

// TestTheRestoredDatabaseWorks. A restore that produces rows the rest of the
// code cannot read is not a restore.
func TestTheRestoredDatabaseWorks(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()
	exerciseEverything(t, s, now)

	out, err := s.Export(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	restored := fresh(t)
	if err := restored.Import(ctx, out); err != nil {
		t.Fatal(err)
	}

	// The home filter returns the same list in the same order.
	const home = "is:open src:local -is:inbox -is:snoozed -is:deferred"
	before, err := s.List(ctx, home, now)
	if err != nil {
		t.Fatal(err)
	}
	after, err := restored.List(ctx, home, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != len(after) {
		t.Fatalf("%d tasks before, %d after", len(before), len(after))
	}
	for i := range before {
		if before[i].Num != after[i].Num {
			t.Errorf("position %d: %d before, %d after", i, before[i].Num, after[i].Num)
		}
	}

	// Full-text search works, which means the FTS triggers fired on import.
	found, err := restored.List(ctx, "tires", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) == 0 {
		t.Error("search found nothing after a restore; the FTS index is empty")
	}

	// A new task carries on from the highest number rather than colliding.
	created, err := restored.Create(ctx, actor, api.TaskCreate{Title: "After the restore"}, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range out.Tasks {
		if task.Num == created.Num {
			t.Fatalf("the new task reused number %d", created.Num)
		}
	}

	// And undo still works, which means the event log came across intact.
	if _, err := restored.Undo(ctx, actor, now); err != nil {
		t.Errorf("undo after a restore: %v", err)
	}
}

// firstDifference points at where two JSON documents diverge, because a diff
// of two multi-kilobyte strings is unreadable.
func firstDifference(a, b string) string {
	limit := min(len(a), len(b))
	for i := range limit {
		if a[i] != b[i] {
			from := max(i-120, 0)
			return "at byte " + itoaExport(i) + ":\n  before: " + a[from:min(i+120, len(a))] +
				"\n   after: " + b[from:min(i+120, len(b))]
		}
	}
	if len(a) != len(b) {
		return "lengths differ: " + itoaExport(len(a)) + " before, " + itoaExport(len(b)) + " after"
	}
	return "identical"
}

func itoaExport(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// TestAPluginCredentialIsNotExported. A backup gets copied to object storage,
// and a Graph refresh token in it would be a credential leaving the machine
// it was granted on.
func TestAPluginCredentialIsNotExported(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()

	const refresh = "a-graph-refresh-token"
	if err := s.SavePluginSettings(ctx, "planner", true,
		json.RawMessage(`{"plans":["PLAN-1"]}`), 15, now); err != nil {
		t.Fatal(err)
	}
	if err := s.SavePluginCredential(ctx, "planner",
		json.RawMessage(`{"refresh_token":"`+refresh+`"}`), now); err != nil {
		t.Fatal(err)
	}

	out, err := s.Export(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), refresh) {
		t.Fatal("the export contains a Graph refresh token")
	}
	// The whole plugin table is absent, not merely stripped of secrets: the
	// settings are a deployment's own state, not a task list.
	if strings.Contains(string(body), "plugin_config") || strings.Contains(string(body), `"plans"`) {
		t.Error("the export carries plugin configuration")
	}
}
