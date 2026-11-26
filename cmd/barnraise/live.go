package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/nats-io/nats.go"

	natsx "github.com/leandrodaf/barnraise/internal/adapters/out/nats"
	"github.com/leandrodaf/barnraise/internal/app"
)

// The live view of a session that runs on another machine (ADR-0048): the
// panel's daemon opens a stream on that machine, which sends the session's
// lines while the panel's tab is open, the way the other per-machine calls
// are passed on (capabilities.go).

const peerLiveTimeout = 10 * time.Second

// remoteLive is what one daemon asks another on "live".
type remoteLive struct {
	Project string `json:"project"`
	Item    string `json:"item"`
	Agent   string `json:"agent,omitempty"`
	From    string `json:"from,omitempty"`
}

// liveOpening is the first answer of a "live" stream.
type liveOpening struct {
	Session app.LiveSession `json:"session"`
	Backlog []app.LiveLine  `json:"backlog"`
}

// liveFromPeer follows a session that runs on machine.
func (d *daemon) liveFromPeer(ctx context.Context, machine, project, item, agent string) (*app.LiveWatch, error) {
	if d.repl == nil {
		return nil, &app.ElsewhereError{Machine: machine, Why: "this daemon has no connection to other machines"}
	}
	nc, ok := d.repl.Conn(machine)
	if !ok || !nc.IsConnected() {
		return nil, &app.ElsewhereError{Machine: machine, Why: "this daemon is not connected to it now"}
	}
	body, _ := json.Marshal(remoteLive{Project: project, Item: item, Agent: agent, From: d.df.Machine})
	wctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	first, next, err := natsx.RequestStream(wctx, nc, natsx.RPCSubject(machine, "live"), body, peerLiveTimeout)
	if err != nil {
		cancel()
		var re *natsx.RemoteError
		if errors.As(err, &re) && strings.Contains(re.Msg, "no session runs") {
			return nil, &app.ElsewhereError{Machine: machine, Why: "no session runs there now"}
		}
		if errors.Is(err, nats.ErrNoResponders) {
			return nil, &app.ElsewhereError{Machine: machine, Why: peerError(err, "the live view of sessions")}
		}
		return nil, &app.ElsewhereError{Machine: machine, Why: err.Error()}
	}
	var open liveOpening
	if err := json.Unmarshal(first, &open); err != nil {
		cancel()
		return nil, &app.ElsewhereError{Machine: machine, Why: "unreadable answer: " + err.Error()}
	}
	lines := make(chan app.LiveLine, 128)
	go func() {
		defer close(lines)
		for data := range next {
			var l app.LiveLine
			if json.Unmarshal(data, &l) == nil {
				select {
				case lines <- l:
				default: // a slow watcher misses lines
				}
			}
		}
	}()
	return &app.LiveWatch{Session: open.Session, Backlog: open.Backlog, Lines: lines, Stop: cancel}, nil
}

// serveRemoteLive answers the peers' live view requests for the sessions running here.
func (d *daemon) serveRemoteLive(ctx context.Context) {
	b := d.bus.v.Load()
	if b == nil {
		return
	}
	_ = natsx.ServeStream(ctx, b.Conn(), natsx.RPCSubject(d.df.Machine, "live"), func(_ context.Context, data []byte) ([]byte, <-chan []byte, func(), error) {
		var rl remoteLive
		if err := json.Unmarshal(data, &rl); err != nil {
			return nil, nil, nil, err
		}
		w, err := d.svc.WatchLocalSession(app.Actor{Kind: app.ActorOwner, Name: "owner@" + rl.From, Scopes: []string{app.ScopeAll}}, rl.Project, rl.Item, rl.Agent)
		if err != nil {
			return nil, nil, nil, err
		}
		first, err := json.Marshal(liveOpening{Session: w.Session, Backlog: w.Backlog})
		if err != nil {
			w.Stop()
			return nil, nil, nil, err
		}
		out := make(chan []byte, 64)
		go func() {
			defer close(out)
			for l := range w.Lines {
				data, _ := json.Marshal(l)
				select {
				case out <- data:
				default: // the stream is gone or slow: the line is dropped
				}
			}
		}()
		return first, out, w.Stop, nil
	})
}
