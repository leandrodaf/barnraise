package app

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// TrackerKey is the duty of repairing the tracker: one machine at a time
// makes the issues match the items, whoever changed them.
const TrackerKey = "lease/tracker"

// Timing of the tracker repair.
var (
	// trackerRepairEvery is the pause between two repairs of a project.
	trackerRepairEvery = 2 * time.Minute
	// trackerDownFor is how long a machine that could not write to the
	// tracker stays away from the repair, so a machine that can takes it.
	trackerDownFor = 15 * time.Minute
	// trackerGrace leaves a fresh change to the update sent right away; the
	// repair only takes what is older (and never races that update).
	trackerGrace = 2 * time.Minute
	// trackerBackfill is how far back a finished item without an issue still
	// gets one: older history is not poured into the tracker.
	trackerBackfill = 7 * 24 * time.Hour
)

// trackerHealth is this machine's last word with a project's tracker.
type trackerHealth struct {
	mu        sync.Mutex
	err       string
	downUntil time.Time
	// repaired is the item version the repair last wrote: a version the
	// tracker still shows differently is not written again and again.
	repaired map[string]int
}

func (s *Service) trackerHealthOf(project string) *trackerHealth {
	h, _ := s.trackerHealth.LoadOrStore(project, &trackerHealth{repaired: map[string]int{}})
	return h.(*trackerHealth)
}

// noteTracker records the outcome of a write to the tracker: an error is
// shown in the machine's heartbeat (and so in everyone's status) until a
// write works again.
func (s *Service) noteTracker(project string, err error) {
	h := s.trackerHealthOf(project)
	h.mu.Lock()
	defer h.mu.Unlock()
	if err == nil {
		h.err = ""
		return
	}
	h.err = truncate(err.Error(), 240)
}

// TrackerErr is why this machine cannot write to the project's tracker, "" when it can.
func (s *Service) TrackerErr(project string) string {
	h := s.trackerHealthOf(project)
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.err
}

func (s *Service) trackerDown(project string, now time.Time) bool {
	h := s.trackerHealthOf(project)
	h.mu.Lock()
	defer h.mu.Unlock()
	return now.Before(h.downUntil)
}

func (s *Service) markTrackerDown(project string, err error, now time.Time) {
	s.noteTracker(project, err)
	h := s.trackerHealthOf(project)
	h.mu.Lock()
	h.downUntil = now.Add(trackerDownFor)
	h.mu.Unlock()
}

// trackerLock is the one writer per item: two updates sent together make the
// tracker refuse one, and the last state must be the one that stays.
func (s *Service) trackerLock(p *Project, id string) *sync.Mutex {
	mu, _ := s.trackerMu.LoadOrStore(p.Name+"/"+id, &sync.Mutex{})
	return mu.(*sync.Mutex)
}

// repairTracker runs the repair when this machine may: the project asked for
// it, the machine can write to the tracker and holds the duty. A machine
// whose repair fails gives the duty up for a while, so another one takes it.
func (r *Runtime) repairTracker(ctx context.Context, p *Project, now time.Time) {
	s := r.s
	if p.Ports().Tracker == nil || !p.Cfg().Tracker.Reconcile || now.Sub(r.lastMirror[p.Name]) < trackerRepairEvery {
		return
	}
	r.lastMirror[p.Name] = now
	if s.trackerDown(p.Name, now) {
		s.releaseLease(ctx, p, TrackerKey)
		return
	}
	if !s.holdDuty(ctx, p, TrackerKey) {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		res, err := s.RepairTracker(ctx, p.Name)
		if err != nil {
			s.markTrackerDown(p.Name, err, time.Now())
			s.releaseLease(context.Background(), p, TrackerKey)
			s.d.Log.Warn("tracker repair; this machine leaves it to another for a while", "project", p.Name, "for", trackerDownFor, "err", err)
			return
		}
		s.noteTracker(p.Name, nil)
		if res.Changed() {
			s.d.Log.Info("tracker repaired", "project", p.Name, "created", res.Created, "adopted", res.Adopted, "updated", res.Updated)
		}
	}()
}

// TrackerRepair says what a repair changed.
type TrackerRepair struct {
	Created []string `json:"created,omitempty"` // items that got their issue
	Adopted []string `json:"adopted,omitempty"` // items linked back to the issue already there
	Updated []string `json:"updated,omitempty"` // issues that showed a stale status
	Failed  []string `json:"failed,omitempty"`
}

// Changed reports whether the repair wrote anything.
func (r TrackerRepair) Changed() bool {
	return len(r.Created)+len(r.Adopted)+len(r.Updated) > 0
}

// issueTitleRe reads the item id an issue's title starts with.
var issueTitleRe = regexp.MustCompile(`^\[(I-\d+)\]`)

// RepairTracker makes the tracker show every item as it is: an item without
// an issue gets one (or is linked back to the issue Barnraise opened for it
// before), and an issue whose state or status is stale is updated. Only
// Barnraise's own issues are touched. It fails when nothing it had to write
// went through, which is how a machine without access learns to step aside.
func (s *Service) RepairTracker(ctx context.Context, project string) (TrackerRepair, error) {
	var res TrackerRepair
	p, err := s.Project(project)
	if err != nil {
		return res, err
	}
	t := p.Ports().Tracker
	if t == nil {
		return res, nil
	}
	list, err := t.List(ctx, time.Time{})
	if err != nil {
		return res, fmt.Errorf("list the tracker: %w", err)
	}
	byRef := map[string]ports.TrackerItem{}
	byID := map[string]ports.TrackerItem{}
	for _, ti := range list {
		byRef[ti.Ref] = ti
		// Only an issue that carries Barnraise's label is taken as its own:
		// setting a label takes write access, a title anyone can type.
		if m := issueTitleRe.FindStringSubmatch(ti.Title); m != nil && slices.Contains(ti.Labels, "barnraise") {
			if _, dup := byID[m[1]]; !dup {
				byID[m[1]] = ti
			}
		}
	}
	now := s.d.Clock.Now().UTC()
	h := s.trackerHealthOf(p.Name)
	var errs []error
	tried := 0
	for _, snapIt := range p.Snapshot().ItemList() {
		if now.Sub(snapIt.UpdatedAt) < trackerGrace && !snapIt.UpdatedAt.IsZero() {
			continue
		}
		if snapIt.TrackerRef == "" && !needsIssue(snapIt, now) {
			continue
		}
		ti, known := byRef[snapIt.TrackerRef]
		if snapIt.TrackerRef != "" && (!known || s.trackerInSync(p, snapIt, ti)) {
			continue // in sync, or an issue the listing does not show (moved, deleted): left alone
		}
		h.mu.Lock()
		done := h.repaired[snapIt.ID] == snapIt.Version
		h.mu.Unlock()
		if done {
			continue
		}
		tried++
		action, err := s.repairOne(ctx, p, snapIt.ID, byID)
		if err != nil {
			res.Failed = append(res.Failed, snapIt.ID)
			errs = append(errs, fmt.Errorf("%s: %w", snapIt.ID, err))
			continue
		}
		switch action {
		case "created":
			res.Created = append(res.Created, snapIt.ID)
		case "adopted":
			res.Adopted = append(res.Adopted, snapIt.ID)
		case "updated":
			res.Updated = append(res.Updated, snapIt.ID)
		}
	}
	if tried > 0 && len(res.Failed) == tried {
		return res, errors.Join(errs...)
	}
	if len(errs) > 0 {
		s.d.Log.Warn("tracker repair left items for the next pass", "project", p.Name, "err", errors.Join(errs...))
	}
	return res, nil
}

// repairOne writes one item to the tracker under the item's lock, with the
// item as it is by then.
func (s *Service) repairOne(ctx context.Context, p *Project, id string, byID map[string]ports.TrackerItem) (string, error) {
	t := p.Ports().Tracker
	mu := s.trackerLock(p, id)
	mu.Lock()
	defer mu.Unlock()
	it := p.Snapshot().Items[id]
	if it == nil {
		return "", nil
	}
	action := "updated"
	if it.TrackerRef == "" {
		if ti, ok := byID[it.ID]; ok {
			// The issue is there but the item lost its reference (the
			// update that set it never came back): link it, no duplicate.
			cur, err := s.mutateItem(ctx, p, id, domain.EvItemUpdated, "tracker", func(x *domain.Item) error {
				x.TrackerRef, x.TrackerURL = ti.Ref, ti.URL
				return nil
			}, domain.Payload{Note: fmt.Sprintf("item %s linked back to %s in %s", id, ti.Ref, t.Name())})
			if err != nil {
				return "", err
			}
			it, action = cur, "adopted"
			if s.trackerInSync(p, it, ti) {
				s.markRepaired(p, it)
				return action, nil
			}
		} else {
			ti, err := t.Create(s.issueCtx(ctx, p, it), it, p.Cfg().Language)
			if err != nil {
				return "", err
			}
			cur, err := s.mutateItem(ctx, p, id, domain.EvItemUpdated, "tracker", func(x *domain.Item) error {
				x.TrackerRef, x.TrackerURL = ti.Ref, ti.URL
				return nil
			}, domain.Payload{Note: fmt.Sprintf("item %s appears as %s in %s", id, ti.Ref, t.Name())})
			if err != nil {
				return "", err
			}
			s.markRepaired(p, cur)
			return "created", nil
		}
	}
	if err := t.Update(s.issueCtx(ctx, p, it), it, p.Cfg().Language); err != nil {
		return "", err
	}
	s.markRepaired(p, it)
	return action, nil
}

func (s *Service) markRepaired(p *Project, it *domain.Item) {
	h := s.trackerHealthOf(p.Name)
	h.mu.Lock()
	h.repaired[it.ID] = it.Version
	h.mu.Unlock()
}

// needsIssue: a live item always has an issue; a finished one only when it
// finished recently (older history stays out of the tracker).
func needsIssue(it *domain.Item, now time.Time) bool {
	if !it.Status.Terminal() {
		return true
	}
	end := it.IntegratedAt
	if end.IsZero() {
		end = it.UpdatedAt
	}
	return !end.IsZero() && now.Sub(end) < trackerBackfill
}

// trackerInSync reports whether the issue shows the item's state: open or
// closed, and its status (as the tracker's column when the project maps one).
func (s *Service) trackerInSync(p *Project, it *domain.Item, ti ports.TrackerItem) bool {
	if ti.Closed != it.Status.Terminal() {
		return false
	}
	if ti.Status == "" {
		return true
	}
	want := string(it.Status)
	if col, ok := p.Cfg().Tracker.Columns[want]; ok && col != "" {
		want = col
	}
	return squashName(ti.Status) == squashName(want)
}

func squashName(s string) string {
	return strings.NewReplacer(" ", "", "_", "", "-", "").Replace(strings.ToLower(s))
}

// trackerAlerts: what the owner should know about the tracker, from any machine.
func (s *Service) trackerAlerts(p *Project, snap *domain.State, machines []MachineView) []string {
	if p.Ports().Tracker == nil {
		return nil
	}
	var out []string
	for _, m := range machines {
		if m.TrackerErr != "" && m.Health != "red" && !m.Left {
			out = append(out, fmt.Sprintf("machine %s cannot write to the tracker (%s): its changes reach the tracker only through another machine's repair", m.Name, m.TrackerErr))
		}
	}
	now := s.d.Clock.Now().UTC()
	var missing []string
	for _, it := range snap.ItemList() {
		if it.TrackerRef == "" && needsIssue(it, now) && now.Sub(it.CreatedAt) > 10*time.Minute {
			missing = append(missing, it.ID)
		}
	}
	if len(missing) > 0 {
		hint := ""
		if !p.Cfg().Tracker.Reconcile {
			hint = " (set tracker.reconcile: true to repair it)"
		}
		out = append(out, fmt.Sprintf("%d items have no issue in the tracker: %s%s", len(missing), strings.Join(firstN(missing, 8), ", "), hint))
	}
	return out
}

func firstN(s []string, n int) []string {
	if len(s) <= n {
		return s
	}
	return append(slices.Clone(s[:n]), "…")
}
