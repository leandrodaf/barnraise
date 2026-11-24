// Package ports declares the small interfaces the application uses (out)
// and exposes (in). Adapters implement them; the domain never sees them.
package ports

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// Clock lets tests control time.
type Clock interface {
	Now() time.Time
}

// IDGen creates unique event ids.
type IDGen interface {
	NewID() string
}

// EventStore is the append-only log plus local queries.
type EventStore interface {
	// Append stores events, assigning Seq; events whose id is already stored are skipped.
	Append(ctx context.Context, events ...domain.Event) ([]domain.Event, error)
	Load(ctx context.Context, project string, afterSeq int64) ([]domain.Event, error)
	Query(ctx context.Context, q EventQuery) ([]domain.Event, error)
	Close() error
}

// EventQuery filters the log.
type EventQuery struct {
	Project string
	Subject string
	Types   []string
	Since   time.Time
	Limit   int
}

// EventBus carries events between processes and machines.
type EventBus interface {
	Publish(ctx context.Context, e domain.Event) error
	// Subscribe delivers events of a project (all projects if empty) until ctx ends.
	Subscribe(ctx context.Context, project string, fn func(domain.Event)) error
}

// Arbiter errors.
var (
	ErrTaken     = errors.New("already held by another machine")
	ErrLost      = errors.New("lease lost")
	ErrNotHeld   = errors.New("lease not held")
	ErrStaleMain = errors.New("main moved; rebase and check again")
	ErrOffline   = errors.New("arbiter unreachable")
)

// Lease is a claim on an item or a global duty.
type Lease struct {
	Key     string    `json:"key"`
	Holder  string    `json:"holder"`
	Version string    `json:"version"` // opaque compare-and-swap token (ref sha)
	Renewed time.Time `json:"renewed,omitzero"`
}

// LeaseInfo is what anyone can see about a lease.
type LeaseInfo struct {
	Held           bool
	Holder         string
	Version        string
	Renewed        time.Time // as written by the holder
	UnchangedSince time.Time // local clock: when this machine first saw this version
}

// Arbiter decides the few things only one machine may do: claim, renew,
// release and integrate. It lives in the git server by atomic operations.
type Arbiter interface {
	Claim(ctx context.Context, key, holder string) (Lease, error)
	Renew(ctx context.Context, l Lease) (Lease, error)
	Release(ctx context.Context, l Lease) error
	Inspect(ctx context.Context, key string) (LeaseInfo, error)
	// TakeOver replaces a lease nobody renewed for longer than ttl.
	TakeOver(ctx context.Context, key, holder string, ttl time.Duration) (Lease, error)
	// Next allocates the next number of a counter (item ids) atomically.
	Next(ctx context.Context, counter string) (int, error)
	// Integrate moves main from expected to next only by fast-forward.
	Integrate(ctx context.Context, expectedMain, next string) error
	List(ctx context.Context, prefix string) ([]string, error)
}

// Git is the repository: worktrees, branches, commits and history.
type Git interface {
	Root() string
	Fetch(ctx context.Context) error
	MainHead(ctx context.Context) (string, error)
	EnsureBranch(ctx context.Context, branch, from string) error
	AddWorktree(ctx context.Context, dir, branch string) error
	// AddDetachedWorktree checks out rev in dir without a branch (integration, reproduction).
	AddDetachedWorktree(ctx context.Context, dir, rev string) error
	// PushRev sets branch to rev locally and on the remote (forced: item branches are rebased).
	PushRev(ctx context.Context, rev, branch string) error
	// ResetBranch points branch at rev and checks it out in dir (discarding local changes).
	ResetBranch(ctx context.Context, dir, branch, rev string) error
	// Checkout moves a worktree to rev (detached).
	Checkout(ctx context.Context, dir, rev string) error
	RemoveWorktree(ctx context.Context, dir string) error
	CommitAll(ctx context.Context, dir, message string) (sha string, changed bool, err error)
	Push(ctx context.Context, dir, branch string) error
	FetchBranch(ctx context.Context, branch string) (bool, error)
	Rebase(ctx context.Context, dir, onto string) error
	Head(ctx context.Context, dir string) (string, error)
	ChangedFiles(ctx context.Context, from, to string) ([]string, error)
	Diff(ctx context.Context, from, to string, max int) (string, error)
	ExistsAt(ctx context.Context, rev, path string) bool
	Show(ctx context.Context, rev, path string) ([]byte, error)
	// SquashOnto builds one commit with the tree of branch on top of base and returns its sha.
	SquashOnto(ctx context.Context, dir, base, message string, exclude ...string) (string, error)
	MergeBase(ctx context.Context, a, b string) (string, error)
	Log(ctx context.Context, args ...string) ([]Commit, error)
	Blame(ctx context.Context, file string, line int) (string, error)
	DeleteBranch(ctx context.Context, branch string) error
}

// Commit is one log entry.
type Commit struct {
	SHA      string            `json:"sha"`
	Subject  string            `json:"subject"`
	Body     string            `json:"body"`
	Author   string            `json:"author"`
	Time     time.Time         `json:"time,omitzero"`
	Files    []string          `json:"files,omitempty"`
	Trailers map[string]string `json:"trailers,omitempty"`
}

// ErrConflict is a rebase conflict.
var ErrConflict = errors.New("rebase conflict")

// Capabilities a provider adapter declares; the core asks for capabilities, never commands.
type Capabilities struct {
	Version      string `json:"version"`
	Headless     bool   `json:"headless"`
	Stream       bool   `json:"stream"`
	Resume       bool   `json:"resume"`
	Fork         bool   `json:"fork"`
	Hooks        bool   `json:"hooks"`
	HookBlock    bool   `json:"hook_block"`
	Subagents    bool   `json:"subagents"`
	Skills       bool   `json:"skills"`
	MCP          bool   `json:"mcp"`
	Sandbox      bool   `json:"sandbox"`
	Messaging    bool   `json:"messaging"`
	SessionState bool   `json:"session_state"`
	UsageLimits  bool   `json:"usage_limits"`
	ModelSwitch  bool   `json:"model_switch"`
	JSONSchema   bool   `json:"json_schema"`
	Images       bool   `json:"images"`
}

// ContainerSpec runs a session inside a container: the agent sees only the
// mounted paths, so it can work without asking for permission.
type ContainerSpec struct {
	Engine  string   `json:"engine"`            // docker or podman
	Image   string   `json:"image"`             // has the AI CLI and the project's toolchain
	Network string   `json:"network,omitempty"` // engine network; "" is the engine's default
	User    string   `json:"user,omitempty"`    // uid:gid inside the container
	Home    string   `json:"home,omitempty"`    // HOME inside the container (a mounted, writable path)
	Mounts  []string `json:"mounts"`            // host paths mounted at the same path; "path:ro" is read-only
	Extra   []string `json:"extra,omitempty"`   // more engine arguments (GPU devices)
}

// SessionSpec opens an agent session.
type SessionSpec struct {
	Agent        string            `json:"agent"`
	Role         string            `json:"role"`
	Account      domain.Account    `json:"account"`
	Model        string            `json:"model"`
	Effort       string            `json:"effort,omitempty"`
	Workdir      string            `json:"workdir"`
	Prompt       string            `json:"prompt"`
	SystemPrompt string            `json:"system_prompt,omitempty"`
	Env          map[string]string `json:"env,omitempty"`
	MCPConfig    string            `json:"mcp_config,omitempty"`
	SettingsFile string            `json:"settings_file,omitempty"`
	ResumeID     string            `json:"resume_id,omitempty"`
	MaxTurns     int               `json:"max_turns,omitempty"`
	JSONSchema   string            `json:"json_schema,omitempty"`
	Timeout      time.Duration     `json:"timeout,omitempty"`
	RunAsUser    string            `json:"run_as_user,omitempty"`
	// Container, when set, runs the session's CLI inside a container.
	Container *ContainerSpec `json:"container,omitempty"`
	// Endpoint, when set, points the CLI at a key-based provider instead
	// of the account's subscription login. The key itself travels only in
	// Env, under EndpointKeyEnv.
	Endpoint *Endpoint `json:"endpoint,omitempty"`
	// ACP, when set, is the agent program of a provider of `type: acp`.
	ACP *ACPAgent `json:"acp,omitempty"`
	// Confine, when set, runs the session's CLI confined by Barnraise
	// (Landlock or a microVM); never together with Container.
	Confine *Confine `json:"confine,omitempty"`
}

// EndpointKeyEnv is the environment variable that carries the API key of
// a session on a key-based provider. The CLI is told this name, never the
// value: the key is not on any command line and not in any file.
const EndpointKeyEnv = "BARNRAISE_PROVIDER_KEY"

// Endpoint is a key-based provider (OpenRouter, or another endpoint that
// speaks the OpenAI Responses API).
type Endpoint struct {
	Name    string `json:"name"`     // provider name: openrouter
	BaseURL string `json:"base_url"` // https://openrouter.ai/api/v1
}

// ProviderEventKind is the normalized event kind.
type ProviderEventKind string

const (
	PEInit       ProviderEventKind = "init"
	PEMessage    ProviderEventKind = "message"
	PEToolUse    ProviderEventKind = "tool_use"
	PEToolResult ProviderEventKind = "tool_result"
	PEUsage      ProviderEventKind = "usage"
	PELimit      ProviderEventKind = "limit"
	PEPermission ProviderEventKind = "permission"
	PEDone       ProviderEventKind = "done"
	PEError      ProviderEventKind = "error"
)

// ProviderEvent is normalized; the original rides along untouched.
type ProviderEvent struct {
	Kind      ProviderEventKind  `json:"kind"`
	SessionID string             `json:"session_id,omitempty"`
	Text      string             `json:"text,omitempty"`
	Tool      string             `json:"tool,omitempty"`
	Input     json.RawMessage    `json:"input,omitempty"`
	Usage     *domain.TokenUsage `json:"usage,omitempty"`
	// Limits is the account's own usage of its windows (5 hours, week), when
	// the provider reports it during the session.
	Limits   *domain.Usage   `json:"limits,omitempty"`
	IsError  bool            `json:"is_error,omitempty"`
	ResetsAt time.Time       `json:"resets_at,omitzero"`
	Raw      json.RawMessage `json:"raw,omitempty"`
}

// SessionResult is what a finished session reports.
type SessionResult struct {
	SessionID string            `json:"session_id"`
	Text      string            `json:"text"`
	Usage     domain.TokenUsage `json:"usage"`
	ExitCode  int               `json:"exit_code"`
	Limited   bool              `json:"limited"`
	ResetsAt  time.Time         `json:"resets_at,omitzero"`
	Duration  time.Duration     `json:"duration"`
}

// Session is a running agent session.
type Session interface {
	Events() <-chan ProviderEvent
	Wait() (SessionResult, error)
	Stop() error
}

// Provider is an AI tool adapter (Claude Code, Codex, …).
type Provider interface {
	Name() string
	Family() string
	Detect(ctx context.Context) (Capabilities, error)
	Open(ctx context.Context, spec SessionSpec) (Session, error)
}

// TrackerItem is an item as a tracker shows it.
type TrackerItem struct {
	Ref     string    `json:"ref"`
	Number  int       `json:"number,omitempty"`
	Title   string    `json:"title"`
	Body    string    `json:"body"`
	Status  string    `json:"status"`
	Labels  []string  `json:"labels,omitempty"`
	Author  string    `json:"author,omitempty"`
	URL     string    `json:"url,omitempty"`
	Updated time.Time `json:"updated,omitzero"`
	Closed  bool      `json:"closed,omitempty"`
}

// Tracker is where people see the items.
type Tracker interface {
	Name() string
	List(ctx context.Context, since time.Time) ([]TrackerItem, error)
	Create(ctx context.Context, it *domain.Item, lang string) (TrackerItem, error)
	Update(ctx context.Context, it *domain.Item, lang string) error
	Comment(ctx context.Context, ref, text string) error
	Columns(ctx context.Context) ([]string, error)
}

// PR is a pull or merge request.
type PR struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
	State  string `json:"state"`
	Draft  bool   `json:"draft"`
	Head   string `json:"head"`
}

// CodeHost is the git server: PRs, reviews, checks, workflows.
type CodeHost interface {
	Name() string
	OpenDraftPR(ctx context.Context, branch, base, title, body string) (PR, error)
	UpdatePR(ctx context.Context, pr PR, title, body string) error
	MarkReady(ctx context.Context, pr PR) error
	Comment(ctx context.Context, pr PR, body string) error
	Review(ctx context.Context, pr PR, approve bool, body string) error
	ReportCheck(ctx context.Context, sha, name string, passed bool, summary string) error
	Merge(ctx context.Context, pr PR, method string) (string, error)
	Close(ctx context.Context, pr PR, reason string) error
	DispatchWorkflow(ctx context.Context, workflow, ref string, inputs map[string]string) error
}

// CheckSpec is one check to run for an item.
type CheckSpec struct {
	Def     domain.CheckDef   `json:"def"`
	ItemID  string            `json:"item_id"`
	Branch  string            `json:"branch"`
	Commit  string            `json:"commit"`
	Dir     string            `json:"dir"`
	Touched []string          `json:"touched,omitempty"`
	Areas   []string          `json:"areas,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Main    bool              `json:"main,omitempty"` // run against main for flaky comparison
	// Base is the commit the change is measured against (where the branch
	// forked from main); empty when unknown.
	Base string `json:"base,omitempty"`
}

// CheckRunner runs a check, or dispatches it and reports pending.
type CheckRunner interface {
	Run(ctx context.Context, spec CheckSpec) (res domain.CheckResult, pending bool, err error)
}

// BlobStore keeps logs, prints, diffs by content hash.
type BlobStore interface {
	Put(ctx context.Context, r io.Reader) (hash string, err error)
	Get(ctx context.Context, hash string) (io.ReadCloser, error)
	Path(hash string) string
}

// Notification goes to the owner.
type Notification struct {
	Title   string   `json:"title"`
	Body    string   `json:"body"`
	Urgent  bool     `json:"urgent"`
	Link    string   `json:"link,omitempty"`
	Options []string `json:"options,omitempty"`
	Kind    string   `json:"kind"` // question, alert, digest
}

// Notifier sends owner notifications.
type Notifier interface {
	Notify(ctx context.Context, n Notification) error
}

// Secrets is the system keychain.
type Secrets interface {
	Get(key string) (string, error)
	Set(key, value string) error
	Delete(key string) error
}

// ErrSecretNotFound is returned when a key is missing.
var ErrSecretNotFound = errors.New("secret not found")

// Cmd is a process to run.
type Cmd struct {
	Name    string
	Args    []string
	Dir     string
	Env     []string
	Stdin   io.Reader
	Timeout time.Duration
}

// ExecResult is a finished process.
type ExecResult struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
	Duration time.Duration
}

// Exec runs processes.
type Exec interface {
	Run(ctx context.Context, c Cmd) (ExecResult, error)
}

// ErrCloneUnsupported says the filesystem cannot clone files copy-on-write
// (and a full copy was not asked for).
var ErrCloneUnsupported = errors.New("copy-on-write clone not supported here")

// TreeCloner copies a directory tree copy-on-write: a reflink on Linux
// (FICLONE), clonefile on macOS. dst must not exist. When the filesystem
// cannot clone, it copies the files in full if fullCopy is set, else it
// leaves nothing at dst and returns ErrCloneUnsupported.
type TreeCloner interface {
	CloneTree(ctx context.Context, src, dst string, fullCopy bool) error
}

// Triage asks a cheap model for verdicts on candidates.
type Triage interface {
	Triage(ctx context.Context, change domain.Change, items []*domain.Item) ([]domain.TriageResult, domain.TokenUsage, error)
}

// Knowledge indexes .barnraise files, the code map and items for search.
type Knowledge interface {
	Reindex(ctx context.Context, project string, root string, commit string, changed []string) error
	IndexItems(ctx context.Context, project string, items []*domain.Item, proposals []*domain.Proposal) error
	Search(ctx context.Context, project, query string, filters SearchFilters, limit int) ([]SearchHit, error)
	Get(ctx context.Context, project, id string) (*Doc, error)
	Graph() domain.CallGraph
	Links(ctx context.Context, project, id string) ([]Link, error)
	Symbols(ctx context.Context, project, file string) ([]Symbol, error)
}

// SearchFilters narrows a search.
type SearchFilters struct {
	Kind   string
	Area   string
	Status string
	Goal   string
	Since  time.Time
}

// SearchHit is a short result; the full content comes with Get.
type SearchHit struct {
	ID      string    `json:"id"`
	Kind    string    `json:"kind"`
	Title   string    `json:"title"`
	Summary string    `json:"summary"`
	Area    string    `json:"area,omitempty"`
	Path    string    `json:"path,omitempty"`
	Date    time.Time `json:"date,omitzero"`
	Tokens  int       `json:"tokens"`
	Score   float64   `json:"score"`
	Layer   string    `json:"layer"` // exact, filter, text, vector
}

// Doc is a knowledge document.
type Doc struct {
	ID        string            `json:"id"`
	Kind      string            `json:"kind"`
	Title     string            `json:"title"`
	Body      string            `json:"body"`
	Path      string            `json:"path,omitempty"`
	Status    string            `json:"status,omitempty"`
	Areas     []string          `json:"areas,omitempty"`
	Links     []string          `json:"links,omitempty"`
	Meta      map[string]string `json:"meta,omitempty"`
	Updated   time.Time         `json:"updated,omitzero"`
	ReviewBy  time.Time         `json:"review_by,omitzero"`
	SourceSHA string            `json:"source_sha,omitempty"`
}

// Link is an edge of the knowledge graph.
type Link struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind"`
}

// Symbol is an entry of the code map.
type Symbol struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	File      string `json:"file"`
	Line      int    `json:"line"`
	Signature string `json:"signature"`
}
