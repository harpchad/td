package main

import (
	"net"
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

// TestParseTrustedProxies covers the config that decides whether
// X-Forwarded-For is believed. An untrusted header would let a caller put a
// fresh address on every attempt and walk straight past the per-IP login
// limit, so the default trusts nothing.
func TestParseTrustedProxies(t *testing.T) {
	t.Run("empty trusts nothing", func(t *testing.T) {
		got, err := parseTrustedProxies("")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Errorf("got %d networks, want none", len(got))
		}
	})

	t.Run("CIDRs and bare addresses both parse", func(t *testing.T) {
		got, err := parseTrustedProxies("172.18.0.0/16, 10.0.0.5 ,::1")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 3 {
			t.Fatalf("got %d networks, want 3", len(got))
		}
		if !got[0].Contains(net.ParseIP("172.18.4.9")) {
			t.Error("the CIDR does not contain an address inside it")
		}
		if got[0].Contains(net.ParseIP("192.168.1.1")) {
			t.Error("the CIDR contains an address outside it")
		}
		// A bare address becomes a single-host network, not a wildcard.
		if !got[1].Contains(net.ParseIP("10.0.0.5")) || got[1].Contains(net.ParseIP("10.0.0.6")) {
			t.Error("a bare address did not become a single-host network")
		}
	})

	t.Run("garbage is refused rather than ignored", func(t *testing.T) {
		for _, bad := range []string{"not-an-address", "10.0.0.0/99", "10.0.0.0/"} {
			if _, err := parseTrustedProxies(bad); err == nil {
				t.Errorf("parseTrustedProxies(%q) was accepted", bad)
			}
		}
	})
}
