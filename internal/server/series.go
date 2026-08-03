package server

import (
	"net/http"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/store"
)

// seriesResponse is a series plus, on creation, the instance it materialized.
// Returning both saves the client a second request to find out what appeared
// in the list.
type seriesResponse struct {
	Series store.Series `json:"series"`
	Task   *api.Task    `json:"task,omitempty"`
}

func (s *Server) getSeries(w http.ResponseWriter, r *http.Request) {
	series, err := s.store.Series(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, seriesResponse{Series: series})
}

func (s *Server) createSeries(w http.ResponseWriter, r *http.Request) {
	var in store.Series
	if err := decode(r, &in); err != nil {
		s.fail(w, err)
		return
	}
	series, task, err := s.store.CreateSeries(r.Context(), s.actorOf(r), in, s.Now())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, seriesResponse{Series: series, Task: &task})
}

// repeatTask turns an existing task into the first instance of a new series.
//
// Distinct from POST /series, which materializes from the template because it
// is called when no task exists yet. Called from a task, that path leaves an
// exact duplicate beside the original: same title, same due date, one attached
// to the series and one not. This is the route a client uses when somebody is
// looking at a task and says "repeat this".
func (s *Server) repeatTask(w http.ResponseWriter, r *http.Request) {
	id, ok := s.resolve(w, r)
	if !ok {
		return
	}
	var in store.Series
	if err := decode(r, &in); err != nil {
		s.fail(w, err)
		return
	}
	series, task, err := s.store.RepeatTask(r.Context(), s.actorOf(r), id, in, s.Now())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, seriesResponse{Series: series, Task: &task})
}

// updateSeries is the explicit series edit. Section 3 says editing an
// instance edits that instance and editing the series needs its own action,
// so there is no path by which a PATCH on a task reaches this.
func (s *Server) updateSeries(w http.ResponseWriter, r *http.Request) {
	var in store.Series
	if err := decode(r, &in); err != nil {
		s.fail(w, err)
		return
	}
	in.ID = r.PathValue("id")
	series, err := s.store.UpdateSeries(r.Context(), in)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, seriesResponse{Series: series})
}
