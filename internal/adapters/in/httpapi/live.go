package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/leandrodaf/barnraise/internal/app"
	"github.com/leandrodaf/barnraise/internal/domain"
)

// ProcWatchSession names the live view of a session for the session-token
// guard. It is not in the session allow-list: an agent's token never watches.
const ProcWatchSession = "WatchSession"

// The live view of an item's running session (ADR-0048): opened by the
// panel's Agent tab, closed when the tab closes. Server-sent events:
//
//	session   {agent, role, step, model, machine, …}   the session followed
//	line      {at, kind, tool, text, error}             what the agent does
//	end       {at, text}                                 the session ended
//	idle      {}                                         no session runs on the item
//	elsewhere {machine, why}                             it runs on a machine this daemon cannot reach
//	message   {id, from, to, text, at}                   the item's messages, before and as they come
//
//	GET /v1/items/{id}/live?project=…[&agent=…]
func (s *Server) routeLive() {
	s.mux.Handle("GET /v1/items/{id}/live", s.auth(s.guardHTTP(ProcWatchSession, http.HandlerFunc(s.handleLive))))
}

type liveMessage struct {
	ID   string    `json:"id"`
	From string    `json:"from"`
	To   string    `json:"to"`
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}

func (s *Server) handleLive(w http.ResponseWriter, r *http.Request) {
	a := actorFrom(r.Context())
	project := r.URL.Query().Get("project")
	if a.Project != "" && project == "" {
		project = a.Project
	}
	id := r.PathValue("id")
	msgs, err := s.svc.ItemMessages(a, project, id, 50)
	if err != nil {
		writeAppErr(w, err)
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	// Subscribed before the backlog is read, so no message falls between them.
	news, cancelNews := s.svc.Hub().Subscribe(project, []string{domain.EvMessageSent}, 64)
	defer cancelNews()
	watch, werr := s.svc.WatchSession(r.Context(), a, project, id, r.URL.Query().Get("agent"))
	if errors.Is(werr, app.ErrForbidden) {
		writeAppErr(w, werr)
		return
	}
	if watch != nil {
		defer watch.Stop()
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	send := func(event string, v any) {
		data, _ := json.Marshal(v)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
	}
	seen := map[string]bool{}
	sendMsg := func(m domain.Message) {
		if seen[m.ID] {
			return
		}
		seen[m.ID] = true
		send("message", liveMessage{ID: m.ID, From: m.From, To: m.To, Text: m.Text, At: m.At})
	}
	for _, m := range msgs {
		sendMsg(m)
	}
	var lines <-chan app.LiveLine
	var ee *app.ElsewhereError
	switch {
	case watch != nil:
		send("session", watch.Session)
		for _, l := range watch.Backlog {
			send("line", l)
		}
		lines = watch.Lines
	case errors.As(werr, &ee):
		send("elsewhere", map[string]string{"machine": ee.Machine, "why": ee.Why})
	default:
		send("idle", map[string]string{})
	}
	fl.Flush()
	item := id
	if n, err := domain.NormalizeItemID(id); err == nil {
		item = n
	}
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case l, ok := <-lines:
			if !ok {
				lines = nil // the session ended; the conversation stays open
				continue
			}
			if l.Kind == "end" {
				send("end", l)
			} else {
				send("line", l)
			}
		case e, ok := <-news:
			if !ok {
				return
			}
			pl, err := e.DecodePayload()
			if err != nil || pl.Message == nil || (pl.Message.ItemID != item && pl.Message.To != "item:"+item) {
				continue
			}
			sendMsg(*pl.Message)
		case <-ping.C:
			fmt.Fprintf(w, ": ping\n\n")
		}
		fl.Flush()
	}
}
