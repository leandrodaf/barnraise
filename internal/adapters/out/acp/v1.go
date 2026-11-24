package acp

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// v1 is version 1 of the Agent Client Protocol, the current stable one
// (agentclientprotocol.com). Fields marked unstable in the specification
// (model selection, usage of a prompt turn) are read when present and
// never required.
type v1 struct{}

func (v1) Version() int { return 1 }

// --- wire types of version 1 ---

type v1Implementation struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version,omitempty"`
}

type v1InitializeRequest struct {
	ProtocolVersion    int `json:"protocolVersion"`
	ClientCapabilities struct {
		FS struct {
			ReadTextFile  bool `json:"readTextFile"`
			WriteTextFile bool `json:"writeTextFile"`
		} `json:"fs"`
		Terminal bool `json:"terminal"`
	} `json:"clientCapabilities"`
	ClientInfo *v1Implementation `json:"clientInfo,omitempty"`
}

type v1InitializeResponse struct {
	ProtocolVersion   int `json:"protocolVersion"`
	AgentCapabilities struct {
		LoadSession     bool `json:"loadSession"`
		MCPCapabilities struct {
			HTTP bool `json:"http"`
			SSE  bool `json:"sse"`
		} `json:"mcpCapabilities"`
	} `json:"agentCapabilities"`
	AgentInfo   *v1Implementation `json:"agentInfo"`
	AuthMethods []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"authMethods"`
}

type v1EnvVariable struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type v1HTTPHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// v1McpServer is the stdio variant (no type) or the http one.
type v1McpServer struct {
	Type    string          `json:"type,omitempty"`
	Name    string          `json:"name"`
	Command string          `json:"command,omitempty"`
	Args    []string        `json:"args,omitempty"`
	Env     []v1EnvVariable `json:"env,omitempty"`
	URL     string          `json:"url,omitempty"`
	Headers []v1HTTPHeader  `json:"headers,omitempty"`
}

// MarshalJSON keeps the required fields of each variant even when empty.
func (m v1McpServer) MarshalJSON() ([]byte, error) {
	if m.Type == "http" {
		h := m.Headers
		if h == nil {
			h = []v1HTTPHeader{}
		}
		return json.Marshal(struct {
			Type    string         `json:"type"`
			Name    string         `json:"name"`
			URL     string         `json:"url"`
			Headers []v1HTTPHeader `json:"headers"`
		}{"http", m.Name, m.URL, h})
	}
	args, env := m.Args, m.Env
	if args == nil {
		args = []string{}
	}
	if env == nil {
		env = []v1EnvVariable{}
	}
	return json.Marshal(struct {
		Name    string          `json:"name"`
		Command string          `json:"command"`
		Args    []string        `json:"args"`
		Env     []v1EnvVariable `json:"env"`
	}{m.Name, m.Command, args, env})
}

type v1NewSessionRequest struct {
	Cwd        string        `json:"cwd"`
	McpServers []v1McpServer `json:"mcpServers"`
}

type v1LoadSessionRequest struct {
	SessionID  string        `json:"sessionId"`
	Cwd        string        `json:"cwd"`
	McpServers []v1McpServer `json:"mcpServers"`
}

type v1NewSessionResponse struct {
	SessionID string `json:"sessionId"`
	// Unstable: the models the agent offers.
	Models *struct {
		AvailableModels []struct {
			ModelID string `json:"modelId"`
		} `json:"availableModels"`
	} `json:"models"`
	ConfigOptions []json.RawMessage `json:"configOptions"`
}

type v1ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type v1PromptRequest struct {
	SessionID string           `json:"sessionId"`
	Prompt    []v1ContentBlock `json:"prompt"`
}

type v1PromptResponse struct {
	StopReason string `json:"stopReason"`
	// Unstable: the tokens of the turn.
	Usage *struct {
		InputTokens       int64 `json:"inputTokens"`
		OutputTokens      int64 `json:"outputTokens"`
		ThoughtTokens     int64 `json:"thoughtTokens"`
		CachedReadTokens  int64 `json:"cachedReadTokens"`
		CachedWriteTokens int64 `json:"cachedWriteTokens"`
	} `json:"usage"`
}

type v1SessionNotification struct {
	SessionID string          `json:"sessionId"`
	Update    json.RawMessage `json:"update"`
}

type v1Location struct {
	Path string `json:"path"`
}

type v1ToolCallContent struct {
	Type    string          `json:"type"`
	Content *v1ContentBlock `json:"content,omitempty"`
	Path    string          `json:"path,omitempty"`
}

type v1ToolCall struct {
	ToolCallID string              `json:"toolCallId"`
	Title      string              `json:"title"`
	Kind       string              `json:"kind"`
	Status     string              `json:"status"`
	Content    []v1ToolCallContent `json:"content"`
	Locations  []v1Location        `json:"locations"`
	RawInput   json.RawMessage     `json:"rawInput"`
}

type v1Update struct {
	SessionUpdate string          `json:"sessionUpdate"`
	Content       json.RawMessage `json:"content"`
	MessageID     string          `json:"messageId"`
	Cost          *struct {
		Amount   float64 `json:"amount"`
		Currency string  `json:"currency"`
	} `json:"cost"`
}

// --- the dialect ---

func (v1) Initialize(c clientInfo) (string, any) {
	var r v1InitializeRequest
	r.ProtocolVersion = 1
	r.ClientCapabilities.FS.ReadTextFile = c.ReadFiles
	r.ClientCapabilities.FS.WriteTextFile = c.WriteFiles
	r.ClientCapabilities.Terminal = c.Terminals
	r.ClientInfo = &v1Implementation{Name: c.Name, Title: "Barnraise", Version: c.Version}
	return "initialize", r
}

func (v1) ParseInitialize(raw json.RawMessage) (agentInfo, error) {
	var r v1InitializeResponse
	if err := json.Unmarshal(raw, &r); err != nil {
		return agentInfo{}, err
	}
	a := agentInfo{Version: r.ProtocolVersion, LoadSession: r.AgentCapabilities.LoadSession, MCPHTTP: r.AgentCapabilities.MCPCapabilities.HTTP}
	if r.AgentInfo != nil {
		a.Name, a.AgentVer = r.AgentInfo.Name, r.AgentInfo.Version
	}
	for _, m := range r.AuthMethods {
		a.AuthMethods = append(a.AuthMethods, firstNonEmpty(m.Name, m.ID))
	}
	return a, nil
}

func v1Servers(mcp []mcpServer, a agentInfo) []v1McpServer {
	out := []v1McpServer{}
	for _, s := range mcp {
		if s.URL != "" {
			if !a.MCPHTTP {
				continue // the agent cannot reach an http server
			}
			m := v1McpServer{Type: "http", Name: s.Name, URL: s.URL}
			for _, k := range sortedKeys(s.Headers) {
				m.Headers = append(m.Headers, v1HTTPHeader{Name: k, Value: s.Headers[k]})
			}
			out = append(out, m)
			continue
		}
		m := v1McpServer{Name: s.Name, Command: s.Command, Args: s.Args}
		for _, k := range sortedKeys(s.Env) {
			m.Env = append(m.Env, v1EnvVariable{Name: k, Value: s.Env[k]})
		}
		out = append(out, m)
	}
	return out
}

func (v1) NewSession(cwd string, mcp []mcpServer, a agentInfo) (string, any) {
	return "session/new", v1NewSessionRequest{Cwd: cwd, McpServers: v1Servers(mcp, a)}
}

func (v1) LoadSession(id, cwd string, mcp []mcpServer, a agentInfo) (string, any) {
	return "session/load", v1LoadSessionRequest{SessionID: id, Cwd: cwd, McpServers: v1Servers(mcp, a)}
}

func (v1) ParseNewSession(raw json.RawMessage) (sessionInfo, error) {
	var r v1NewSessionResponse
	if err := json.Unmarshal(raw, &r); err != nil {
		return sessionInfo{}, err
	}
	s := sessionInfo{ID: r.SessionID}
	if r.Models != nil {
		for _, m := range r.Models.AvailableModels {
			s.Models = append(s.Models, m.ModelID)
		}
	}
	for _, o := range r.ConfigOptions {
		var opt struct {
			ID       string          `json:"id"`
			Category string          `json:"category"`
			Options  json.RawMessage `json:"options"`
		}
		if json.Unmarshal(o, &opt) != nil || (opt.Category != "model" && opt.ID != "model") {
			continue
		}
		s.ModelOption = opt.ID
		s.ModelOptionValues = optionValues(opt.Options)
		break
	}
	return s, nil
}

// optionValues collects the values of a select option, flat or in groups.
func optionValues(raw json.RawMessage) []string {
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return nil
	}
	var out []string
	for _, it := range items {
		var v struct {
			Value   *string         `json:"value"`
			Options json.RawMessage `json:"options"`
		}
		if json.Unmarshal(it, &v) != nil {
			continue
		}
		if v.Value != nil {
			out = append(out, *v.Value)
		}
		if len(v.Options) > 0 {
			out = append(out, optionValues(v.Options)...)
		}
	}
	return out
}

func (v1) SelectModel(s sessionInfo, model string) (string, any, bool) {
	if slices.Contains(s.Models, model) {
		return "session/set_model", map[string]string{"sessionId": s.ID, "modelId": model}, true
	}
	if s.ModelOption != "" && slices.Contains(s.ModelOptionValues, model) {
		return "session/set_config_option", map[string]string{"sessionId": s.ID, "configId": s.ModelOption, "value": model}, true
	}
	return "", nil, false
}

func (v1) Prompt(sessionID string, texts []string) (string, any) {
	r := v1PromptRequest{SessionID: sessionID}
	for _, t := range texts {
		if strings.TrimSpace(t) != "" {
			r.Prompt = append(r.Prompt, v1ContentBlock{Type: "text", Text: t})
		}
	}
	return "session/prompt", r
}

func (v1) ParsePrompt(raw json.RawMessage) (turnEnd, error) {
	var r v1PromptResponse
	if err := json.Unmarshal(raw, &r); err != nil {
		return turnEnd{}, err
	}
	t := turnEnd{StopReason: r.StopReason}
	if u := r.Usage; u != nil {
		t.Usage = &domain.TokenUsage{Input: u.InputTokens, Output: u.OutputTokens + u.ThoughtTokens, CacheRead: u.CachedReadTokens, CacheWrite: u.CachedWriteTokens}
	}
	return t, nil
}

func (v1) Cancel(sessionID string) (string, any) {
	return "session/cancel", map[string]string{"sessionId": sessionID}
}

func v1Tool(raw json.RawMessage) toolCall {
	var w v1ToolCall
	_ = json.Unmarshal(raw, &w)
	t := toolCall{ID: w.ToolCallID, Title: w.Title, Kind: w.Kind, Status: w.Status, RawInput: w.RawInput}
	add := func(p string) {
		if p != "" && !slices.Contains(t.Paths, p) {
			t.Paths = append(t.Paths, p)
		}
	}
	for _, l := range w.Locations {
		add(l.Path)
	}
	var out []string
	for _, c := range w.Content {
		switch c.Type {
		case "diff":
			add(c.Path)
		case "content":
			if c.Content != nil && c.Content.Type == "text" {
				out = append(out, c.Content.Text)
			}
		}
	}
	t.Output = strings.Join(out, "\n")
	return t
}

func (v1) ParseUpdate(method string, raw json.RawMessage) []update {
	if method != "session/update" {
		return nil
	}
	var n v1SessionNotification
	if json.Unmarshal(raw, &n) != nil {
		return nil
	}
	var u v1Update
	if json.Unmarshal(n.Update, &u) != nil {
		return nil
	}
	out := update{SessionID: n.SessionID, Raw: n.Update, Kind: updOther}
	switch u.SessionUpdate {
	case "agent_message_chunk", "agent_thought_chunk":
		var b v1ContentBlock
		_ = json.Unmarshal(u.Content, &b)
		if b.Type != "text" {
			return nil
		}
		out.Kind, out.Text, out.MessageID = updMessage, b.Text, u.MessageID
		if u.SessionUpdate == "agent_thought_chunk" {
			out.Kind = updThought
		}
	case "tool_call":
		out.Kind, out.Tool = updToolCall, v1Tool(n.Update)
	case "tool_call_update":
		out.Kind, out.Tool = updToolUpdate, v1Tool(n.Update)
	case "usage_update":
		out.Kind = updUsage
		if u.Cost != nil && strings.EqualFold(u.Cost.Currency, "USD") {
			out.CostUSD, out.HasCost = u.Cost.Amount, true
		}
	}
	return []update{out}
}

// --- requests of the agent ---

func (v1) Serve(ctx context.Context, cl *client, method string, raw json.RawMessage) (any, *RPCError, bool) {
	bad := func(err error) *RPCError { return &RPCError{Code: codeInvalidParams, Message: err.Error()} }
	empty := struct{}{}
	switch method {
	case "fs/read_text_file":
		var p struct {
			Path  string `json:"path"`
			Line  int    `json:"line"`
			Limit int    `json:"limit"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, bad(err), true
		}
		content, rerr := cl.readFile(ctx, p.Path, p.Line, p.Limit)
		if rerr != nil {
			return nil, rerr, true
		}
		return map[string]string{"content": content}, nil, true
	case "fs/write_text_file":
		var p struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, bad(err), true
		}
		if rerr := cl.writeFile(ctx, p.Path, p.Content); rerr != nil {
			return nil, rerr, true
		}
		return empty, nil, true
	case "session/request_permission":
		var p struct {
			ToolCall json.RawMessage `json:"toolCall"`
			Options  []struct {
				OptionID string `json:"optionId"`
				Kind     string `json:"kind"`
			} `json:"options"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, bad(err), true
		}
		var opts []permOption
		for _, o := range p.Options {
			opts = append(opts, permOption{ID: o.OptionID, Kind: o.Kind})
		}
		id, ok := cl.permission(ctx, v1Tool(p.ToolCall), opts)
		if !ok {
			return map[string]any{"outcome": map[string]string{"outcome": "cancelled"}}, nil, true
		}
		return map[string]any{"outcome": map[string]string{"outcome": "selected", "optionId": id}}, nil, true
	case "terminal/create":
		var p struct {
			Command         string          `json:"command"`
			Args            []string        `json:"args"`
			Env             []v1EnvVariable `json:"env"`
			Cwd             string          `json:"cwd"`
			OutputByteLimit int             `json:"outputByteLimit"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, bad(err), true
		}
		env := map[string]string{}
		for _, e := range p.Env {
			env[e.Name] = e.Value
		}
		id, rerr := cl.createTerminal(ctx, p.Command, p.Args, env, p.Cwd, p.OutputByteLimit)
		if rerr != nil {
			return nil, rerr, true
		}
		return map[string]string{"terminalId": id}, nil, true
	case "terminal/output", "terminal/wait_for_exit", "terminal/kill", "terminal/release":
		var p struct {
			TerminalID string `json:"terminalId"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, bad(err), true
		}
		switch method {
		case "terminal/output":
			out, truncated, st, rerr := cl.terminalOutput(p.TerminalID)
			if rerr != nil {
				return nil, rerr, true
			}
			res := map[string]any{"output": out, "truncated": truncated}
			if st != nil {
				res["exitStatus"] = v1Exit(*st)
			}
			return res, nil, true
		case "terminal/wait_for_exit":
			st, rerr := cl.waitTerminal(ctx, p.TerminalID)
			if rerr != nil {
				return nil, rerr, true
			}
			return v1Exit(st), nil, true
		case "terminal/kill":
			if rerr := cl.killTerminal(p.TerminalID); rerr != nil {
				return nil, rerr, true
			}
		default:
			if rerr := cl.releaseTerminal(p.TerminalID); rerr != nil {
				return nil, rerr, true
			}
		}
		return empty, nil, true
	}
	return nil, nil, false
}

func v1Exit(st exitStatus) map[string]any {
	return map[string]any{"exitCode": st.Code, "signal": st.Signal}
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
