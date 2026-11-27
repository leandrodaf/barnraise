package httpapi

import (
	"mime"
	"net/http"
	"path"
	"strings"
)

// The pictures an item's work made (app.ItemImages). The panel fetches each
// with its token and shows it from memory: an <img> cannot send the token.
func (s *Server) routeImages() {
	s.mux.Handle("GET /v1/items/{id}/images", s.auth(s.guardHTTP("ItemImages", http.HandlerFunc(s.handleItemImages))))
	s.mux.Handle("GET /v1/items/{id}/image", s.auth(s.guardHTTP("ItemImage", http.HandlerFunc(s.handleItemImage))))
}

func (s *Server) handleItemImages(w http.ResponseWriter, r *http.Request) {
	v, err := s.svc.ItemImages(r.Context(), r.URL.Query().Get("project"), actorFrom(r.Context()), r.PathValue("id"))
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, v)
}

func (s *Server) handleItemImage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	file := q.Get("path")
	b, err := s.svc.ItemImageBytes(r.Context(), q.Get("project"), actorFrom(r.Context()), r.PathValue("id"), file, q.Get("v") == "before")
	if err != nil {
		writeAppErr(w, err)
		return
	}
	ct := mime.TypeByExtension(strings.ToLower(path.Ext(file)))
	if ct == "" {
		ct = "application/octet-stream"
	}
	h := w.Header()
	h.Set("Content-Type", ct)
	h.Set("X-Content-Type-Options", "nosniff")
	// An SVG opened on its own runs no script and loads nothing.
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	h.Set("Cache-Control", "private, max-age=30")
	_, _ = w.Write(b)
}
