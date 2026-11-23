// Package mcp is the Model Context Protocol server agents use to talk to
// Barnraise. It speaks newline-delimited JSON-RPC 2.0 over stdio and turns
// every tool call into one call to the daemon API. Permission belongs to the
// role: a role only sees the tools it can use, and the daemon checks again.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
)

// Protocol versions this server speaks; the first is preferred.
var supportedVersions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

// JSON-RPC error codes.
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
)

// maxLine bounds one JSON-RPC message.
const maxLine = 16 << 20

// Options configure a server. Project, Role and Item come from the
// environment of the caller (BARNRAISE_PROJECT, BARNRAISE_ROLE, BARNRAISE_ITEM).
type Options struct {
	// Project is sent on every daemon call; empty lets the daemon pick.
	Project string
	// Role is the agent role of the session; empty means the owner.
	Role string
	// Item is the item of the session, the default for item_id arguments.
	Item string
	// Version is reported in serverInfo.
	Version string
}

const instructions = `Barnraise coordinates the agents working on this project. Work only through these tools.
1. Call next_item to get your assignment: the item, the step and its context. If it says there is no authorized work, record your state and end your turn; never create work for yourself.
2. Do the step. Read more with context and find instead of guessing.
3. End the step with exactly one structured result: deliver (implementers) or review (reviewers). Loose text does not move an item.
4. Out-of-scope ideas go to propose; questions only the owner can decide go to ask_owner; short notes to other agents go to message.
Tool errors explain what to fix: correct the arguments and call again.`

// Serve runs the MCP server until in ends or ctx is cancelled. Requests are
// handled concurrently; each response is written whole on its own line.
func Serve(ctx context.Context, in io.Reader, out io.Writer, c API, opts Options) error {
	if c == nil {
		return errors.New("mcp: nil API")
	}
	if opts.Version == "" {
		opts.Version = "dev"
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s := newServer(c, opts, out)

	lines := make(chan []byte)
	readErr := make(chan error, 1)
	go func() {
		r := bufio.NewReaderSize(in, 64<<10)
		for {
			line, err := readLine(r)
			if len(bytes.TrimSpace(line)) > 0 {
				select {
				case lines <- line:
				case <-ctx.Done():
					readErr <- ctx.Err()
					return
				}
			}
			if err != nil {
				if errors.Is(err, io.EOF) {
					err = nil
				}
				readErr <- err
				return
			}
		}
	}()

	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-readErr:
			wg.Wait()
			if err != nil && ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		case line := <-lines:
			wg.Go(func() {
				s.handle(ctx, line)
			})
		}
	}
}

// readLine reads one newline-terminated line, refusing lines over maxLine.
func readLine(r *bufio.Reader) ([]byte, error) {
	var buf []byte
	for {
		chunk, err := r.ReadSlice('\n')
		buf = append(buf, chunk...)
		if len(buf) > maxLine {
			// Drop the rest of the oversized line.
			for errors.Is(err, bufio.ErrBufferFull) {
				_, err = r.ReadSlice('\n')
			}
			return []byte(`{"oversized":true}`), err
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return buf, err
	}
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type server struct {
	api   API
	opts  Options
	tools []*tool
	byNm  map[string]*tool

	wmu sync.Mutex
	out io.Writer

	mu       sync.Mutex
	item     string // current item: Options.Item or the last one next_item returned
	inflight map[string]context.CancelFunc
}

func newServer(c API, opts Options, out io.Writer) *server {
	s := &server{api: c, opts: opts, out: out, item: opts.Item, inflight: map[string]context.CancelFunc{}}
	s.tools = visibleTools(opts.Role)
	s.byNm = map[string]*tool{}
	for _, t := range s.tools {
		s.byNm[t.Name] = t
	}
	return s
}

func (s *server) currentItem() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.item
}

func (s *server) setItem(id string) {
	if id == "" {
		return
	}
	s.mu.Lock()
	s.item = id
	s.mu.Unlock()
}

func (s *server) write(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		b, _ = json.Marshal(rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"),
			Error: &rpcError{Code: codeInternalError, Message: "could not encode the response"}})
	}
	b = append(b, '\n')
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, _ = s.out.Write(b)
}

func (s *server) reply(id json.RawMessage, result any) {
	if result == nil {
		result = struct{}{}
	}
	s.write(rpcResponse{JSONRPC: "2.0", ID: id, Result: result})
}

func (s *server) fail(id json.RawMessage, code int, msg string) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	s.write(rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}})
}

func (s *server) handle(ctx context.Context, line []byte) {
	line = bytes.TrimSpace(line)
	if len(line) > 0 && line[0] == '[' {
		s.fail(nil, codeInvalidRequest, "batch requests are not supported")
		return
	}
	var req rpcRequest
	if err := json.Unmarshal(line, &req); err != nil {
		s.fail(nil, codeParseError, "parse error: "+err.Error())
		return
	}
	notification := len(req.ID) == 0 || string(req.ID) == "null"
	if req.JSONRPC != "2.0" || req.Method == "" {
		if !notification {
			s.fail(req.ID, codeInvalidRequest, `invalid request: needs "jsonrpc":"2.0" and a method`)
		}
		return
	}
	if notification {
		s.notify(req)
		return
	}

	key := string(req.ID)
	rctx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.inflight[key] = cancel
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.inflight, key)
		s.mu.Unlock()
		cancel()
	}()

	result, rerr := s.dispatch(rctx, req)
	if rctx.Err() != nil && ctx.Err() == nil {
		// Cancelled by the client: no response is expected.
		return
	}
	if rerr != nil {
		s.fail(req.ID, rerr.Code, rerr.Message)
		return
	}
	s.reply(req.ID, result)
}

func (s *server) notify(req rpcRequest) {
	switch req.Method {
	case "notifications/cancelled":
		var p struct {
			RequestID json.RawMessage `json:"requestId"`
		}
		if json.Unmarshal(req.Params, &p) == nil && len(p.RequestID) > 0 {
			s.mu.Lock()
			cancel := s.inflight[string(p.RequestID)]
			s.mu.Unlock()
			if cancel != nil {
				cancel()
			}
		}
	default:
		// notifications/initialized and any other notification need no reply.
	}
}

func (s *server) dispatch(ctx context.Context, req rpcRequest) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		version := supportedVersions[0]
		for _, v := range supportedVersions {
			if p.ProtocolVersion == v {
				version = v
			}
		}
		return map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "barnraise", "version": s.opts.Version},
			"instructions":    instructions,
		}, nil
	case "ping":
		return struct{}{}, nil
	case "tools/list":
		list := make([]map[string]any, 0, len(s.tools))
		for _, t := range s.tools {
			list = append(list, map[string]any{"name": t.Name, "description": t.Description, "inputSchema": t.Schema})
		}
		return map[string]any{"tools": list}, nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil || p.Name == "" {
			return nil, &rpcError{Code: codeInvalidParams, Message: "tools/call needs a tool name and arguments"}
		}
		t := s.byNm[p.Name]
		if t == nil {
			return nil, &rpcError{Code: codeInvalidParams, Message: "unknown tool: " + p.Name + " (available: " + s.toolNames() + ")"}
		}
		return s.call(ctx, t, p.Arguments), nil
	}
	return nil, &rpcError{Code: codeMethodNotFound, Message: "method not found: " + req.Method}
}

func (s *server) toolNames() string {
	names := make([]string, 0, len(s.tools))
	for _, t := range s.tools {
		names = append(names, t.Name)
	}
	return strings.Join(names, ", ")
}

// call runs a tool and never fails at the protocol level: problems with the
// input or the daemon come back as a result with isError, so the model can fix them.
func (s *server) call(ctx context.Context, t *tool, args json.RawMessage) (res *toolResult) {
	defer func() {
		if r := recover(); r != nil {
			res = errorResult("the " + t.Name + " tool failed unexpectedly; try again or report it")
		}
	}()
	if len(bytes.TrimSpace(args)) == 0 || string(bytes.TrimSpace(args)) == "null" {
		args = json.RawMessage("{}")
	}
	return t.run(ctx, s, args)
}
