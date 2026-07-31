package query_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/query"
	"github.com/harpchad/td/internal/seed"
)

// testdataDir is the oracle. Every case below comes out of it and none of
// them may be edited to make a test pass.
func testdataDir(t *testing.T) string {
	t.Helper()
	return filepath.Join("..", "..", "testdata")
}

type filterFile struct {
	ASTCases []struct {
		Q   string          `json:"q"`
		AST json.RawMessage `json:"ast"`
	} `json:"ast_cases"`
	ErrorCases []struct {
		Q     string `json:"q"`
		Error string `json:"error"`
	} `json:"error_cases"`
	DateCases struct {
		Anchor   string            `json:"anchor"`
		Resolves map[string]string `json:"resolves"`
	} `json:"date_cases"`
}

func loadFilterFile(t *testing.T) filterFile {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(testdataDir(t), "filter_cases.json"))
	if err != nil {
		t.Fatalf("read filter_cases.json: %v", err)
	}
	var f filterFile
	if err := json.Unmarshal(body, &f); err != nil {
		t.Fatalf("parse filter_cases.json: %v", err)
	}
	return f
}

func seedClock(t *testing.T) time.Time {
	t.Helper()
	d, err := seed.Load(filepath.Join(testdataDir(t), "seed.json"))
	if err != nil {
		t.Fatalf("load seed: %v", err)
	}
	now, _, err := d.Clock()
	if err != nil {
		t.Fatalf("seed clock: %v", err)
	}
	return now
}

func TestParseAST(t *testing.T) {
	f := loadFilterFile(t)
	now := seedClock(t)

	for _, c := range f.ASTCases {
		t.Run(c.Q, func(t *testing.T) {
			node, err := query.ParseAt(c.Q, now)
			if err != nil {
				t.Fatalf("parse %q: %v", c.Q, err)
			}
			got, err := json.Marshal(node)
			if err != nil {
				t.Fatalf("marshal ast: %v", err)
			}

			var gotAny, wantAny any
			if err := json.Unmarshal(got, &gotAny); err != nil {
				t.Fatalf("decode got: %v", err)
			}
			if err := json.Unmarshal(c.AST, &wantAny); err != nil {
				t.Fatalf("decode want: %v", err)
			}
			if !reflect.DeepEqual(gotAny, wantAny) {
				t.Errorf("ast mismatch for %q\n got: %s\nwant: %s", c.Q, got, c.AST)
			}
		})
	}
}

func TestParseErrors(t *testing.T) {
	f := loadFilterFile(t)
	now := seedClock(t)

	for _, c := range f.ErrorCases {
		t.Run(c.Q, func(t *testing.T) {
			_, err := query.ParseAt(c.Q, now)
			if err == nil {
				t.Fatalf("parse %q: expected an error, got none", c.Q)
			}
			if !strings.HasPrefix(err.Error(), c.Error) {
				t.Errorf("parse %q\n got: %s\nwant prefix: %s", c.Q, err.Error(), c.Error)
			}
		})
	}
}

func TestResolveDate(t *testing.T) {
	f := loadFilterFile(t)
	now := seedClock(t)

	if got := now.Format(query.DateLayout); got != f.DateCases.Anchor {
		t.Fatalf("seed clock is %s, fixture anchors on %s", got, f.DateCases.Anchor)
	}

	for keyword, want := range f.DateCases.Resolves {
		t.Run(keyword, func(t *testing.T) {
			got, err := query.ResolveDate(keyword, now)
			if err != nil {
				t.Fatalf("resolve %q: %v", keyword, err)
			}
			if got != want {
				t.Errorf("resolve %q = %s, want %s", keyword, got, want)
			}
		})
	}
}

// TestResolveDateMonthClamping locks the note in the fixture that +1m is
// calendar arithmetic with clamping and not a 30 day addition.
func TestResolveDateMonthClamping(t *testing.T) {
	loc, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Fatal(err)
	}
	jan31 := time.Date(2026, 1, 31, 12, 0, 0, 0, loc)
	got, err := query.ResolveDate("+1m", jan31)
	if err != nil {
		t.Fatal(err)
	}
	if got != "2026-02-28" {
		t.Errorf("+1m from 2026-01-31 = %s, want 2026-02-28", got)
	}
}

// TestResolveDateUsesFixtureTimezone locks the fixture note that at 23:30
// Central the server clock is already tomorrow in UTC and today is still
// today.
func TestResolveDateUsesFixtureTimezone(t *testing.T) {
	loc, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Fatal(err)
	}
	late := time.Date(2026, 8, 3, 23, 30, 0, 0, loc)
	if late.UTC().Format(query.DateLayout) != "2026-08-04" {
		t.Fatal("this test is only meaningful if the UTC date has already rolled over")
	}
	got, err := query.ResolveDate("today", late)
	if err != nil {
		t.Fatal(err)
	}
	if got != "2026-08-03" {
		t.Errorf("today at 23:30 Central = %s, want 2026-08-03", got)
	}
}

func TestParseEmpty(t *testing.T) {
	for _, q := range []string{"", "   "} {
		node, err := query.ParseAt(q, seedClock(t))
		if err != nil {
			t.Fatalf("parse %q: %v", q, err)
		}
		if node != nil {
			t.Errorf("parse %q = %v, want nil", q, node)
		}
	}
}

// TestParseKeepsHyphenInsideWords guards the lexer rule that a minus only
// negates at the start of a term, so an issue key stays one word.
func TestParseKeepsHyphenInsideWords(t *testing.T) {
	node, err := query.ParseAt("OPS-1421", seedClock(t))
	if err != nil {
		t.Fatal(err)
	}
	w, ok := node.(*query.Word)
	if !ok {
		t.Fatalf("got %T, want *query.Word", node)
	}
	if w.Text != "ops-1421" {
		t.Errorf("text = %q, want %q", w.Text, "ops-1421")
	}
}

// TestSortIsTotal checks the stability requirement in sort_cases.json: no two
// rows may compare equal, so two runs over the same data return the same
// order.
func TestSortIsTotal(t *testing.T) {
	now := seedClock(t)
	s := query.NewSorter(now)

	due := "2026-08-03"
	p := 2
	mk := func(num int64) api.Task {
		return api.Task{
			Num: num, Priority: &p, DueAt: &due,
			CreatedAt: "2026-07-01T09:00:00-05:00",
		}
	}
	a, b := mk(102), mk(110)
	if !s.Less(&a, &b) {
		t.Error("102 should sort before 110 on the num tiebreak")
	}
	if s.Less(&b, &a) {
		t.Error("the comparator is not antisymmetric")
	}
	if s.Less(&a, &a) {
		t.Error("a task compares less than itself")
	}
}

// TestSortBucketOutranksPriority locks the case the fixture calls out as
// looking like a bug in a screenshot.
func TestSortBucketOutranksPriority(t *testing.T) {
	now := seedClock(t)
	s := query.NewSorter(now)

	p1, p4 := 1, 4
	today, tomorrow := "2026-08-03", "2026-08-04"
	low := api.Task{Num: 1, Priority: &p4, DueAt: &today, CreatedAt: "2026-07-01T09:00:00-05:00"}
	high := api.Task{Num: 2, Priority: &p1, DueAt: &tomorrow, CreatedAt: "2026-07-01T09:00:00-05:00"}

	if !s.Less(&low, &high) {
		t.Error("a P4 due today must sort above a P1 due tomorrow")
	}
}
