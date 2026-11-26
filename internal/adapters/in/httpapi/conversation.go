package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
)

// routeConversation serves the owner's conversation: one session per machine,
// shared with the command line and the provider's app on a phone.
func (s *Server) routeConversation() {
	s.mux.Handle("GET /v1/conversation", s.auth(s.guardHTTP("Conversation", http.HandlerFunc(s.handleConversation))))
	s.mux.Handle("POST /v1/conversation/messages", s.auth(s.guardHTTP("ConversationSend", http.HandlerFunc(s.handleConversationSend))))
	s.mux.Handle("POST /v1/conversation/pair", s.auth(s.guardHTTP("ConversationPair", http.HandlerFunc(s.handleConversationPair))))
	s.mux.Handle("POST /v1/conversation/account", s.auth(s.guardHTTP("ConversationAccount", http.HandlerFunc(s.handleConversationAccount))))
	s.mux.Handle("POST /v1/conversation/{action}", s.auth(s.guardHTTP("ConversationControl", http.HandlerFunc(s.handleConversationControl))))
}

func (s *Server) handleConversation(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.Atoi(r.URL.Query().Get("after"))
	v, err := s.svc.Conversation(r.Context(), actorFrom(r.Context()), after)
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, v)
}

func (s *Server) handleConversationAccount(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Account string `json:"account"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "body: "+err.Error())
		return
	}
	if err := s.svc.ConversationUse(r.Context(), actorFrom(r.Context()), body.Account); err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleConversationPair(w http.ResponseWriter, r *http.Request) {
	p, err := s.svc.ConversationPair(r.Context(), actorFrom(r.Context()))
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, p)
}

func (s *Server) handleConversationSend(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "body: "+err.Error())
		return
	}
	if err := s.svc.ConversationSend(r.Context(), actorFrom(r.Context()), body.Text); err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// handleConversationControl: start, stop, interrupt, new, keep and release.
func (s *Server) handleConversationControl(w http.ResponseWriter, r *http.Request) {
	a, ctx := actorFrom(r.Context()), r.Context()
	var err error
	switch r.PathValue("action") {
	case "start":
		err = s.svc.ConversationStart(ctx, a)
	case "stop":
		err = s.svc.ConversationStop(ctx, a)
	case "interrupt":
		err = s.svc.ConversationInterrupt(ctx, a)
	case "new":
		err = s.svc.ConversationNew(ctx, a)
	case "keep":
		err = s.svc.ConversationKeep(ctx, a, true)
	case "release":
		err = s.svc.ConversationKeep(ctx, a, false)
	default:
		writeErr(w, http.StatusNotFound, "unknown action: start, stop, interrupt, new, keep or release")
		return
	}
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}
