package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// The version handling of the protocol lives here and in one file per
// version (v1.go). The session and the client speak the normalized types
// below; a dialect turns them into one version's messages and back. A new
// major version of ACP is a new dialect registered in dialects, nothing else.

// dialect is one major version of the Agent Client Protocol.
type dialect interface {
	// Version is the protocolVersion number of the dialect.
	Version() int
	// Initialize is the first request: method and params.
	Initialize(c clientInfo) (string, any)
	// ParseInitialize reads the agent's answer.
	ParseInitialize(raw json.RawMessage) (agentInfo, error)
	// NewSession opens a session in cwd with the MCP servers given.
	NewSession(cwd string, mcp []mcpServer, a agentInfo) (string, any)
	// LoadSession resumes a session the agent stored (when it can).
	LoadSession(id, cwd string, mcp []mcpServer, a agentInfo) (string, any)
	// ParseNewSession reads the session id and what can be chosen in it.
	ParseNewSession(raw json.RawMessage) (sessionInfo, error)
	// SelectModel picks a model in the session; ok is false when the
	// agent offers no way to pick that one.
	SelectModel(s sessionInfo, model string) (method string, params any, ok bool)
	// Prompt sends one prompt turn made of text blocks.
	Prompt(sessionID string, texts []string) (string, any)
	// ParsePrompt reads the end of a prompt turn.
	ParsePrompt(raw json.RawMessage) (turnEnd, error)
	// Cancel is the notification that stops the running prompt turn.
	Cancel(sessionID string) (string, any)
	// ParseUpdate reads a notification of the agent into session updates.
	ParseUpdate(method string, raw json.RawMessage) []update
	// Serve decodes a request of the agent, runs it on the client and
	// encodes the answer. handled is false for a method it does not know.
	Serve(ctx context.Context, cl *client, method string, raw json.RawMessage) (res any, rerr *RPCError, handled bool)
}

// dialects are the versions Barnraise speaks, newest first.
var dialects = []dialect{v1{}}

// latest is the version Barnraise proposes in initialize.
func latest() dialect { return dialects[0] }

// dialectFor picks the dialect of the version the agent answered with.
func dialectFor(version int) (dialect, error) {
	for _, d := range dialects {
		if d.Version() == version {
			return d, nil
		}
	}
	var known []string
	for _, d := range dialects {
		known = append(known, fmt.Sprint(d.Version()))
	}
	return nil, fmt.Errorf("acp: the agent speaks protocol version %d; Barnraise speaks %s", version, strings.Join(known, ", "))
}

// clientInfo is what Barnraise offers the agent.
type clientInfo struct {
	Name, Version string
	ReadFiles     bool
	WriteFiles    bool
	Terminals     bool
}

// agentInfo is what the agent said about itself.
type agentInfo struct {
	Version     int
	Name        string
	AgentVer    string
	LoadSession bool
	MCPHTTP     bool
	AuthMethods []string
}

// sessionInfo is an open session and what can be chosen in it.
type sessionInfo struct {
	ID     string
	Models []string // models the agent lists (session/set_model)
	// ModelOption is the configuration option that picks the model
	// (session/set_config_option), with its values.
	ModelOption       string
	ModelOptionValues []string
}

// turnEnd is how a prompt turn ended.
type turnEnd struct {
	StopReason string
	Usage      *domain.TokenUsage // only when the agent reports it
}

// mcpServer is an MCP server the agent should connect to.
type mcpServer struct {
	Name    string
	Command string
	Args    []string
	Env     map[string]string
	URL     string // http server, when set
	Headers map[string]string
}

// updateKind is a normalized session update.
type updateKind int

const (
	updMessage updateKind = iota + 1
	updThought
	updToolCall
	updToolUpdate
	updUsage
	updOther
)

// update is one normalized session update.
type update struct {
	Kind      updateKind
	SessionID string
	Text      string
	MessageID string
	Tool      toolCall
	CostUSD   float64
	HasCost   bool
	Raw       json.RawMessage
}

// toolCall is a tool call as the agent reports it or asks permission for.
type toolCall struct {
	ID       string
	Title    string
	Kind     string // read, edit, delete, move, search, execute, think, fetch, switch_mode, other
	Status   string // pending, in_progress, completed, failed
	Paths    []string
	RawInput json.RawMessage
	Output   string
}

// merge applies the fields an update carries.
func (t *toolCall) merge(u toolCall) {
	if u.Title != "" {
		t.Title = u.Title
	}
	if u.Kind != "" {
		t.Kind = u.Kind
	}
	if u.Status != "" {
		t.Status = u.Status
	}
	for _, p := range u.Paths {
		if !slices.Contains(t.Paths, p) {
			t.Paths = append(t.Paths, p)
		}
	}
	if len(u.RawInput) > 0 {
		t.RawInput = u.RawInput
	}
	if u.Output != "" {
		t.Output = u.Output
	}
}

// permOption is one answer the agent offers to a permission request.
type permOption struct {
	ID   string
	Kind string // allow_once, allow_always, reject_once, reject_always
}

// exitStatus of a terminal.
type exitStatus struct {
	Code   *int
	Signal *string
}
