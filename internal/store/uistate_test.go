package store_test

import (
	"context"
	"testing"
)

// View state, which is the state that is nobody's business but the screen's.
// TestNobodyHasChosenAFilterYet. No row and an empty row are different
// answers, and home reads them differently: one opens slot 1, the other
// honours a box somebody emptied on purpose.
func TestNobodyHasChosenAFilterYet(t *testing.T) {
	s, _ := seeded(t)
	ctx := context.Background()

	if _, ok, err := s.CurrentFilter(ctx); err != nil || ok {
		t.Fatalf("a fresh database reported a chosen filter (ok=%v, err=%v)", ok, err)
	}

	if err := s.SetCurrentFilter(ctx, "#certs"); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.CurrentFilter(ctx)
	if err != nil || !ok || got != "#certs" {
		t.Fatalf("CurrentFilter = %q, %v, %v", got, ok, err)
	}

	// Clearing is a choice and has to survive as one.
	if err := s.SetCurrentFilter(ctx, ""); err != nil {
		t.Fatal(err)
	}
	got, ok, err = s.CurrentFilter(ctx)
	if err != nil || !ok || got != "" {
		t.Fatalf("after clearing, CurrentFilter = %q, %v, %v", got, ok, err)
	}
}

// TestOnlyOneFilterIsEverRemembered. A filter is a place you are, not a
// collection, and the table is shaped so it cannot hold two.
func TestOnlyOneFilterIsEverRemembered(t *testing.T) {
	s, _ := seeded(t)
	ctx := context.Background()

	for _, f := range []string{"#certs", "is:inbox", "p:1"} {
		if err := s.SetCurrentFilter(ctx, f); err != nil {
			t.Fatal(err)
		}
	}
	got, _, err := s.CurrentFilter(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != "p:1" {
		t.Errorf("CurrentFilter = %q, want the last one written", got)
	}
}
