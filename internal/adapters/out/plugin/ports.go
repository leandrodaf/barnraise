package plugin

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/leandrodaf/barnraise/internal/adapters/out/httpx/itemdoc"
	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// WireItem is an item as Barnraise sends it to a tracker plugin.
type WireItem struct {
	ID       string   `json:"id"`
	Ref      string   `json:"ref,omitempty"` // the tracker's own reference, once created
	Kind     string   `json:"kind"`
	Title    string   `json:"title"`    // with the item id, as the built-in trackers show it
	Body     string   `json:"body"`     // Markdown in the project's language
	Status   string   `json:"status"`   // Barnraise status
	Column   string   `json:"column"`   // the tracker column of that status (tracker.columns), "" when unmapped
	Labels   []string `json:"labels"`   // kind, priority, area, status:<status>
	Priority string   `json:"priority"` // P0…P3
	Area     string   `json:"area,omitempty"`
	Branch   string   `json:"branch,omitempty"`
	PR       string   `json:"pr,omitempty"`
	Adopted  bool     `json:"adopted,omitempty"` // a person wrote it in the tracker: keep its title and body
	Closed   bool     `json:"closed"`
}

// Tracker is a plugin seen as ports.Tracker.
type Tracker struct {
	h       *Host
	columns map[string]string
}

var _ ports.Tracker = (*Tracker)(nil)

// Tracker returns the plugin as a tracker; columns maps Barnraise statuses
// to the tracker's columns (tracker.columns in barnraise.yaml).
func (h *Host) Tracker(columns map[string]string) *Tracker {
	return &Tracker{h: h, columns: columns}
}

// Name is "plugin:<name>".
func (t *Tracker) Name() string { return "plugin:" + t.h.o.Name }

func (t *Tracker) wire(it *domain.Item, lang string) WireItem {
	return WireItem{
		ID: it.ID, Ref: it.TrackerRef, Kind: it.Kind, Title: itemdoc.Title(it), Body: itemdoc.Markdown(it, lang),
		Status: string(it.Status), Column: t.columns[string(it.Status)], Labels: itemdoc.Labels(it),
		Priority: it.Priority.String(), Area: it.Area, Branch: it.Branch, PR: it.PR, Adopted: it.Adopted, Closed: itemdoc.Closed(it),
	}
}

// List returns the tracker's entries changed since (all when zero).
func (t *Tracker) List(ctx context.Context, since time.Time) ([]ports.TrackerItem, error) {
	if err := t.h.require(ctx, domain.PluginTracker); err != nil {
		return nil, err
	}
	params := map[string]any{"since": ""}
	if !since.IsZero() {
		params["since"] = since.UTC().Format(time.RFC3339)
	}
	var out struct {
		Items []ports.TrackerItem `json:"items"`
	}
	if err := t.h.Call(ctx, "tracker.list", params, &out, 0); err != nil {
		return nil, err
	}
	for i, it := range out.Items {
		if strings.TrimSpace(it.Ref) == "" {
			return nil, fmt.Errorf("plugin %s listed an entry without a ref (entry %d)", t.h.o.Name, i+1)
		}
		// People wrote this text: hidden instructions are stripped here,
		// as every built-in tracker does.
		out.Items[i].Title = domain.SanitizeExternal(it.Title)
		out.Items[i].Body = domain.SanitizeExternal(it.Body)
	}
	return out.Items, nil
}

// Create creates the item in the tracker and returns its reference.
func (t *Tracker) Create(ctx context.Context, it *domain.Item, lang string) (ports.TrackerItem, error) {
	if err := t.h.require(ctx, domain.PluginTracker); err != nil {
		return ports.TrackerItem{}, err
	}
	var out ports.TrackerItem
	if err := t.h.Call(ctx, "tracker.create", map[string]any{"item": t.wire(it, lang), "lang": lang}, &out, 0); err != nil {
		return ports.TrackerItem{}, err
	}
	if strings.TrimSpace(out.Ref) == "" {
		return ports.TrackerItem{}, fmt.Errorf("plugin %s created %s but answered without a ref", t.h.o.Name, it.ID)
	}
	return out, nil
}

// Update sends the item's current state.
func (t *Tracker) Update(ctx context.Context, it *domain.Item, lang string) error {
	if err := t.h.require(ctx, domain.PluginTracker); err != nil {
		return err
	}
	if strings.TrimSpace(it.TrackerRef) == "" {
		return errors.New("the item has no tracker reference yet")
	}
	return t.h.Call(ctx, "tracker.update", map[string]any{"item": t.wire(it, lang), "lang": lang}, nil, 0)
}

// Comment adds a comment to the entry ref.
func (t *Tracker) Comment(ctx context.Context, ref, text string) error {
	if err := t.h.require(ctx, domain.PluginTracker); err != nil {
		return err
	}
	return t.h.Call(ctx, "tracker.comment", map[string]string{"ref": ref, "text": text}, nil, 0)
}

// Columns lists the tracker's columns.
func (t *Tracker) Columns(ctx context.Context) ([]string, error) {
	if err := t.h.require(ctx, domain.PluginTracker); err != nil {
		return nil, err
	}
	var out struct {
		Columns []string `json:"columns"`
	}
	if err := t.h.Call(ctx, "tracker.columns", map[string]any{}, &out, 0); err != nil {
		return nil, err
	}
	return out.Columns, nil
}

// Notifier is a plugin seen as ports.Notifier.
type Notifier struct {
	h      *Host
	target string
	extra  map[string]string
}

var _ ports.Notifier = (*Notifier)(nil)

// Notifier returns the plugin as a notification channel; target and extra
// come from the notifications entry and are sent along.
func (h *Host) Notifier(target string, extra map[string]string) *Notifier {
	return &Notifier{h: h, target: target, extra: extra}
}

// Notify sends n with notify.send.
func (n *Notifier) Notify(ctx context.Context, x ports.Notification) error {
	if err := n.h.require(ctx, domain.PluginNotifier); err != nil {
		return err
	}
	params := map[string]any{
		"title": x.Title, "body": x.Body, "urgent": x.Urgent, "link": x.Link, "options": x.Options, "kind": x.Kind,
		"target": n.target, "extra": n.extra,
	}
	return n.h.Call(ctx, "notify.send", params, nil, 0)
}

// CheckOutcome is what check.run answers.
type CheckOutcome struct {
	Passed     bool     `json:"passed"`
	Summary    string   `json:"summary"`
	Log        string   `json:"log,omitempty"`
	Artifacts  []string `json:"artifacts,omitempty"`
	Flaky      bool     `json:"flaky,omitempty"`
	Pending    bool     `json:"pending,omitempty"`
	Criteria   []string `json:"criteria,omitempty"`
	DurationMS int64    `json:"duration_ms,omitempty"`
}

// RunCheck runs a check on the plugin. The call's timeout is the check's
// own; the full log, when the plugin returns one, is kept in LogDir.
func (h *Host) RunCheck(ctx context.Context, spec ports.CheckSpec) (domain.CheckResult, bool, error) {
	res := domain.CheckResult{ItemID: spec.ItemID, Check: spec.Def.Name, Commit: spec.Commit, RunsOn: "plugin:" + h.o.Name}
	if err := h.require(ctx, domain.PluginCheck); err != nil {
		return res, false, err
	}
	timeout := spec.Def.Timeout
	if timeout <= 0 {
		timeout = h.o.Def.Timeout
	}
	params := map[string]any{
		"check": spec.Def.Name, "run": spec.Def.Run, "item_id": spec.ItemID, "branch": spec.Branch, "commit": spec.Commit,
		"dir": spec.Dir, "touched": spec.Touched, "areas": spec.Areas, "env": spec.Env, "main": spec.Main,
		"timeout_ms": timeout.Milliseconds(),
	}
	start := time.Now()
	var out CheckOutcome
	if err := h.Call(ctx, "check.run", params, &out, timeout); err != nil {
		return res, false, err
	}
	res.Passed, res.Summary, res.Artifacts, res.Flaky, res.Criteria = out.Passed, out.Summary, out.Artifacts, out.Flaky, out.Criteria
	res.Duration = time.Duration(out.DurationMS) * time.Millisecond
	if res.Duration <= 0 {
		res.Duration = time.Since(start)
	}
	if out.Log != "" && h.o.LogDir != "" {
		if err := os.MkdirAll(h.o.LogDir, 0o700); err == nil {
			name := fmt.Sprintf("%s-%s-plugin-%d.log", domain.Slug(spec.ItemID, 20), domain.Slug(spec.Def.Name, 40), time.Now().UnixNano())
			p := filepath.Join(h.o.LogDir, name)
			if err := os.WriteFile(p, []byte(out.Log), 0o600); err == nil {
				res.LogPath = p
			}
		}
	}
	if res.Summary == "" && out.Pending {
		res.Summary = "sent to plugin " + h.o.Name
	}
	return res, out.Pending, nil
}

// Wants reports whether the plugin's events list covers an event type.
func (h *Host) Wants(eventType string) bool {
	for _, e := range h.o.Def.Events {
		if e == "*" || e == eventType {
			return true
		}
		if p, ok := strings.CutSuffix(e, "*"); ok && strings.HasPrefix(eventType, p) {
			return true
		}
	}
	return false
}

// Event sends one event with hook.event and waits for the answer.
func (h *Host) Event(ctx context.Context, e domain.Event) error {
	if err := h.require(ctx, domain.PluginHook); err != nil {
		return err
	}
	return h.Call(ctx, "hook.event", map[string]any{"event": e}, nil, 0)
}

// HookQueue is how many events wait for a slow hook plugin before new
// ones are dropped.
const HookQueue = 256

// Deliver queues an event for the plugin without blocking: hooks see what
// happened, they never hold the flow. It reports false when the queue is
// full or the host is closed.
func (h *Host) Deliver(e domain.Event) bool {
	h.hmu.Lock()
	if h.hclosed {
		h.hmu.Unlock()
		return false
	}
	h.hookOnce.Do(func() {
		h.hooks = make(chan domain.Event, HookQueue)
		h.hookDone = make(chan struct{})
		go h.hookWorker()
	})
	h.hmu.Unlock()
	select {
	case h.hooks <- e:
		return true
	default:
		h.o.Log.Warn("hook plugin is behind; event dropped", "event", e.Type, "subject", e.Subject)
		return false
	}
}

func (h *Host) hookWorker() {
	defer close(h.hookDone)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-h.stop
		cancel()
	}()
	for {
		select {
		case <-h.stop:
			return
		case e := <-h.hooks:
			if err := h.Event(ctx, e); err != nil && ctx.Err() == nil {
				h.o.Log.Warn("hook plugin", "event", e.Type, "err", err)
			}
		}
	}
}
