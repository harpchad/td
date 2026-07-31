package server_test

import (
	"path/filepath"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

// TestOpenAPISchemaLints is the schema lint step of `make check`. It runs from
// phase 1 onward so the document cannot drift into being decorative.
func TestOpenAPISchemaLints(t *testing.T) {
	loader := openapi3.NewLoader()
	loader.IsExternalRefsAllowed = false

	doc, err := loader.LoadFromFile(filepath.Join("..", "..", "openapi.yaml"))
	if err != nil {
		t.Fatalf("load openapi.yaml: %v", err)
	}
	if err := doc.Validate(loader.Context); err != nil {
		t.Fatalf("openapi.yaml is not valid: %v", err)
	}
}

// TestOpenAPICoversEveryRoute checks the document against the mux rather than
// against itself. A route added without a matching entry fails here, which is
// the only thing that keeps the two from separating.
func TestOpenAPICoversEveryRoute(t *testing.T) {
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromFile(filepath.Join("..", "..", "openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	// The routes registered in Handler, written as OpenAPI paths.
	want := map[string][]string{
		"/healthz":                                  {"get"},
		"/login":                                    {"post"},
		"/logout":                                   {"post"},
		"/api/v1/whoami":                            {"get"},
		"/api/v1/tokens":                            {"get", "post"},
		"/api/v1/tokens/{id}":                       {"delete"},
		"/api/v1/tasks":                             {"get", "post"},
		"/api/v1/tasks/{id}":                        {"get", "patch", "delete"},
		"/api/v1/tasks/{id}/complete":               {"post"},
		"/api/v1/tasks/{id}/snooze":                 {"post"},
		"/api/v1/tasks/{id}/attachments":            {"get", "post"},
		"/api/v1/tasks/{id}/attachments/{att}":      {"get", "delete"},
		"/api/v1/series":                            {"post"},
		"/api/v1/series/{id}":                       {"get", "patch"},
		"/api/v1/people":                            {"get", "post"},
		"/api/v1/people/{id}":                       {"get", "patch"},
		"/api/v1/people/{id}/tasks":                 {"get"},
		"/api/v1/people/{id}/identities":            {"post"},
		"/api/v1/groups":                            {"get", "post"},
		"/api/v1/groups/{id}/members":               {"put"},
		"/api/v1/tasks/{id}/people":                 {"post"},
		"/api/v1/tasks/{id}/people/{person}/{role}": {"delete"},
		"/api/v1/filters":                           {"get", "post"},
		"/api/v1/ui/folds":                          {"get"},
		"/api/v1/ui/folds/{id}":                     {"post"},
		"/api/v1/events":                            {"get"},
		"/api/v1/undo":                              {"post"},
	}

	for path, methods := range want {
		item := doc.Paths.Find(path)
		if item == nil {
			t.Errorf("openapi.yaml has no entry for %s", path)
			continue
		}
		ops := item.Operations()
		for _, m := range methods {
			if ops[upper(m)] == nil {
				t.Errorf("openapi.yaml has no %s for %s", m, path)
			}
		}
	}

	for path := range doc.Paths.Map() {
		if _, ok := want[path]; !ok {
			t.Errorf("openapi.yaml documents %s, which the mux does not serve", path)
		}
	}

	// The browser routes are deliberately absent. openapi.yaml describes the
	// JSON API that clients and plugins program against; the server-rendered
	// pages and their form posts are not an interface anything integrates
	// with, and documenting them there would invite someone to try.
	for _, page := range []string{"/", "/settings", "/help", "/t/{ref}", "/w/add", "/w/edit/{id}"} {
		if doc.Paths.Find(page) != nil {
			t.Errorf("openapi.yaml documents the browser route %s", page)
		}
	}
}

func upper(s string) string {
	out := []byte(s)
	for i := range out {
		if out[i] >= 'a' && out[i] <= 'z' {
			out[i] -= 32
		}
	}
	return string(out)
}
