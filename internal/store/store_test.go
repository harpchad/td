package store_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/seed"
	"github.com/harpchad/td/internal/store"
)

func testdataPath(name string) string {
	return filepath.Join("..", "..", "testdata", name)
}

// seeded opens a scratch database loaded with testdata/seed.json and returns
// it alongside the fixed clock every case in testdata/ evaluates against.
func seeded(t *testing.T) (*store.Store, time.Time) {
	t.Helper()

	d, err := seed.Load(testdataPath("seed.json"))
	if err != nil {
		t.Fatalf("load seed: %v", err)
	}
	now, loc, err := d.Clock()
	if err != nil {
		t.Fatalf("seed clock: %v", err)
	}

	s, err := store.Open(":memory:", store.Options{Location: loc})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if err := s.Seed(context.Background(), d); err != nil {
		t.Fatalf("seed store: %v", err)
	}
	return s, now
}

func nums(tasks []api.Task) []int64 {
	out := make([]int64, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, t.Num)
	}
	return out
}

func sortedNums(tasks []api.Task) []int64 {
	out := nums(tasks)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func equalNums(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

type filterCases struct {
	Cases []struct {
		Q      string  `json:"q"`
		Expect []int64 `json:"expect"`
		Note   string  `json:"note"`
	} `json:"cases"`
}

// TestFilterCases runs every query case in testdata/filter_cases.json against
// the seeded database. The expected sets were computed independently of this
// implementation.
func TestFilterCases(t *testing.T) {
	body, err := os.ReadFile(testdataPath("filter_cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f filterCases
	if err := json.Unmarshal(body, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Cases) == 0 {
		t.Fatal("no cases loaded")
	}

	s, now := seeded(t)
	ctx := context.Background()

	for _, c := range f.Cases {
		t.Run(c.Q, func(t *testing.T) {
			got, err := s.List(ctx, c.Q, now)
			if err != nil {
				t.Fatalf("list %q: %v", c.Q, err)
			}
			want := c.Expect
			if want == nil {
				want = []int64{}
			}
			if g := sortedNums(got); !equalNums(g, want) {
				t.Errorf("query %q\n got: %v\nwant: %v\nnote: %s", c.Q, g, want, c.Note)
			}
		})
	}
}

type sortCases struct {
	Cases []struct {
		Name        string  `json:"name"`
		Filter      string  `json:"filter"`
		ExpectOrder []int64 `json:"expect_order"`
	} `json:"cases"`
}

// TestSortCases runs the ordering cases in testdata/sort_cases.json. Filtering
// happens in SQL and ordering in Go, so this exercises the same comparator
// the TUI and the web UI use.
func TestSortCases(t *testing.T) {
	body, err := os.ReadFile(testdataPath("sort_cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f sortCases
	if err := json.Unmarshal(body, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Cases) == 0 {
		t.Fatal("no cases loaded")
	}

	s, now := seeded(t)
	ctx := context.Background()

	for _, c := range f.Cases {
		t.Run(c.Name, func(t *testing.T) {
			got, err := s.List(ctx, c.Filter, now)
			if err != nil {
				t.Fatalf("list %q: %v", c.Filter, err)
			}
			if g := nums(got); !equalNums(g, c.ExpectOrder) {
				t.Errorf("filter %q\n got: %v\nwant: %v", c.Filter, g, c.ExpectOrder)
			}
		})
	}
}

// TestSortIsDeterministic covers the stability requirement: two runs over the
// same data return the same order.
func TestSortIsDeterministic(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()

	first, err := s.List(ctx, "is:open", now)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		again, err := s.List(ctx, "is:open", now)
		if err != nil {
			t.Fatal(err)
		}
		if !equalNums(nums(first), nums(again)) {
			t.Fatalf("run %d differs\n got: %v\nwant: %v", i, nums(again), nums(first))
		}
	}
}

// TestSeedIsReproducible checks that a second load of the same fixture
// produces the same ids, so an export diff stays readable.
func TestSeedIsReproducible(t *testing.T) {
	ids := func() []string {
		s, now := seeded(t)
		tasks, err := s.List(context.Background(), "", now)
		if err != nil {
			t.Fatal(err)
		}
		out := make([]string, 0, len(tasks))
		for _, task := range tasks {
			out = append(out, task.ID)
		}
		return out
	}
	a, b := ids(), ids()
	if len(a) != len(b) {
		t.Fatalf("length %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("id %d: %s vs %s", i, a[i], b[i])
		}
	}
}

// TestSeedWritesNoEvents locks the decision that seeding is not a mutation
// the user made, so a freshly seeded database has an empty activity feed.
func TestSeedWritesNoEvents(t *testing.T) {
	s, _ := seeded(t)
	events, err := s.Events(context.Background(), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Errorf("seeded database has %d events, want 0", len(events))
	}
}

// TestHydration checks the fields that live in other tables and drive the row
// rendering: tags, people, groups, attachment counts, and the 2/5 badge.
func TestHydration(t *testing.T) {
	s, _ := seeded(t)
	ctx := context.Background()

	parent, err := s.GetByNum(ctx, 101)
	if err != nil {
		t.Fatal(err)
	}
	if len(parent.Tags) != 2 || parent.Tags[0] != "certs" || parent.Tags[1] != "ops" {
		t.Errorf("101 tags = %v, want [certs ops]", parent.Tags)
	}
	if parent.ChildrenTotal != 1 || parent.ChildrenDone != 0 {
		t.Errorf("101 children = %d/%d, want 0/1", parent.ChildrenDone, parent.ChildrenTotal)
	}
	if len(parent.People) != 1 || parent.People[0].Role != api.RoleAssigner {
		t.Errorf("101 people = %+v, want one assigner", parent.People)
	}

	waiting, err := s.GetByNum(ctx, 106)
	if err != nil {
		t.Fatal(err)
	}
	if len(waiting.People) != 1 || waiting.People[0].Role != api.RoleWaiting {
		t.Errorf("106 people = %+v, want one waiting link", waiting.People)
	}

	withAttachment, err := s.GetByNum(ctx, 114)
	if err != nil {
		t.Fatal(err)
	}
	if withAttachment.Attachments != 1 {
		t.Errorf("114 attachments = %d, want 1", withAttachment.Attachments)
	}

	grouped, err := s.GetByNum(ctx, 108)
	if err != nil {
		t.Fatal(err)
	}
	if len(grouped.Groups) != 1 {
		t.Errorf("108 groups = %v, want one", grouped.Groups)
	}
}

// discardLogger keeps the scheduler quiet in tests.
func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }
