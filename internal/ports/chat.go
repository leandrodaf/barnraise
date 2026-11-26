package ports

import "time"

// ChatSpec starts the owner's conversation session: one interactive session
// of a provider's CLI, kept running without a window, that the panel, the
// command line and the provider's own remote control (a phone) all talk to.
type ChatSpec struct {
	// SessionID names the conversation; the same id resumes it.
	SessionID string
	// Resume continues SessionID instead of starting it.
	Resume bool
	// Dir is the working folder (the project roots it may read are in the
	// settings file).
	Dir string
	// Name is how the session shows on the provider's remote control.
	Name string
	// SystemPrompt is added to the provider's own.
	SystemPrompt string
	// SettingsFile holds the session's permissions.
	SettingsFile string
	// ConfigDir is the login folder of the account the session runs as
	// ("" for the CLI's own login): the phone shows the session in the app
	// signed in to that account.
	ConfigDir string
	// Model is the model to run ("" for the account's default).
	Model string
	// Env is added to the session's environment.
	Env []string
}

// ChatStatus is what the session is doing.
type ChatStatus struct {
	Running bool `json:"running"`
	// Busy: a turn is under way (the model thinks or a tool runs).
	Busy bool `json:"busy"`
	// Provider runs the session: "claude" or "codex".
	Provider string `json:"provider,omitempty"`
	// RemoteURL opens the session on the provider's site and phone app
	// (Claude); Remote is how Remote Control stands (Codex: connected,
	// connecting, errored, disabled), the phone pairing once with a code.
	RemoteURL string    `json:"remote_url,omitempty"`
	Remote    string    `json:"remote,omitempty"`
	StartedAt time.Time `json:"started_at,omitzero"`
	// Draft is the answer being written, before it lands in the transcript
	// (providers that stream it).
	Draft string `json:"draft,omitempty"`
	// Err says why the session is not running, when it stopped on its own.
	Err string `json:"error,omitempty"`
}

// ChatEntry is one thing said or done in the conversation.
type ChatEntry struct {
	Seq int `json:"seq"`
	// Role is "user", "assistant" or "tool".
	Role string `json:"role"`
	Text string `json:"text,omitempty"`
	// Tool and Input describe a tool the assistant ran; Result is what it
	// gave back, cut short.
	Tool   string    `json:"tool,omitempty"`
	Input  string    `json:"input,omitempty"`
	Result string    `json:"result,omitempty"`
	Failed bool      `json:"failed,omitempty"`
	At     time.Time `json:"at,omitzero"`
}

// ChatSession runs the owner's one conversation session on this machine.
type ChatSession interface {
	// Start runs the session (or does nothing when it already runs) and
	// returns the id it runs under: a provider that names its own sessions
	// may give a new one.
	Start(spec ChatSpec) (string, error)
	// Send types a message into the session.
	Send(text string) error
	// Interrupt stops the turn under way, as Esc does.
	Interrupt() error
	// Stop ends the session; its conversation stays and resumes later.
	Stop() error
	Status() ChatStatus
	// Transcript reads the conversation sessionID holds under the account's
	// login folder (configDir), from entry after on.
	Transcript(configDir, sessionID, dir string, after int) ([]ChatEntry, error)
}

// ChatPairing pairs a phone with the provider's remote control: the app
// scans Code (a QR code) or the owner types Manual, before Expires.
type ChatPairing struct {
	Code    string    `json:"code"`
	Manual  string    `json:"manual"`
	Expires time.Time `json:"expires"`
}

// ChatPairer is a session whose phone app pairs with a code (Codex).
type ChatPairer interface {
	Pair(configDir string) (ChatPairing, error)
}
