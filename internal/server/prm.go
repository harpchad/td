package server

import (
	"net/http"

	"github.com/harpchad/td/internal/api"
)

// PRMPath is where RFC 9728 Protected Resource Metadata lives. The 2026-07-28
// MCP revision makes it mandatory for a resource server, and it is the first
// link in the discovery chain the 401 hands out.
//
// One deployment note, because it fails in a way that looks like an
// application bug: the reverse proxy has to pass /.well-known/* through to td.
// nginx-proxy-manager already intercepts /.well-known/acme-challenge/ for
// certificate issuance, and a rule broad enough to swallow the rest of
// /.well-known/ breaks discovery entirely. Check that before debugging
// anything else.
const PRMPath = "/.well-known/oauth-protected-resource"

// protectedResourceMetadata is the RFC 9728 document.
type protectedResourceMetadata struct {
	// Resource must exactly match the MCP URL. A token's audience is checked
	// against this string, and an audience mismatch is the failure people hit,
	// because it is what stops a token minted for another server from being
	// replayed here.
	Resource string `json:"resource"`

	AuthorizationServers   []string `json:"authorization_servers"`
	ScopesSupported        []string `json:"scopes_supported"`
	BearerMethodsSupported []string `json:"bearer_methods_supported"`

	ResourceName          string `json:"resource_name"`
	ResourceDocumentation string `json:"resource_documentation,omitempty"`
}

// protectedResource serves the metadata unauthenticated, which is the point:
// a client reads it precisely because it has no credential yet.
func (s *Server) protectedResource(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "public, max-age=3600")
	writeJSON(w, http.StatusOK, protectedResourceMetadata{
		Resource:             s.ResourceURL(),
		AuthorizationServers: []string{s.baseURL},
		ScopesSupported: []string{
			// The minimal set for basic functionality, which is what this
			// field is for. Write is not here because it is not needed to be
			// useful, and a client that needs it is told so by a step-up
			// challenge naming it rather than asking for it up front.
			api.MCPScopeRead, api.MCPScopeCapture,
		},
		// Header only. There is no endpoint anywhere in td that accepts a
		// token in a query string: it ends up in access logs, browser
		// history, and Referer headers.
		BearerMethodsSupported: []string{"header"},
		ResourceName:           "td",
	})
}
