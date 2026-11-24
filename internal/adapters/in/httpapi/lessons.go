package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/leandrodaf/barnraise/internal/app"
	"github.com/leandrodaf/barnraise/internal/domain"
)

// Lessons are served as plain JSON next to the Connect API: listing and
// deleting them is an owner chore that needs no typed RPC.
//
//	GET    /v1/lessons?project=…&file=…&area=…
//	DELETE /v1/lessons/{id}?project=…
//
// Without a project, the daemon's only project is meant.
func (s *Server) routeLessons() {
	s.mux.Handle("GET /v1/lessons", s.auth(http.HandlerFunc(s.handleListLessons)))
	s.mux.Handle("DELETE /v1/lessons/{id}", s.auth(http.HandlerFunc(s.handleDeleteLesson)))
}

// LessonsResponse is the body of the lesson list.
type LessonsResponse struct {
	Lessons []*domain.Lesson `json:"lessons"`
}

func (s *Server) handleListLessons(w http.ResponseWriter, r *http.Request) {
	ls, err := s.svc.ListLessons(r.URL.Query().Get("project"), actorFrom(r.Context()),
		app.LessonFilter{File: r.URL.Query().Get("file"), Area: r.URL.Query().Get("area")})
	if err != nil {
		writeAppErr(w, err)
		return
	}
	if ls == nil {
		ls = []*domain.Lesson{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(LessonsResponse{Lessons: ls})
}

func (s *Server) handleDeleteLesson(w http.ResponseWriter, r *http.Request) {
	project := r.URL.Query().Get("project")
	l, err := s.svc.DeleteLesson(r.Context(), project, actorFrom(r.Context()), r.PathValue("id"))
	if err != nil {
		writeAppErr(w, err)
		return
	}
	s.audit(r.Context(), "lesson.delete", l.ID, project, l.Text)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(l)
}

// writeAppErr maps an app error to an HTTP status.
func writeAppErr(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	switch {
	case errors.Is(err, app.ErrNotFound), errors.Is(err, app.ErrUnknownProj):
		code = http.StatusNotFound
	case errors.Is(err, app.ErrForbidden):
		code = http.StatusForbidden
	case errors.Is(err, app.ErrInvalid):
		code = http.StatusBadRequest
	}
	writeErr(w, code, err.Error())
}
