package acp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
)

// JSON-RPC 2.0 over newline-delimited JSON, the transport of the Agent
// Client Protocol on stdio. It knows nothing of ACP itself.

// Standard JSON-RPC error codes, and the one Barnraise uses when its policy
// refuses a request of the agent.
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternal       = -32603
	codePolicy         = -32001
)

// maxLine is the largest message accepted from the agent; a longer line is
// dropped (a whole file read is far below it).
const maxLine = 64 << 20

// ErrClosed is returned by calls still waiting when the agent's output ended.
var ErrClosed = errors.New("acp: connection closed")

// RPCError is a JSON-RPC error object.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string { return fmt.Sprintf("%s (code %d)", e.Message, e.Code) }

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RequestHandler answers a request of the peer. It runs in its own
// goroutine, so it may block (waiting for a terminal to exit).
type RequestHandler func(ctx context.Context, method string, params json.RawMessage) (any, *RPCError)

// NotificationHandler handles a notification of the peer, in order, on the
// reading goroutine.
type NotificationHandler func(method string, params json.RawMessage)

// Conn is one JSON-RPC connection.
type Conn struct {
	out      *outbox
	onReq    RequestHandler
	onNote   NotificationHandler
	observe  func(incoming bool, line []byte)
	handlers sync.WaitGroup

	mu      sync.Mutex
	nextID  int64
	pending map[string]chan message
	closed  bool
	ctx     context.Context
	cancel  context.CancelFunc
}

// NewConn writes to w. observe, when set, sees every line in both
// directions (the session's transcript).
func NewConn(w io.Writer, onReq RequestHandler, onNote NotificationHandler, observe func(incoming bool, line []byte)) *Conn {
	ctx, cancel := context.WithCancel(context.Background())
	c := &Conn{out: newOutbox(w), onReq: onReq, onNote: onNote, observe: observe, pending: map[string]chan message{}, ctx: ctx, cancel: cancel}
	go c.out.run()
	return c
}

// Serve reads messages from r until it ends, then fails the calls still
// waiting and cancels the handlers still running.
func (c *Conn) Serve(r io.Reader) {
	br := bufio.NewReaderSize(r, 64<<10)
	for {
		line, err := readLine(br)
		if len(line) > 0 {
			c.dispatch(line)
		}
		if err != nil {
			break
		}
	}
	c.mu.Lock()
	c.closed = true
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
	c.mu.Unlock()
	c.cancel()
	c.handlers.Wait()
}

// readLine returns the next line without its newline; a line over maxLine
// comes back empty.
func readLine(br *bufio.Reader) ([]byte, error) {
	var buf []byte
	tooLong := false
	for {
		chunk, err := br.ReadSlice('\n')
		if !tooLong {
			if len(buf)+len(chunk) > maxLine {
				tooLong, buf = true, nil
			} else {
				buf = append(buf, chunk...)
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if tooLong {
			return nil, err
		}
		return bytes.TrimSpace(buf), err
	}
}

func (c *Conn) dispatch(line []byte) {
	if c.observe != nil {
		c.observe(true, line)
	}
	var m message
	if err := json.Unmarshal(line, &m); err != nil {
		c.send(message{ID: json.RawMessage("null"), Error: &RPCError{Code: codeParseError, Message: "parse error"}})
		return
	}
	hasID := len(m.ID) > 0 && string(m.ID) != "null"
	switch {
	case m.Method != "" && hasID:
		c.handlers.Add(1)
		go func() {
			defer c.handlers.Done()
			res, rerr := c.safeHandle(m.Method, m.Params)
			if rerr != nil {
				c.send(message{ID: m.ID, Error: rerr})
				return
			}
			raw, err := json.Marshal(res)
			if err != nil {
				c.send(message{ID: m.ID, Error: &RPCError{Code: codeInternal, Message: err.Error()}})
				return
			}
			c.send(message{ID: m.ID, Result: raw})
		}()
	case m.Method != "":
		if c.onNote != nil {
			c.onNote(m.Method, m.Params)
		}
	case hasID:
		key := idKey(m.ID)
		c.mu.Lock()
		ch, ok := c.pending[key]
		delete(c.pending, key)
		c.mu.Unlock()
		if ok {
			ch <- m
		}
	default:
		c.send(message{ID: json.RawMessage("null"), Error: &RPCError{Code: codeInvalidRequest, Message: "invalid request"}})
	}
}

// safeHandle never lets a handler bug crash the daemon.
func (c *Conn) safeHandle(method string, params json.RawMessage) (res any, rerr *RPCError) {
	defer func() {
		if r := recover(); r != nil {
			res, rerr = nil, &RPCError{Code: codeInternal, Message: fmt.Sprintf("client panic: %v", r)}
		}
	}()
	if c.onReq == nil {
		return nil, &RPCError{Code: codeMethodNotFound, Message: "method not found: " + method}
	}
	return c.onReq(c.ctx, method, params)
}

// idKey normalizes an id: 7 and "7" are different ids in JSON-RPC, so the
// raw text is the key, with insignificant spaces removed.
func idKey(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	var s string
	if len(raw) > 0 && raw[0] == '"' && json.Unmarshal(raw, &s) == nil {
		return "s:" + s
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		return "n:" + n.String()
	}
	return "r:" + string(bytes.TrimSpace(raw))
}

// Call sends a request and waits for its answer, decoded into out (when not nil).
func (c *Conn) Call(ctx context.Context, method string, params, out any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ErrClosed
	}
	c.nextID++
	id := json.RawMessage(strconv.FormatInt(c.nextID, 10))
	ch := make(chan message, 1)
	c.pending[idKey(id)] = ch
	c.mu.Unlock()
	c.send(message{ID: id, Method: method, Params: raw})
	select {
	case m, ok := <-ch:
		if !ok {
			return ErrClosed
		}
		if m.Error != nil {
			return m.Error
		}
		if out != nil && len(m.Result) > 0 {
			if err := json.Unmarshal(m.Result, out); err != nil {
				return fmt.Errorf("acp: %s: bad result: %w", method, err)
			}
		}
		return nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, idKey(id))
		c.mu.Unlock()
		return ctx.Err()
	}
}

// Notify sends a notification.
func (c *Conn) Notify(method string, params any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	c.send(message{Method: method, Params: raw})
	return nil
}

// CloseWrite flushes what is queued and closes the writer when it is a
// Closer (the agent's stdin: most agents end when it closes).
func (c *Conn) CloseWrite() { c.out.close() }

func (c *Conn) send(m message) {
	m.JSONRPC = "2.0"
	line, err := json.Marshal(m)
	if err != nil {
		return
	}
	if c.observe != nil {
		c.observe(false, line)
	}
	c.out.push(append(line, '\n'))
}

// outbox writes in order without ever blocking the caller: an agent that
// does not read its stdin while it writes stdout cannot deadlock us.
type outbox struct {
	w      io.Writer
	mu     sync.Mutex
	cond   *sync.Cond
	queue  [][]byte
	closed bool
	done   chan struct{}
}

func newOutbox(w io.Writer) *outbox {
	o := &outbox{w: w, done: make(chan struct{})}
	o.cond = sync.NewCond(&o.mu)
	return o
}

func (o *outbox) push(b []byte) {
	o.mu.Lock()
	if !o.closed {
		o.queue = append(o.queue, b)
	}
	o.mu.Unlock()
	o.cond.Signal()
}

func (o *outbox) close() {
	o.mu.Lock()
	o.closed = true
	o.mu.Unlock()
	o.cond.Broadcast()
	<-o.done
}

func (o *outbox) run() {
	defer close(o.done)
	failed := false
	for {
		o.mu.Lock()
		for len(o.queue) == 0 && !o.closed {
			o.cond.Wait()
		}
		if len(o.queue) == 0 {
			o.mu.Unlock()
			if cl, ok := o.w.(io.Closer); ok {
				_ = cl.Close()
			}
			return
		}
		b := o.queue[0]
		o.queue[0] = nil
		o.queue = o.queue[1:]
		o.mu.Unlock()
		if !failed {
			if _, err := o.w.Write(b); err != nil {
				failed = true // the agent is gone; keep draining
			}
		}
	}
}
