// Package boundary_test enforces the package layout that BUILD-SPEC.md
// section 1 calls the split. The rules are structural, so they are checked by
// walking the real import graph rather than by reading the imports at the top
// of a file: a violation three packages deep is exactly the one that erodes
// the boundary without anyone noticing.
package boundary_test

import (
	"os/exec"
	"strings"
	"testing"
)

// forbidden lists, per binary, the packages that must never appear anywhere
// in its transitive import graph.
var forbidden = map[string][]string{
	// The client never opens the database file. If it could, td would only
	// work on the box holding it, and every query would need two
	// implementations.
	"github.com/harpchad/td/cmd/td": {
		"github.com/harpchad/td/internal/store",
		"github.com/harpchad/td/internal/server",
		"github.com/harpchad/td/internal/seed",
		"github.com/harpchad/td/internal/web",
		"github.com/harpchad/td/internal/notify",
		"modernc.org/sqlite",
		// Section 1: the client links no password hashing. It holds a bearer
		// token and nothing else, so argon2 and TOTP have no business in a
		// binary that cross-compiles to a laptop.
		"github.com/harpchad/td/internal/auth",
		"golang.org/x/crypto/argon2",
		"github.com/pquerna/otp",
		// Section 1: no MCP server in the client. td talks to tdd over HTTP
		// like every other client; a second protocol server inside the
		// binary that cross-compiles to a laptop is not what /mcp is for.
		"github.com/harpchad/td/internal/mcpsrv",
		"github.com/modelcontextprotocol/go-sdk/mcp",
		"github.com/harpchad/td/internal/blob",
	},
	// The server links no terminal UI.
	"github.com/harpchad/td/cmd/tdd": {
		"github.com/harpchad/td/internal/tui",
		"charm.land/bubbletea/v2",
	},
}

func deps(t *testing.T, pkg string) map[string]bool {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "go", "list", "-deps", pkg).Output()
	if err != nil {
		t.Fatalf("go list -deps %s: %v", pkg, err)
	}
	set := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		set[strings.TrimSpace(line)] = true
	}
	return set
}

func TestImportBoundary(t *testing.T) {
	for pkg, banned := range forbidden {
		graph := deps(t, pkg)
		for _, bad := range banned {
			if graph[bad] {
				t.Errorf("%s imports %s. The split has already failed; see BUILD-SPEC.md section 1.", pkg, bad)
			}
		}
	}
}

// TestSharedPackagesStayShared checks the other half of the rule: the filter
// grammar and the API types have to be reachable from both binaries, because
// one grammar parsed by two parsers is the drift the split exists to prevent.
func TestSharedPackagesStayShared(t *testing.T) {
	shared := []string{
		"github.com/harpchad/td/internal/api",
		"github.com/harpchad/td/internal/query",
	}
	for _, bin := range []string{"github.com/harpchad/td/cmd/td", "github.com/harpchad/td/cmd/tdd"} {
		graph := deps(t, bin)
		for _, pkg := range shared {
			if !graph[pkg] {
				t.Errorf("%s does not import %s", bin, pkg)
			}
		}
	}
}
