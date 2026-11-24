package acp

// A fake ACP agent: the test binary re-run as a child process with a
// scenario in its environment (the helper-process pattern). It speaks the
// agent side of the protocol, version 1, as the specification describes it
// (agentclientprotocol.com), and records what Barnraise answered.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"sync"
	"time"

	"github.com/leandrodaf/barnraise/internal/adapters/out/provcommon"
)

const fakeVar = "BARNRAISE_FAKE_ACP"

// scenario is what the fake agent does.
type scenario struct {
	Version      int        `json:"version,omitempty"`       // protocolVersion it answers (default 1)
	LoadSession  bool       `json:"load_session,omitempty"`  // it can resume sessions
	MCPHTTP      bool       `json:"mcp_http,omitempty"`      // it reaches http MCP servers
	AuthRequired bool       `json:"auth_required,omitempty"` // session/new fails with auth_required
	Models       []string   `json:"models,omitempty"`        // models it offers
	Steps        []fakeStep `json:"steps,omitempty"`         // during the prompt turn
	StopReason   string     `json:"stop_reason,omitempty"`   // default end_turn
	PromptError  string     `json:"prompt_error,omitempty"`  // the prompt answers this error
	Usage        bool       `json:"usage,omitempty"`         // usage in the prompt answer
	WaitCancel   bool       `json:"wait_cancel,omitempty"`   // the turn lasts until session/cancel
	IgnoreEOF    bool       `json:"ignore_eof,omitempty"`    // does not end when stdin closes
	ExitAtInit   bool       `json:"exit_at_init,omitempty"`  // dies on initialize
	Record       string     `json:"record,omitempty"`        // JSONL of everything it received
}

// fakeStep is a session/update the agent sends, or a request it makes.
type fakeStep struct {
	Update  json.RawMessage `json:"update,omitempty"`
	Request string          `json:"request,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// fakeCommand runs the fake agent for any program name.
func fakeCommand(sc scenario) provcommon.CommandFunc {
	b, _ := json.Marshal(sc)
	return func(_ context.Context, name string, args ...string) *exec.Cmd {
		if name != "fake-agent" {
			return exec.Command(name, args...) // git, terminals
		}
		cmd := exec.Command(os.Args[0], args...)
		cmd.Env = []string{fakeVar + "=" + string(b), "GORACE=atexit_sleep_ms=0"}
		return cmd
	}
}

// fakeAgentMain runs the agent when the binary is started as one.
func fakeAgentMain() {
	raw := os.Getenv(fakeVar)
	if raw == "" {
		return
	}
	var sc scenario
	if err := json.Unmarshal([]byte(raw), &sc); err != nil {
		fmt.Fprintln(os.Stderr, "fake agent: bad scenario:", err)
		os.Exit(2)
	}
	runFakeAgent(sc)
	os.Exit(0)
}

type fakeMsg struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

func runFakeAgent(sc scenario) {
	var rec *os.File
	if sc.Record != "" {
		rec, _ = os.OpenFile(sc.Record, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	}
	var wmu sync.Mutex
	send := func(m fakeMsg) {
		m.JSONRPC = "2.0"
		b, _ := json.Marshal(m)
		wmu.Lock()
		_, _ = os.Stdout.Write(append(b, '\n'))
		wmu.Unlock()
	}
	in := make(chan fakeMsg, 64)
	go func() {
		sc := bufio.NewScanner(os.Stdin)
		sc.Buffer(make([]byte, 1<<20), 64<<20)
		for sc.Scan() {
			var m fakeMsg
			if json.Unmarshal(sc.Bytes(), &m) == nil {
				if rec != nil {
					_, _ = rec.Write(append(append([]byte(nil), sc.Bytes()...), '\n'))
				}
				in <- m
			}
		}
		close(in)
	}()
	nextID := 100
	cancelled := make(chan struct{}, 1)
	// call sends a request to the client and waits for its answer.
	call := func(method string, params json.RawMessage) {
		nextID++
		id := json.RawMessage(strconv.Itoa(nextID))
		send(fakeMsg{ID: id, Method: method, Params: params})
		for m := range in {
			if m.Method == "session/cancel" {
				select {
				case cancelled <- struct{}{}:
				default:
				}
				continue
			}
			if string(m.ID) == string(id) && m.Method == "" {
				return
			}
		}
	}
	session := "sess-1"
	result := func(id json.RawMessage, v any) {
		b, _ := json.Marshal(v)
		send(fakeMsg{ID: id, Result: b})
	}
	for m := range in {
		switch m.Method {
		case "initialize":
			if sc.ExitAtInit {
				fmt.Fprintln(os.Stderr, "fake agent: crashed on purpose")
				os.Exit(3)
			}
			v := sc.Version
			if v == 0 {
				v = 1
			}
			result(m.ID, map[string]any{"protocolVersion": v, "agentInfo": map[string]string{"name": "fake-agent", "version": "0.1"},
				"agentCapabilities": map[string]any{"loadSession": sc.LoadSession, "mcpCapabilities": map[string]bool{"http": sc.MCPHTTP}},
				"authMethods":       []map[string]string{{"id": "login", "name": "Log in with the browser"}}})
		case "session/new", "session/load":
			if sc.AuthRequired {
				send(fakeMsg{ID: m.ID, Error: &RPCError{Code: -32000, Message: "Authentication required"}})
				continue
			}
			if m.Method == "session/load" {
				var p struct {
					SessionID string `json:"sessionId"`
				}
				_ = json.Unmarshal(m.Params, &p)
				session = p.SessionID
				// The history of the session is replayed before the answer.
				b, _ := json.Marshal(map[string]any{"sessionId": session, "update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]string{"type": "text", "text": "old history"}}})
				send(fakeMsg{Method: "session/update", Params: b})
				result(m.ID, map[string]any{})
				continue
			}
			res := map[string]any{"sessionId": session}
			if len(sc.Models) > 0 {
				var ms []map[string]string
				for _, id := range sc.Models {
					ms = append(ms, map[string]string{"modelId": id, "name": id})
				}
				res["models"] = map[string]any{"availableModels": ms, "currentModelId": sc.Models[0]}
			}
			result(m.ID, res)
		case "session/set_model", "session/set_config_option":
			result(m.ID, map[string]any{})
		case "session/prompt":
			for _, st := range sc.Steps {
				if st.Request != "" {
					call(st.Request, st.Params)
					continue
				}
				b, _ := json.Marshal(map[string]any{"sessionId": session, "update": st.Update})
				send(fakeMsg{Method: "session/update", Params: b})
			}
			stop := sc.StopReason
			if sc.WaitCancel {
				select {
				case <-cancelled:
				case m2, ok := <-in:
					if !ok || m2.Method != "session/cancel" {
						os.Exit(4)
					}
				}
				stop = "cancelled"
			}
			if sc.PromptError != "" {
				send(fakeMsg{ID: m.ID, Error: &RPCError{Code: -32603, Message: sc.PromptError}})
				continue
			}
			if stop == "" {
				stop = "end_turn"
			}
			res := map[string]any{"stopReason": stop}
			if sc.Usage {
				res["usage"] = map[string]int{"inputTokens": 120, "outputTokens": 30, "thoughtTokens": 5, "cachedReadTokens": 1000, "cachedWriteTokens": 7, "totalTokens": 1162}
			}
			result(m.ID, res)
		case "session/cancel":
			select {
			case cancelled <- struct{}{}:
			default:
			}
		}
	}
	if sc.IgnoreEOF {
		signal.Ignore(os.Interrupt)
		time.Sleep(time.Minute)
	}
}
