package main

import "testing"

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
