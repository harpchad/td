package server

import "github.com/harpchad/td/internal/oauth"

// SetCIMDResolverForTest points the Client ID Metadata Document resolver at a
// test double.
//
// The real resolver refuses to connect to anything but a public address, which
// is the SSRF mitigation and is not something to weaken for convenience. A
// test server listens on loopback, so the tests that exercise the flow supply
// their own HTTP client and the guard itself is tested directly in
// internal/oauth.
func (s *Server) SetCIMDResolverForTest(r *oauth.Resolver) { s.cimd = r }
