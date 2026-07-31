package server

import (
	"encoding/json"
	"net/http"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/store"
)

// export streams the whole database.
//
// A read, so it needs the read scope like any other. It is not gated behind
// anything stronger on purpose: a token that can list every task can already
// see everything in here, and putting a special scope on the backup would be
// theatre. What it does not contain is the part that matters, and that is
// enforced in store.Export rather than here.
func (s *Server) export(w http.ResponseWriter, r *http.Request) {
	out, err := s.store.Export(r.Context(), s.Now())
	if err != nil {
		s.fail(w, err)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="td-export.json"`)
	// A backup is a credential-adjacent file even without credentials in it.
	w.Header().Set("Cache-Control", "no-store")

	// Indented, because the file is something a person opens and diffs
	// against yesterday's.
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		s.log.Error("writing export", "err", err)
	}
}

// importAll restores an export into an empty database.
func (s *Server) importAll(w http.ResponseWriter, r *http.Request) {
	// A backup is large, and the 1 MB cap the other handlers use would refuse
	// any real one. 256 MB is past anything this schema can produce at the
	// scale it is built for and still bounded.
	var in store.Export
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<20))
	if err := dec.Decode(&in); err != nil {
		s.fail(w, &api.Error{Code: api.ErrBadRequest, Message: err.Error()})
		return
	}

	if err := s.store.Import(r.Context(), in); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{
		"tasks": len(in.Tasks), "events": len(in.Events), "people": len(in.People),
	})
}
