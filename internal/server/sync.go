package server

import (
	"net/http"
	"strings"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/sync"
)

// syncSource applies a plugin batch.
//
// The scope is per source: a token minted for sync:planner cannot post to
// sync:jira. That is checked in requiredScope before this runs, and it is the
// reason each plugin gets its own token rather than one shared "sync" one.
func (s *Server) syncSource(w http.ResponseWriter, r *http.Request) {
	source := strings.TrimSpace(r.PathValue("source"))
	if source == "" {
		s.fail(w, &api.Error{Code: api.ErrBadRequest, Message: "no sync source in the path"})
		return
	}

	var in sync.Request
	if err := decode(r, &in); err != nil {
		s.fail(w, err)
		return
	}

	// The actor is the plugin, so a bad import is separable from your own work
	// in the activity feed and in an undo.
	res, err := s.store.Sync(r.Context(), s.actorOf(r), source, in, s.Now())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
