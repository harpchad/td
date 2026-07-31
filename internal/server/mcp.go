package server

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/mcpsrv"
)

// MCPPath is where the protocol is served. It is also the resource identifier
// an access token's audience has to match exactly, which is why it is a
// constant rather than a string in two places.
const MCPPath = "/mcp"

// AttachMCP mounts the MCP server. baseURL is the server's own public URL,
// which the discovery chain in the 401 is built from: without an absolute
// resource_metadata a client never finds the authorization server, and the
// usual symptom is the MCP endpoint seeing traffic while the AS sees none.
func (s *Server) AttachMCP(baseURL string) {
	s.baseURL = strings.TrimRight(baseURL, "/")
	s.mcp = mcpsrv.New(s.store, func() time.Time { return s.Now() })
}

// ResourceURL is the canonical identifier of this MCP resource.
func (s *Server) ResourceURL() string { return s.baseURL + MCPPath }

// mcpHandler serves POST /mcp behind the same authentication as everything
// else, and hands the caller through to the tools as a principal.
func (s *Server) mcpHandler(w http.ResponseWriter, r *http.Request) {
	if s.mcp == nil {
		http.NotFound(w, r)
		return
	}
	p := principalOf(r.Context())
	if p == nil {
		// Unreachable behind the authentication middleware, but a handler
		// that would run anonymously if someone reordered the middleware is a
		// handler waiting to be a hole.
		s.mcpUnauthorized(w, r, "")
		return
	}

	// Read is the floor for reaching the protocol at all. Which tools the
	// credential can actually call is checked per tool, because a client that
	// can list tools and gets told which ones it may use is a better failure
	// than one that sees nothing.
	if !p.has(api.ScopeRead) {
		s.insufficientScope(w, api.ScopeRead)
		return
	}

	ctx := mcpsrv.WithPrincipal(r.Context(), &mcpsrv.Principal{
		Actor: p.Actor, Scopes: p.Scopes,
	})
	s.mcp.Handler().ServeHTTP(w, r.WithContext(ctx))
}

// mcpUnauthorized answers 401 with the discovery chain.
//
// The WWW-Authenticate header is the whole of client discovery: it names the
// Protected Resource Metadata document, which names the authorization server,
// which names its own endpoints. RFC 9728 requires it, and its absence is the
// single most common reason an MCP client cannot authenticate.
func (s *Server) mcpUnauthorized(w http.ResponseWriter, r *http.Request, scope string) {
	s.logAuth(r, api.KindAuthDenied, "anonymous", map[string]any{
		"path": r.URL.Path, "reason": "no valid credential",
	})
	w.Header().Set("WWW-Authenticate", s.challenge("", scope))
	w.WriteHeader(http.StatusUnauthorized)
}

// insufficientScope is the runtime scope failure, which RFC 6750 makes a 403
// and distinct from the 401 that means "no credential at all". A client that
// gets 401 retries the whole authorization dance; one that gets this knows to
// ask for more scope instead.
func (s *Server) insufficientScope(w http.ResponseWriter, scope string) {
	w.Header().Set("WWW-Authenticate", s.challenge("insufficient_scope", scope))
	writeJSON(w, http.StatusForbidden, &api.Error{
		Code:    api.ErrForbidden,
		Message: "this credential does not carry the " + scope + " scope",
	})
}

// challenge builds the Bearer challenge. resource_metadata is absolute
// because a client resolving a relative one against the wrong base is a
// failure mode with no error message.
func (s *Server) challenge(errCode, scope string) string {
	parts := []string{`Bearer realm=` + strconv.Quote("td")}
	if errCode != "" {
		parts = append(parts, `error=`+strconv.Quote(errCode))
	}
	parts = append(parts, `resource_metadata=`+strconv.Quote(s.baseURL+PRMPath))
	if scope != "" {
		parts = append(parts, `scope=`+strconv.Quote(scope))
	}
	return strings.Join(parts, ", ")
}
