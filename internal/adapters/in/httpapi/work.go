package httpapi

import "net/http"

// routeWork serves the proof of an item's work: its commits, the commit of
// each delivery and its pull request.
func (s *Server) routeWork() {
	s.mux.Handle("GET /v1/items/{id}/work", s.auth(s.guardHTTP("ItemWork", http.HandlerFunc(s.handleItemWork))))
}

func (s *Server) handleItemWork(w http.ResponseWriter, r *http.Request) {
	v, err := s.svc.ItemWork(r.Context(), r.URL.Query().Get("project"), actorFrom(r.Context()), r.PathValue("id"))
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, v)
}
