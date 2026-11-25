package httpapi

import (
	"encoding/json"
	"net/http"
)

// Document templates are served as plain JSON next to the Connect API, like
// the lessons: the panel shows and edits them, agents never call them (none
// is on the session allow-list, ADR-0029).
//
//	GET    /v1/documents?project=…                       the templates of the project
//	PUT    /v1/documents/{name}?project=…&variant=…      write one (owner)
//	DELETE /v1/documents/{name}?project=…&variant=…      remove one (owner)
//	POST   /v1/documents/{name}/preview?project=…        render a text with a sample item
//	GET    /v1/items/{id}/documents?project=…            an item's plan, diagrams and pull request body
func (s *Server) routeDocuments() {
	s.mux.Handle("GET /v1/documents", s.auth(s.guardHTTP("ListDocuments", http.HandlerFunc(s.handleListDocuments))))
	s.mux.Handle("PUT /v1/documents/{name}", s.auth(s.guardHTTP("SaveDocument", http.HandlerFunc(s.handleSaveDocument))))
	s.mux.Handle("DELETE /v1/documents/{name}", s.auth(s.guardHTTP("DeleteDocument", http.HandlerFunc(s.handleDeleteDocument))))
	s.mux.Handle("POST /v1/documents/{name}/preview", s.auth(s.guardHTTP("PreviewDocument", http.HandlerFunc(s.handlePreviewDocument))))
	s.mux.Handle("GET /v1/items/{id}/documents", s.auth(s.guardHTTP("ItemDocuments", http.HandlerFunc(s.handleItemDocuments))))
}

const maxDocumentBody = 128 << 10

type documentBody struct {
	Text string `json:"text"`
	Area string `json:"area,omitempty"`
	Kind string `json:"kind,omitempty"`
}

func readDocumentBody(w http.ResponseWriter, r *http.Request) (documentBody, bool) {
	var b documentBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxDocumentBody)).Decode(&b); err != nil {
		writeErr(w, http.StatusBadRequest, "the body is {\"text\": \"…\"}: "+err.Error())
		return b, false
	}
	return b, true
}

func (s *Server) handleListDocuments(w http.ResponseWriter, r *http.Request) {
	v, err := s.svc.ListDocuments(r.URL.Query().Get("project"), actorFrom(r.Context()))
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, v)
}

func (s *Server) handleSaveDocument(w http.ResponseWriter, r *http.Request) {
	b, ok := readDocumentBody(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f, err := s.svc.SaveDocument(r.Context(), q.Get("project"), actorFrom(r.Context()), r.PathValue("name"), q.Get("variant"), b.Text)
	if err != nil {
		writeAppErr(w, err)
		return
	}
	s.audit(r.Context(), "document.save", f.Path, q.Get("project"), "")
	writeJSON(w, f)
}

func (s *Server) handleDeleteDocument(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if err := s.svc.DeleteDocument(r.Context(), q.Get("project"), actorFrom(r.Context()), r.PathValue("name"), q.Get("variant")); err != nil {
		writeAppErr(w, err)
		return
	}
	s.audit(r.Context(), "document.delete", r.PathValue("name")+"."+q.Get("variant"), q.Get("project"), "")
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handlePreviewDocument(w http.ResponseWriter, r *http.Request) {
	b, ok := readDocumentBody(w, r)
	if !ok {
		return
	}
	v, err := s.svc.PreviewDocument(r.URL.Query().Get("project"), actorFrom(r.Context()), r.PathValue("name"), b.Text, b.Area, b.Kind)
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, v)
}

func (s *Server) handleItemDocuments(w http.ResponseWriter, r *http.Request) {
	v, err := s.svc.ItemDocuments(r.URL.Query().Get("project"), actorFrom(r.Context()), r.PathValue("id"))
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, v)
}
