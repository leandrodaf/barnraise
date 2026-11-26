// Package claudechat runs the owner's conversation session: one interactive
// Claude Code session kept in a pseudo-terminal, with Remote Control on, so
// the panel, the command line and the Claude app on a phone all talk to the
// same conversation. Messages go in as a paste and Enter; what was said is
// read back from the transcript Claude Code writes, which also holds what was
// typed on the phone.
package claudechat

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"

	"github.com/leandrodaf/barnraise/internal/ports"
)

// Session is the one conversation session of this machine.
type Session struct {
	// Bin is the claude command (default "claude" on the PATH).
	Bin string

	mu      sync.Mutex
	cmd     *exec.Cmd
	tty     *os.File
	spec    ports.ChatSpec
	started time.Time
	url     string
	err     string
	busyAt  time.Time
	screen  []byte
	trusted bool
	ready   bool
	done    chan struct{}
}

var _ ports.ChatSession = (*Session)(nil)

// New returns a session using the claude command on the PATH.
func New() *Session { return &Session{} }

// screenKeep is how much of the terminal output is kept to read prompts and
// the Remote Control address from.
const screenKeep = 64 << 10

// busyFor is how long after the last "esc to interrupt" the session still
// counts as busy: the line redraws every fraction of a second while a turn
// runs.
const busyFor = 3 * time.Second

var (
	ansiRe      = regexp.MustCompile(`\x1b\[[0-9;?<>=]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b[()][A-Z0-9]|\x1b[=>78]`)
	remoteURLRe = regexp.MustCompile(`https://claude\.ai/code/session_[A-Za-z0-9]+`)
	spaceRe     = regexp.MustCompile(`\s+`)
)

func (s *Session) bin() string {
	if s.Bin != "" {
		return s.Bin
	}
	return "claude"
}

// args builds the command line of a session.
func args(spec ports.ChatSpec) []string {
	a := []string{}
	if spec.Resume {
		a = append(a, "--resume", spec.SessionID)
	} else {
		a = append(a, "--session-id", spec.SessionID)
	}
	a = append(a, "--remote-control", spec.Name, "--permission-mode", "dontAsk")
	if spec.SettingsFile != "" {
		a = append(a, "--settings", spec.SettingsFile)
	}
	if spec.SystemPrompt != "" {
		a = append(a, "--append-system-prompt", spec.SystemPrompt)
	}
	if spec.Model != "" {
		a = append(a, "--model", spec.Model)
	}
	return a
}

// environ is the daemon's environment without what marks a process as a
// child of another Claude Code session: with it, the session would not save
// its transcript, and the panel would see nothing.
func environ(extra []string) []string {
	var out []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "CLAUDE_CODE_") || k == "CLAUDECODE" {
			continue
		}
		out = append(out, kv)
	}
	return append(append(out, "TERM=xterm-256color"), extra...)
}

// Start runs the session in a pseudo-terminal, or does nothing when it runs.
func (s *Session) Start(spec ports.ChatSpec) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd != nil {
		return s.spec.SessionID, nil
	}
	path, err := exec.LookPath(s.bin())
	if err != nil {
		return "", fmt.Errorf("claudechat: %s is not installed: %w", s.bin(), err)
	}
	if spec.Resume {
		// A session that never got a message wrote no transcript, and
		// Claude Code refuses to resume it: it starts under the same id.
		if _, err := os.Stat(TranscriptPath(spec.ConfigDir, spec.Dir, spec.SessionID)); err != nil {
			spec.Resume = false
		}
	}
	cmd := exec.Command(path, args(spec)...)
	cmd.Dir = spec.Dir
	cmd.Env = environ(spec.Env)
	if spec.ConfigDir != "" {
		cmd.Env = append(cmd.Env, "CLAUDE_CONFIG_DIR="+spec.ConfigDir)
	}
	tty, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 50, Cols: 160})
	if err != nil {
		return "", fmt.Errorf("claudechat: start: %w", err)
	}
	s.cmd, s.tty, s.spec = cmd, tty, spec
	s.started, s.url, s.err, s.screen, s.trusted, s.ready = time.Now(), "", "", nil, false, false
	s.done = make(chan struct{})
	go s.read(tty)
	go s.wait(cmd, s.done)
	return spec.SessionID, nil
}

// read keeps the terminal's recent output, answers the folder-trust prompt
// for the session's own folder and finds the Remote Control address.
func (s *Session) read(tty *os.File) {
	buf := make([]byte, 32<<10)
	for {
		n, err := tty.Read(buf)
		if n > 0 {
			s.saw(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

func (s *Session) saw(chunk []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.screen = append(s.screen, chunk...)
	if len(s.screen) > screenKeep {
		s.screen = s.screen[len(s.screen)-screenKeep:]
	}
	text := ansiRe.ReplaceAllString(string(s.screen), "")
	if u := remoteURLRe.FindAllString(text, -1); len(u) > 0 {
		s.url = u[len(u)-1]
	}
	squashed := spaceRe.ReplaceAllString(text, "")
	// The input box is up once its mode line shows.
	if !s.ready && (strings.Contains(squashed, "shift+tabtocycle") || s.url != "") {
		s.ready = true
	}
	if strings.Contains(spaceRe.ReplaceAllString(ansiRe.ReplaceAllString(string(chunk), ""), ""), "esctointerrupt") {
		s.busyAt = time.Now()
	}
	if !s.trusted && strings.Contains(squashed, "Itrustthisfolder") {
		// The session's folder is Barnraise's own: the default answer is
		// "No, exit", so move to "Yes" and confirm.
		s.trusted = true
		tty := s.tty
		go func() {
			time.Sleep(400 * time.Millisecond)
			_, _ = tty.Write([]byte("\x1b[B"))
			time.Sleep(300 * time.Millisecond)
			_, _ = tty.Write([]byte("\r"))
		}()
	}
}

func (s *Session) wait(cmd *exec.Cmd, done chan struct{}) {
	err := cmd.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd == cmd {
		if s.tty != nil {
			_ = s.tty.Close()
		}
		s.cmd, s.tty = nil, nil
		if err != nil {
			s.err = "the session ended: " + err.Error() + lastLine(s.screen)
		} else {
			s.err = "the session ended" + lastLine(s.screen)
		}
	}
	close(done)
}

// lastLine is the end of the terminal output, for an error message.
func lastLine(screen []byte) string {
	t := strings.TrimSpace(spaceRe.ReplaceAllString(ansiRe.ReplaceAllString(string(screen), ""), " "))
	if t == "" {
		return ""
	}
	if r := []rune(t); len(r) > 300 {
		t = string(r[len(r)-300:])
	}
	return ": " + t
}

// readyWait bounds how long a message waits for a session just started:
// typed before its input box exists, it would be lost.
const readyWait = 40 * time.Second

// waitReady waits until the session's input box is up, or until d passes.
func (s *Session) waitReady(d time.Duration) {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		ready := s.ready || s.cmd == nil
		s.mu.Unlock()
		if ready {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// ErrNotRunning: the session is not running.
var ErrNotRunning = errors.New("claudechat: the conversation session is not running")

// Send pastes text into the session and presses Enter. A paste keeps line
// breaks as part of the message instead of sending each line.
func (s *Session) Send(text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return errors.New("claudechat: empty message")
	}
	s.mu.Lock()
	tty := s.tty
	s.mu.Unlock()
	if tty == nil {
		return ErrNotRunning
	}
	text = strings.ReplaceAll(text, "\x1b", "")
	s.waitReady(readyWait)
	if _, err := io.WriteString(tty, "\x1b[200~"+text+"\x1b[201~"); err != nil {
		return s.ended(err)
	}
	time.Sleep(250 * time.Millisecond)
	if _, err := io.WriteString(tty, "\r"); err != nil {
		return s.ended(err)
	}
	return nil
}

// ended explains a write that failed because the session ended meanwhile.
func (s *Session) ended(err error) error {
	time.Sleep(100 * time.Millisecond)
	if st := s.Status(); !st.Running && st.Err != "" {
		return errors.New(st.Err)
	}
	return err
}

// Interrupt presses Esc: the turn under way stops, the session stays.
func (s *Session) Interrupt() error {
	s.mu.Lock()
	tty := s.tty
	s.mu.Unlock()
	if tty == nil {
		return ErrNotRunning
	}
	_, err := tty.Write([]byte("\x1b"))
	return err
}

// Stop ends the session; its transcript stays for the next Start to resume.
func (s *Session) Stop() error {
	s.mu.Lock()
	cmd, done := s.cmd, s.done
	s.mu.Unlock()
	if cmd == nil {
		return nil
	}
	_ = cmd.Process.Signal(os.Interrupt)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		<-done
	}
	s.mu.Lock()
	s.err = ""
	s.mu.Unlock()
	return nil
}

// Status says whether the session runs, whether a turn is under way and
// where Remote Control opens it.
func (s *Session) Status() ports.ChatStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := ports.ChatStatus{Running: s.cmd != nil, Err: s.err, Provider: "claude"}
	if st.Running {
		st.StartedAt, st.RemoteURL = s.started, s.url
		st.Busy = time.Since(s.busyAt) < busyFor
	}
	return st
}
