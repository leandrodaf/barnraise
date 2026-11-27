package mcpcatalog

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/leandrodaf/barnraise/internal/adapters/out/execx"
	"github.com/leandrodaf/barnraise/internal/domain"
)

// stdioTools starts the server and talks newline-delimited JSON-RPC over
// its standard input and output.
func stdioTools(ctx context.Context, def domain.MCPServerDef) ([]string, error) {
	if _, err := exec.LookPath(def.Command); err != nil {
		return nil, fmt.Errorf("%s is not installed on this machine", def.Command)
	}
	cmd := exec.CommandContext(ctx, def.Command, def.Args...)
	execx.KillGroupOnCancel(cmd, 2*time.Second)
	// Started as Claude Code starts a server: some refuse to run without
	// the session id it passes them (session-aware bridges).
	cmd.Env = append(os.Environ(), "CLAUDE_CODE_SESSION_ID="+probeSessionID())
	for k, v := range def.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr tail
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", def.Command, err)
	}
	exited := false
	defer func() {
		if exited {
			return
		}
		_ = in.Close()
		_ = cmd.Cancel()
		_ = cmd.Wait()
	}()
	enc := json.NewEncoder(in)
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	wait := func(id int) (rpcReply, error) {
		for sc.Scan() {
			var r rpcReply
			if json.Unmarshal(sc.Bytes(), &r) != nil || !r.idIs(id) {
				continue // logs or notifications on stdout
			}
			return r, r.err()
		}
		// The server closed its output: let it exit, so all it wrote on
		// stderr is there to say why.
		_ = in.Close()
		_ = cmd.Wait()
		exited = true
		why := lastWords(stderr.String())
		if why == "" && ctx.Err() != nil {
			why = "no answer"
		}
		if why == "" {
			why = "the server closed its output"
		}
		return rpcReply{}, errors.New(why)
	}
	if err := enc.Encode(initRequest()); err != nil {
		return nil, err
	}
	if _, err := wait(1); err != nil {
		return nil, err
	}
	_ = enc.Encode(initialized)
	if err := enc.Encode(listTools); err != nil {
		return nil, err
	}
	r, err := wait(2)
	if err != nil {
		return nil, err
	}
	return toolNames(r.Result)
}

// tail keeps the last bytes a server wrote on stderr, for the error shown.
type tail struct {
	mu sync.Mutex
	b  []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if len(t.b) > 600 {
		t.b = t.b[len(t.b)-600:]
	}
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return ansiRe.ReplaceAllString(string(t.b), "")
}

// lastWords picks what a server said last before dying: its last error
// line, else its last line (logs come first, the reason at the end).
func lastWords(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if strings.Contains(strings.ToLower(l), "error") || strings.Contains(strings.ToLower(l), "fatal") || strings.Contains(l, "panic") {
			return l
		}
	}
	return strings.TrimSpace(lines[len(lines)-1])
}

// probeSessionID is a fresh id in the shape of Claude Code's session ids.
func probeSessionID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// ansiRe matches terminal colour codes, which servers put in their logs.
var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

var httpClient = &http.Client{Timeout: 30 * time.Second}

// httpTools speaks the streamable HTTP transport: each message is a POST,
// answered with JSON or with an event stream.
func httpTools(ctx context.Context, def domain.MCPServerDef) ([]string, error) {
	session := ""
	post := func(msg map[string]any) (*rpcReply, error) {
		b, _ := json.Marshal(msg)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, def.URL, bytes.NewReader(b))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("MCP-Protocol-Version", protocolVersion)
		if session != "" {
			req.Header.Set("Mcp-Session-Id", session)
		}
		for k, v := range def.Headers {
			req.Header.Set(k, v)
		}
		res, err := httpClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer res.Body.Close()
		if s := res.Header.Get("Mcp-Session-Id"); s != "" {
			session = s
		}
		if err := httpProblem(res, len(def.Headers) > 0); err != nil {
			return nil, err
		}
		id, _ := msg["id"].(int)
		if id == 0 {
			return nil, nil // a notification: nothing comes back
		}
		if strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
			return readEvents(res.Body, id)
		}
		var r rpcReply
		if err := json.NewDecoder(io.LimitReader(res.Body, 16<<20)).Decode(&r); err != nil {
			return nil, fmt.Errorf("unreadable answer: %w", err)
		}
		return &r, r.err()
	}
	if _, err := post(initRequest()); err != nil {
		return nil, err
	}
	_, _ = post(initialized)
	r, err := post(listTools)
	if err != nil {
		return nil, err
	}
	return toolNames(r.Result)
}

// httpProblem explains an answer that is not MCP: a refusal, an OAuth
// server (Claude logs in to those itself, agents cannot), or a web page
// (a login page in front of the server).
func httpProblem(res *http.Response, withHeaders bool) error {
	switch {
	case res.StatusCode == http.StatusUnauthorized && strings.Contains(strings.ToLower(res.Header.Get("WWW-Authenticate")), "resource_metadata"):
		return errors.New("the server signs in with OAuth (Claude's /mcp login), which agents cannot do: give it a token in headers, if it takes one, or leave it to your own Claude")
	case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
		if withHeaders {
			return fmt.Errorf("the server refused the credentials in its headers (%d)", res.StatusCode)
		}
		return fmt.Errorf("the server asks for credentials (%d) and its definition has no headers", res.StatusCode)
	case res.StatusCode >= 300:
		body, _ := io.ReadAll(io.LimitReader(res.Body, 300))
		return fmt.Errorf("HTTP %d: %s", res.StatusCode, strings.TrimSpace(string(body)))
	case strings.HasPrefix(res.Header.Get("Content-Type"), "text/html"):
		return errors.New("the address answers a web page, not MCP: a login page (Cloudflare Access, a VPN) may stand in front of the server")
	}
	return nil
}

// readEvents reads server-sent events until the answer to id.
func readEvents(body io.Reader, id int) (*rpcReply, error) {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	var data strings.Builder
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "data:"):
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		case line == "":
			var r rpcReply
			if json.Unmarshal([]byte(data.String()), &r) == nil && r.idIs(id) {
				return &r, r.err()
			}
			data.Reset()
		}
	}
	return nil, errors.New("the event stream ended without an answer")
}

// sseTools speaks the older HTTP+SSE transport: a GET opens the event
// stream, whose first "endpoint" event says where to POST the messages;
// the answers come on the stream.
func sseTools(ctx context.Context, def domain.MCPServerDef) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, def.URL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	for k, v := range def.Headers {
		req.Header.Set(k, v)
	}
	res, err := (&http.Client{}).Do(req) // the stream stays open: no client timeout, the context bounds it
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if err := httpProblem(res, len(def.Headers) > 0); err != nil {
		return nil, err
	}
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	next := func() (event, data string, ok bool) {
		var d strings.Builder
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event:"):
				event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			case strings.HasPrefix(line, "data:"):
				d.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			case line == "" && d.Len() > 0:
				return event, d.String(), true
			}
		}
		return "", "", false
	}
	ev, endpoint, ok := next()
	for ok && ev != "endpoint" {
		ev, endpoint, ok = next()
	}
	if !ok {
		return nil, errors.New("the server sent no endpoint")
	}
	base, _ := url.Parse(def.URL)
	ep, err := base.Parse(strings.TrimSpace(endpoint))
	if err != nil {
		return nil, fmt.Errorf("endpoint %q: %w", endpoint, err)
	}
	send := func(msg map[string]any) error {
		b, _ := json.Marshal(msg)
		r, err := http.NewRequestWithContext(ctx, http.MethodPost, ep.String(), bytes.NewReader(b))
		if err != nil {
			return err
		}
		r.Header.Set("Content-Type", "application/json")
		for k, v := range def.Headers {
			r.Header.Set(k, v)
		}
		resp, err := httpClient.Do(r)
		if err != nil {
			return err
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		_ = resp.Body.Close()
		if resp.StatusCode >= 300 {
			return fmt.Errorf("HTTP %d posting to %s", resp.StatusCode, ep.Path)
		}
		return nil
	}
	await := func(id int) (rpcReply, error) {
		for {
			_, data, ok := next()
			if !ok {
				return rpcReply{}, errors.New("the event stream ended without an answer")
			}
			var r rpcReply
			if json.Unmarshal([]byte(data), &r) == nil && r.idIs(id) {
				return r, r.err()
			}
		}
	}
	if err := send(initRequest()); err != nil {
		return nil, err
	}
	if _, err := await(1); err != nil {
		return nil, err
	}
	_ = send(initialized)
	if err := send(listTools); err != nil {
		return nil, err
	}
	r, err := await(2)
	if err != nil {
		return nil, err
	}
	return toolNames(r.Result)
}
