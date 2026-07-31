package main

import (
	"path/filepath"
	"testing"
	"time"
)

// TestParseClock covers the -now flag. The @seed form is what makes a running
// server agree with the case files in testdata/, which all evaluate against
// one fixed instant.
func TestParseClock(t *testing.T) {
	chicago, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("empty means the real clock", func(t *testing.T) {
		got, err := parseClock("", chicago)
		if err != nil {
			t.Fatal(err)
		}
		if got != nil {
			t.Errorf("got %v, want nil", got)
		}
	})

	t.Run("a seed file supplies the instant and the zone", func(t *testing.T) {
		got, err := parseClock("@"+filepath.Join("..", "..", "testdata", "seed.json"), time.UTC)
		if err != nil {
			t.Fatal(err)
		}
		if got == nil {
			t.Fatal("got nil, want the fixture clock")
		}
		if want := "2026-08-03T10:30:00-05:00"; got.Format(time.RFC3339) != want {
			t.Errorf("clock = %s, want %s", got.Format(time.RFC3339), want)
		}
		// The zone has to come from the fixture too: the whole point is that
		// today means the fixture's today.
		if got.Location().String() != "America/Chicago" {
			t.Errorf("location = %s, want America/Chicago", got.Location())
		}
	})

	t.Run("an explicit instant is read in the configured zone", func(t *testing.T) {
		got, err := parseClock("2026-08-03T10:30:00-05:00", chicago)
		if err != nil {
			t.Fatal(err)
		}
		if got == nil || got.Format(time.RFC3339) != "2026-08-03T10:30:00-05:00" {
			t.Errorf("got %v", got)
		}
	})

	t.Run("garbage is refused rather than silently ignored", func(t *testing.T) {
		if _, err := parseClock("last tuesday", chicago); err == nil {
			t.Error("expected an error")
		}
		if _, err := parseClock("@/no/such/seed.json", chicago); err == nil {
			t.Error("expected an error")
		}
	})
}

// TestRequireLoopback covers the phase 1 posture: the API is unauthenticated
// until phase 2, so an accidental public bind is refused rather than
// discovered.
func TestRequireLoopback(t *testing.T) {
	tests := []struct {
		addr    string
		allowed bool
		ok      bool
	}{
		{addr: "127.0.0.1:8080", ok: true},
		{addr: "localhost:8080", ok: true},
		{addr: "[::1]:8080", ok: true},
		{addr: "0.0.0.0:8080", ok: false},
		{addr: "192.168.1.10:8080", ok: false},
		{addr: ":8080", ok: false},
		// The container publishes to the host's loopback only, so it opts in
		// explicitly rather than being allowed by accident.
		{addr: "0.0.0.0:8080", allowed: true, ok: true},
		// A malformed address is refused whatever the flag says.
		{addr: "not-an-address", allowed: true, ok: false},
	}

	for _, tc := range tests {
		err := requireLoopback(tc.addr, tc.allowed)
		if tc.ok && err != nil {
			t.Errorf("requireLoopback(%q, %v) = %v, want nil", tc.addr, tc.allowed, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("requireLoopback(%q, %v) = nil, want a refusal", tc.addr, tc.allowed)
		}
	}
}
