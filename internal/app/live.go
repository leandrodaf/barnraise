package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// The live view of a running session (ADR-0048). While somebody watches,
// what the agent writes and the tools it calls reach the panel as short
// lines; nobody watching costs one append to a small ring per event.
// Nothing of it is stored in the event log or sent to other machines
// unless a watcher there asked for it.

// liveRing is how many lines a session keeps for a new watcher.
const liveRing = 200

// liveTextMax bounds a line's text (a tool's input or result is cut).
const liveTextMax = 300

// liveSubBuffer is how many lines wait for a slow watcher before the
// newest are dropped for it.
const liveSubBuffer = 128

// LiveLine is one line of a running session, as the panel shows it.
type LiveLine struct {
	At time.Time `json:"at"`
	// Kind: text (what the agent writes), tool (a tool call), result (what
	// the tool answered), error, limit, end (the session ended).
	Kind  string `json:"kind"`
	Tool  string `json:"tool,omitempty"`
	Text  string `json:"text,omitempty"`
	Error bool   `json:"error,omitempty"`
}

// LiveSession says which session a watcher follows.
type LiveSession struct {
	Project  string    `json:"project"`
	ItemID   string    `json:"item_id"`
	Agent    string    `json:"agent"`
	Role     string    `json:"role"`
	Step     string    `json:"step"`
	Provider string    `json:"provider"`
	Model    string    `json:"model"`
	Machine  string    `json:"machine"`
	Since    time.Time `json:"since,omitzero"`
}

// LiveWatch is one watcher of a session: the lines it missed (up to the
// ring), then the new ones on Lines until the session ends (Lines is
// closed) or Stop is called.
type LiveWatch struct {
	Session LiveSession
	Backlog []LiveLine
	Lines   <-chan LiveLine
	Stop    func()
}

// ElsewhereError says the item's session runs on another machine that
// this daemon could not reach for its lines.
type ElsewhereError struct {
	Machine string
	Why     string
}

func (e *ElsewhereError) Error() string {
	msg := "the session runs on " + e.Machine
	if e.Why != "" {
		msg += ": " + e.Why
	}
	return msg
}

// LivePeer opens a watch of a session that runs on another machine (the
// composition root passes it over the cluster's connection).
type LivePeer func(ctx context.Context, machine, project, itemID, agent string) (*LiveWatch, error)

// liveFeed is the ring and the watchers of one session.
type liveFeed struct {
	mu     sync.Mutex
	ring   []LiveLine // circular once full
	next   int
	subs   map[int]chan LiveLine
	nextID int
	closed bool
}

func newLiveFeed() *liveFeed { return &liveFeed{ring: make([]LiveLine, 0, 16)} }

// publish keeps the line and hands it to the watchers, never waiting for one.
func (f *liveFeed) publish(l LiveLine) {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return
	}
	if len(f.ring) < liveRing {
		f.ring = append(f.ring, l)
	} else {
		f.ring[f.next] = l
		f.next = (f.next + 1) % liveRing
	}
	for _, ch := range f.subs {
		select {
		case ch <- l:
		default: // a slow watcher misses lines rather than slowing the session
		}
	}
}

// backlog returns the kept lines, oldest first. Callers hold f.mu.
func (f *liveFeed) backlog() []LiveLine {
	out := make([]LiveLine, 0, len(f.ring))
	out = append(out, f.ring[f.next:]...)
	return append(out, f.ring[:f.next]...)
}

// subscribe returns the kept lines and a channel of the next ones.
func (f *liveFeed) subscribe() ([]LiveLine, <-chan LiveLine, func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch := make(chan LiveLine, liveSubBuffer)
	back := f.backlog()
	if f.closed {
		close(ch)
		return back, ch, func() {}
	}
	if f.subs == nil {
		f.subs = map[int]chan LiveLine{}
	}
	id := f.nextID
	f.nextID++
	f.subs[id] = ch
	return back, ch, func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		if c, ok := f.subs[id]; ok {
			delete(f.subs, id)
			close(c)
		}
	}
}

// watchers counts the open watches.
func (f *liveFeed) watchers() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.subs)
}

// close ends the feed: every watcher gets the end line, then its channel closes.
func (f *liveFeed) close(at time.Time, why string) {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return
	}
	f.closed = true
	end := LiveLine{At: at, Kind: "end", Text: why}
	for id, ch := range f.subs {
		select {
		case ch <- end:
		default:
		}
		close(ch)
		delete(f.subs, id)
	}
}

// liveLineOf turns a provider event into a line; false for events a
// person does not read (usage, init, the end's summary).
func liveLineOf(ev ports.ProviderEvent, at time.Time) (LiveLine, bool) {
	l := LiveLine{At: at.UTC(), Tool: ev.Tool, Error: ev.IsError}
	switch ev.Kind {
	case ports.PEMessage:
		l.Kind, l.Text = "text", strings.TrimSpace(ev.Text)
		if l.Text == "" {
			return l, false
		}
	case ports.PEToolUse:
		l.Kind, l.Text = "tool", inputSummary(ev.Input)
	case ports.PEToolResult:
		l.Kind, l.Text = "result", strings.TrimSpace(ev.Text)
	case ports.PEError:
		l.Kind, l.Text, l.Error = "error", strings.TrimSpace(ev.Text), true
	case ports.PELimit:
		l.Kind, l.Text = "limit", strings.TrimSpace(ev.Text)
	case ports.PEPermission:
		l.Kind, l.Text, l.Error = "error", "blocked: "+ev.Tool, true
	default:
		return l, false
	}
	l.Text = clip(l.Text, liveTextMax)
	return l, true
}

// inputSummary is a tool's input in one short line: the arguments people
// read first (command, path, pattern), else the compact JSON.
func inputSummary(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return clip(strings.Join(strings.Fields(string(raw)), " "), liveTextMax)
	}
	for _, k := range []string{"command", "file_path", "path", "pattern", "url", "query", "description", "to", "text"} {
		if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
			return clip(k+": "+strings.Join(strings.Fields(v), " "), liveTextMax)
		}
	}
	b, _ := json.Marshal(m)
	return clip(string(b), liveTextMax)
}

// clip cuts s to n runes, marking the cut.
func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// SetLivePeer installs how a watch reaches another machine (nil: this machine only).
func (s *Service) SetLivePeer(f LivePeer) { s.livePeer = f }

// canWatch: only the owner and admin tokens follow a session; a session
// token never does, whatever its scopes.
func canWatch(a Actor, project string) error {
	if a.Kind == ActorAgent {
		return fmt.Errorf("%w: a session token cannot watch a session", ErrForbidden)
	}
	return a.Require(ScopeAdmin, project)
}

// WatchSession follows the session running on an item (agent narrows it
// when two run), on this machine or, through the peer, on the machine
// that runs it.
func (s *Service) WatchSession(ctx context.Context, a Actor, project, itemID, agent string) (*LiveWatch, error) {
	if err := canWatch(a, project); err != nil {
		return nil, err
	}
	p, err := s.Project(project)
	if err != nil {
		return nil, err
	}
	itemID = normItem(itemID)
	if w, ok := s.runtime.watchLocal(p.Name, itemID, agent); ok {
		return w, nil
	}
	machine := s.sessionMachine(p, itemID, agent)
	if machine == "" || machine == s.d.Machine {
		return nil, fmt.Errorf("%w: no session runs on %s now", ErrNotFound, itemID)
	}
	if s.livePeer == nil {
		return nil, &ElsewhereError{Machine: machine, Why: "this daemon has no connection to other machines"}
	}
	w, err := s.livePeer(ctx, machine, p.Name, itemID, agent)
	if err != nil {
		var ee *ElsewhereError
		if errors.As(err, &ee) || errors.Is(err, ErrNotFound) {
			return nil, err
		}
		return nil, &ElsewhereError{Machine: machine, Why: err.Error()}
	}
	return w, nil
}

// WatchLocalSession is WatchSession for this machine only: what a peer
// asks for its watcher. It never passes the watch on.
func (s *Service) WatchLocalSession(a Actor, project, itemID, agent string) (*LiveWatch, error) {
	if err := canWatch(a, project); err != nil {
		return nil, err
	}
	if w, ok := s.runtime.watchLocal(project, normItem(itemID), agent); ok {
		return w, nil
	}
	return nil, fmt.Errorf("%w: no session runs on %s on %s now", ErrNotFound, itemID, s.d.Machine)
}

// ItemMessages lists the messages about an item, oldest first, for the
// conversation next to the live view (the last max).
func (s *Service) ItemMessages(a Actor, project, itemID string, max int) ([]domain.Message, error) {
	if err := canWatch(a, project); err != nil {
		return nil, err
	}
	p, err := s.Project(project)
	if err != nil {
		return nil, err
	}
	itemID = normItem(itemID)
	p.mu.Lock()
	var out []domain.Message
	for _, m := range p.state.Messages {
		if sameItem(m.ItemID, itemID) || m.To == "item:"+itemID {
			out = append(out, *m)
		}
	}
	p.mu.Unlock()
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	if max > 0 && len(out) > max {
		out = out[len(out)-max:]
	}
	return out, nil
}

func normItem(id string) string {
	if n, err := domain.NormalizeItemID(id); err == nil {
		return n
	}
	return id
}

// sessionMachine is the machine whose session works on the item now, as
// the cluster's live session state tells it, else the item's claim.
func (s *Service) sessionMachine(p *Project, itemID, agent string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var best *domain.SessionInfo
	for _, si := range p.state.Sessions {
		if !sameItem(si.ItemID, itemID) || si.State == "done" || si.State == "failed" || si.Machine == "" {
			continue
		}
		if agent != "" && si.Agent != agent {
			continue
		}
		if best == nil || si.At.After(best.At) {
			best = si
		}
	}
	if best != nil {
		return best.Machine
	}
	if it := p.state.Items[itemID]; it != nil && it.Claim != nil && it.Status == domain.StatusInProgress {
		return it.Claim.Machine
	}
	return ""
}

// watchLocal opens a watch of a session running here.
func (r *Runtime) watchLocal(project, itemID, agent string) (*LiveWatch, bool) {
	r.mu.Lock()
	var pick *liveSession
	for _, ls := range r.running {
		if ls.project != project || !sameItem(ls.itemID, itemID) || ls.feed == nil {
			continue
		}
		if agent != "" && ls.agent != agent {
			continue
		}
		if pick == nil || ls.started.Before(pick.started) {
			pick = ls
		}
	}
	r.mu.Unlock()
	if pick == nil {
		return nil, false
	}
	back, ch, stop := pick.feed.subscribe()
	return &LiveWatch{Session: LiveSession{Project: pick.project, ItemID: pick.itemID, Agent: pick.agent, Role: pick.role, Step: pick.step,
		Provider: pick.provider, Model: pick.model, Machine: r.s.d.Machine, Since: pick.started.UTC()}, Backlog: back, Lines: ch, Stop: stop}, true
}

// flagMail marks the sessions running here that a new message is for.
func (s *Service) flagMail(e domain.Event) {
	if e.Type != domain.EvMessageSent || s.runtime == nil {
		return
	}
	pl, err := e.DecodePayload()
	if err != nil || pl.Message == nil {
		return
	}
	m := pl.Message
	r := s.runtime
	r.mu.Lock()
	var dirs []string
	for _, ls := range r.running {
		if ls.project != e.Project {
			continue
		}
		if m.To == ls.agent || (ls.role != "" && m.To == "role:"+ls.role) || m.To == "item:"+ls.itemID {
			dirs = append(dirs, ls.dir)
		}
	}
	r.mu.Unlock()
	for _, d := range dirs {
		_ = os.WriteFile(filepath.Join(d, ".barnraise-session", domain.MailFlag), nil, 0o600)
	}
}
