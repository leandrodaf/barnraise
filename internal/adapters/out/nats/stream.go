package nats

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"
)

// A stream between machines: one machine asks another for a flow of
// messages (the live lines of a session) that lasts while it keeps asking.
// The opening is a request on an RPC subject; the flow comes on an inbox
// of the asker; the asker renews a lease, and the side that sends stops
// when the lease runs out or the asker says stop. Nothing is stored.

// Headers of the stream protocol.
const (
	streamOpHeader    = "Br-Stream-Op"    // open, keep, stop
	streamIDHeader    = "Br-Stream-Id"    // the stream, from the open's reply on
	streamInboxHeader = "Br-Stream-Inbox" // where the flow goes
	streamEndHeader   = "Br-Stream-End"   // the last message of a flow
)

// StreamKeep is how often the asker renews a stream; the sender ends it
// after three renewals missed. A variable for the tests.
var StreamKeep = 15 * time.Second

// StreamOpen opens the flow for a request: the first answer, then the
// next messages until the channel closes; stop ends it early.
type StreamOpen func(ctx context.Context, data []byte) (first []byte, next <-chan []byte, stop func(), err error)

type servedStream struct {
	stop  func()
	keep  atomic.Int64 // unix nanos of the last renewal
	ended chan struct{}
}

// ServeStream answers stream requests on subject until ctx ends.
func ServeStream(ctx context.Context, nc *nats.Conn, subject string, open StreamOpen) error {
	var mu sync.Mutex
	streams := map[string]*servedStream{}
	var seq atomic.Int64
	answer := func(m *nats.Msg, data []byte, id string, err error) {
		reply := nats.NewMsg(m.Reply)
		if err != nil {
			reply.Header.Set(errHeader, err.Error())
		} else {
			reply.Data = data
			reply.Header.Set(streamIDHeader, id)
		}
		_ = m.RespondMsg(reply)
	}
	sub, err := nc.Subscribe(subject, func(m *nats.Msg) {
		op := ""
		if m.Header != nil {
			op = m.Header.Get(streamOpHeader)
		}
		switch op {
		case "keep", "stop":
			id := m.Header.Get(streamIDHeader)
			mu.Lock()
			st := streams[id]
			mu.Unlock()
			if st == nil {
				if m.Reply != "" {
					answer(m, nil, "", errors.New("the stream ended"))
				}
				return
			}
			if op == "stop" {
				st.stop()
			} else {
				st.keep.Store(time.Now().UnixNano())
			}
			if m.Reply != "" {
				answer(m, nil, id, nil)
			}
			return
		}
		inbox := ""
		if m.Header != nil {
			inbox = m.Header.Get(streamInboxHeader)
		}
		if inbox == "" {
			answer(m, nil, "", errors.New("a stream needs an inbox"))
			return
		}
		first, next, stop, err := open(ctx, m.Data)
		if err != nil {
			answer(m, nil, "", err)
			return
		}
		id := strconv.FormatInt(time.Now().UnixNano(), 36) + "-" + strconv.FormatInt(seq.Add(1), 36)
		st := &servedStream{ended: make(chan struct{})}
		var once sync.Once
		st.stop = func() { once.Do(func() { stop(); close(st.ended) }) }
		st.keep.Store(time.Now().UnixNano())
		mu.Lock()
		streams[id] = st
		mu.Unlock()
		answer(m, first, id, nil)
		go func() {
			defer func() {
				st.stop()
				mu.Lock()
				delete(streams, id)
				mu.Unlock()
				end := nats.NewMsg(inbox)
				end.Header.Set(streamEndHeader, "1")
				_ = nc.PublishMsg(end)
			}()
			lease := time.NewTicker(StreamKeep)
			defer lease.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-st.ended:
					return
				case <-lease.C:
					if time.Since(time.Unix(0, st.keep.Load())) > 3*StreamKeep {
						return // the asker is gone
					}
				case data, ok := <-next:
					if !ok {
						return
					}
					_ = nc.Publish(inbox, data)
				}
			}
		}()
	})
	if err != nil {
		return err
	}
	context.AfterFunc(ctx, func() {
		_ = sub.Unsubscribe()
		mu.Lock()
		list := make([]*servedStream, 0, len(streams))
		for _, st := range streams {
			list = append(list, st)
		}
		mu.Unlock()
		for _, st := range list {
			st.stop()
		}
	})
	return nc.Flush()
}

// RequestStream opens a stream on subject: the first answer, then the
// flow on next until the sender ends it or ctx ends (next is closed).
// Ending ctx tells the sender to stop.
func RequestStream(ctx context.Context, nc *nats.Conn, subject string, data []byte, timeout time.Duration) ([]byte, <-chan []byte, error) {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	inbox := nc.NewRespInbox()
	raw := make(chan *nats.Msg, 256)
	sub, err := nc.ChanSubscribe(inbox, raw)
	if err != nil {
		return nil, nil, err
	}
	req := nats.NewMsg(subject)
	req.Data = data
	req.Header.Set(streamOpHeader, "open")
	req.Header.Set(streamInboxHeader, inbox)
	rctx, cancel := context.WithTimeout(ctx, timeout)
	msg, err := nc.RequestMsgWithContext(rctx, req)
	cancel()
	if err == nil && msg.Header.Get(errHeader) != "" {
		err = &RemoteError{Msg: msg.Header.Get(errHeader)}
	}
	if err != nil {
		_ = sub.Unsubscribe()
		return nil, nil, err
	}
	id := msg.Header.Get(streamIDHeader)
	control := func(op string) error {
		m := nats.NewMsg(subject)
		m.Header.Set(streamOpHeader, op)
		m.Header.Set(streamIDHeader, id)
		if op == "stop" {
			return nc.PublishMsg(m)
		}
		kctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		r, err := nc.RequestMsgWithContext(kctx, m)
		if err == nil && r.Header.Get(errHeader) != "" {
			err = &RemoteError{Msg: r.Header.Get(errHeader)}
		}
		return err
	}
	out := make(chan []byte, 256)
	go func() {
		defer close(out)
		defer func() { _ = sub.Unsubscribe() }()
		keep := time.NewTicker(StreamKeep)
		defer keep.Stop()
		for {
			select {
			case <-ctx.Done():
				_ = control("stop")
				return
			case <-keep.C:
				var re *RemoteError
				if err := control("keep"); errors.As(err, &re) {
					return // the sender no longer has it
				}
			case m := <-raw:
				if m.Header.Get(streamEndHeader) != "" {
					return
				}
				select {
				case out <- m.Data:
				case <-ctx.Done():
					_ = control("stop")
					return
				}
			}
		}
	}()
	return msg.Data, out, nil
}
