package app

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// Work of other clusters. A teammate's Barnraise, not paired with this one,
// can work on the same repository: its items have their own ids (from the
// shared counter) and their own claims, so neither cluster takes the
// other's item, but nothing kept them off the same files, and two big
// deliveries that touch the same code conflict at integration or do the
// same thing twice. Every machine looks, every foreignEvery, at the claims
// in the repository that are not this cluster's items, reads the files
// their branches change, and does not start a new item that would change
// them too (scheduling.other_clusters: wait). What already runs here goes
// on.

// foreignEvery is how often the other clusters' work is read again.
const foreignEvery = 2 * time.Minute

// foreignWork is one item another cluster is working on.
type foreignWork struct {
	ID     string // I-89 (another cluster's item) or #32 (a pull request)
	Branch string
	SHA    string
	Files  []string
	Author string
	Title  string
	URL    string
	// Updated is the last activity seen; Stale marks work idle for longer
	// than scheduling.stale_after: it holds nothing.
	Updated time.Time
	Stale   bool
	// Machine is the other cluster's machine that holds the claim (apolo).
	Machine string
}

// refreshForeignWork reads the other clusters' work when it is due.
func (s *Service) refreshForeignWork(ctx context.Context, p *Project, now time.Time) {
	if p.Cfg().Scheduling.OtherClusters == domain.OtherClustersIgnore {
		p.mu.Lock()
		p.foreign = nil
		p.mu.Unlock()
		return
	}
	a, g := p.Ports().Arbiter, p.Ports().Git
	if a == nil || g == nil {
		return
	}
	p.mu.Lock()
	if now.Sub(p.foreignAt) < foreignEvery {
		p.mu.Unlock()
		return
	}
	p.foreignAt = now
	known := map[string]*foreignWork{}
	for i := range p.foreign {
		known[p.foreign[i].ID] = &p.foreign[i]
	}
	p.mu.Unlock()

	keys, err := a.List(ctx, "claims/")
	if err != nil {
		return
	}
	snap := p.Snapshot()
	var ids []string
	for _, k := range keys {
		id := strings.TrimPrefix(k, "claims/")
		if _, ours := snap.Items[id]; ours || !strings.HasPrefix(id, "I-") {
			continue
		}
		ids = append(ids, id)
	}
	var branches map[string]string
	main := ""
	if len(ids) > 0 {
		if branches, err = g.RemoteBranches(ctx, "br/"); err != nil {
			return
		}
		if main, err = g.MainHead(ctx); err != nil {
			return
		}
	}
	var out []foreignWork
	for _, id := range ids {
		branch, sha := "", ""
		for name, h := range branches {
			if name == "br/"+id || strings.HasPrefix(name, "br/"+id+"-") {
				branch, sha = name, h
				break
			}
		}
		if branch == "" {
			continue // claimed, nothing pushed yet
		}
		stale, holder := false, ""
		var renewed time.Time
		if info, err := a.Inspect(ctx, "claims/"+id); err == nil {
			holder, renewed = info.Holder, info.Renewed
			if !info.Renewed.IsZero() && now.Sub(info.Renewed) > p.Cfg().Lease.TTL+foreignEvery {
				stale = true // its machine stopped renewing: nobody works on it now
			}
		}
		if k := known[id]; k != nil && k.SHA == sha {
			k.Stale, k.Machine, k.Updated = stale, holder, renewed
			out = append(out, *k)
			continue
		}
		if _, err := g.FetchBranch(ctx, branch); err != nil {
			continue
		}
		base, err := g.MergeBase(ctx, main, sha)
		if err != nil {
			continue
		}
		files, err := g.ChangedFiles(ctx, base, sha)
		if err != nil {
			continue
		}
		var keep []string
		for _, f := range files {
			if !domain.Contains(ItemFiles, f) && f != domain.RepoMapFile {
				keep = append(keep, f)
			}
		}
		out = append(out, foreignWork{ID: id, Branch: branch, SHA: sha, Files: keep, Stale: stale, Machine: holder, Updated: renewed})
	}
	out = append(out, s.foreignPRs(ctx, p, snap, known, out, now)...)
	p.mu.Lock()
	p.foreign = out
	p.mu.Unlock()
	s.proposeStaleWork(ctx, p, out)
}

// foreignPRs are the open pull requests that are not this cluster's items:
// a person's, or another cluster's when its claim showed nothing.
func (s *Service) foreignPRs(ctx context.Context, p *Project, snap *domain.State, known map[string]*foreignWork, have []foreignWork, now time.Time) []foreignWork {
	lister, ok := p.Ports().CodeHost.(ports.PRLister)
	if !ok || p.Ports().CodeHost == nil {
		return nil
	}
	prs, err := lister.OpenPRs(ctx)
	if err != nil {
		s.d.Log.Debug("list the open pull requests", "project", p.Name, "err", err)
		return nil
	}
	ours := map[string]bool{}
	for _, it := range snap.Items {
		if it.Branch != "" {
			ours[it.Branch] = true
		}
		if it.Alt != nil && it.Alt.Branch != "" {
			ours[it.Alt.Branch] = true
		}
	}
	for _, f := range have {
		ours[f.Branch] = true
	}
	var out []foreignWork
	for _, pr := range prs {
		if ours[pr.Head] {
			continue
		}
		id := fmt.Sprintf("#%d", pr.Number)
		fw := foreignWork{ID: id, Branch: pr.Head, SHA: pr.SHA, Author: pr.Author, Title: pr.Title, URL: pr.URL, Updated: pr.Updated,
			Stale: !pr.Updated.IsZero() && now.Sub(pr.Updated) > p.Cfg().Scheduling.StaleAfter}
		if k := known[id]; k != nil && k.SHA == pr.SHA {
			fw.Files = k.Files
		} else if files, err := lister.PRFiles(ctx, pr.Number); err == nil {
			fw.Files = files
		} else {
			continue
		}
		out = append(out, fw)
	}
	return out
}

// proposeStaleWork proposes, once, to continue or take over other people's
// work that stopped: a pull request idle past scheduling.stale_after, or
// another cluster's item whose machine stopped renewing it. Only the
// machine that holds the housekeeping does it; the autopilot or the owner
// decides, and an accepted proposal becomes an item like any other.
func (s *Service) proposeStaleWork(ctx context.Context, p *Project, work []foreignWork) {
	p.mu.Lock()
	_, duty := p.leases[HousekeepingKey]
	p.mu.Unlock()
	if !duty && p.Ports().Arbiter != nil {
		return
	}
	snap := p.Snapshot()
	for _, f := range work {
		if !f.Stale {
			continue
		}
		mark := "stale work " + f.ID + " at " + short(f.SHA)
		seen := false
		for _, pr := range snap.Proposals {
			if strings.Contains(pr.Evidence, mark) {
				seen = true
				break
			}
		}
		if seen {
			continue
		}
		who := f.Author
		if who == "" {
			who = "another Barnraise on this repository"
		}
		idle := "its machine stopped renewing its claim"
		if !f.Updated.IsZero() {
			idle = fmt.Sprintf("no activity since %s", f.Updated.Format("2006-01-02"))
		}
		files := f.Files
		if len(files) > 12 {
			files = append(files[:12:12], fmt.Sprintf("and %d more", len(f.Files)-12))
		}
		area := "general"
		for _, file := range f.Files {
			if a := p.Cfg().AreaOfFile(file); a != "" {
				area = a
				break
			}
		}
		title := strings.TrimSpace("Continue or take over " + f.ID + " " + f.Title)
		_, err := s.Propose(ctx, p.Name, System(), domain.Proposal{
			Title: truncate(title, 80),
			Problem: fmt.Sprintf("%s (branch %s, by %s) stopped: %s. It changes %s. An item can continue it from its branch (rebase it on main, finish what it set out to do, keep its author's intent) "+
				"or take it over with a smaller scope; if the work is no longer wanted, close it. %s", f.ID, f.Branch, who, idle, strings.Join(files, ", "), f.URL),
			Evidence: mark,
			Impact:   "minor", Size: "unknown", Area: area, Kind: "improvement", By: "barnraise", GoalLink: "work left by someone else on this repository",
		})
		if err != nil {
			s.d.Log.Warn("propose stale work", "project", p.Name, "work", f.ID, "err", err)
		}
	}
}

// foreignOverlapLocked is the other cluster's item whose files an item
// would change too, "" when none. Callers hold p.mu.
func (s *Service) foreignOverlapLocked(p *Project, files []string) string {
	if len(files) == 0 || p.Cfg().Scheduling.OtherClusters == domain.OtherClustersIgnore {
		return ""
	}
	for _, f := range p.foreign {
		if f.Stale {
			continue // idle work holds nothing; it is proposed instead
		}
		if domain.FilesOverlap(files, p.Cfg().Scheduling.Exclusive(f.Files)) {
			return f.ID
		}
	}
	return ""
}

// noteForeignWaitLocked logs once that an item waits for another cluster.
func (s *Service) noteForeignWaitLocked(p *Project, itemID, foreign string) {
	if p.foreignWaits == nil {
		p.foreignWaits = map[string]string{}
	}
	if p.foreignWaits[itemID] == foreign {
		return
	}
	p.foreignWaits[itemID] = foreign
	s.d.Log.Info("the item waits: another Barnraise on this repository changes the same files", "project", p.Name, "item", itemID, "their_item", foreign)
}

// ForeignWork lists the other clusters' items this machine knows of.
func (s *Service) ForeignWork(project string) []string {
	p, err := s.Project(project)
	if err != nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for _, f := range p.foreign {
		state := ""
		if f.Stale {
			state = ", stale"
		}
		out = append(out, f.ID+" ("+f.Branch+state+")")
	}
	slices.Sort(out)
	return out
}

// OtherWork is someone else's work on the project's repository, as the
// panel lists it next to this cluster's items.
type OtherWork struct {
	ID      string    `json:"id"` // I-89 or #32
	Kind    string    `json:"kind"`
	Title   string    `json:"title,omitempty"`
	URL     string    `json:"url,omitempty"`
	Branch  string    `json:"branch,omitempty"`
	Author  string    `json:"author,omitempty"`
	Machine string    `json:"machine,omitempty"`
	Updated time.Time `json:"updated,omitzero"`
	Stale   bool      `json:"stale,omitempty"`
	Files   int       `json:"files"`
	// Holds are this cluster's items that wait for it (same files).
	Holds []string `json:"holds,omitempty"`
}

// OtherWorkView lists them.
type OtherWorkView struct {
	Work []OtherWork `json:"work"`
	// Assigned are this cluster's items whose issue a person took.
	Assigned []AssignedItem `json:"assigned"`
	Mode     string         `json:"mode"`
}

// AssignedItem is an item whose tracker entry a person holds.
type AssignedItem struct {
	ID     string   `json:"id"`
	Title  string   `json:"title"`
	Ref    string   `json:"ref"`
	URL    string   `json:"url,omitempty"`
	People []string `json:"people"`
}

// OtherWork lists other people's work on the repository and what it holds.
func (s *Service) OtherWork(project string, a Actor) (*OtherWorkView, error) {
	p, err := s.Project(project)
	if err != nil {
		return nil, err
	}
	if err := a.Require(ScopeItemsRead, p.Name); err != nil {
		return nil, err
	}
	out := &OtherWorkView{Work: []OtherWork{}, Assigned: []AssignedItem{}, Mode: p.Cfg().Scheduling.OtherClusters}
	p.mu.Lock()
	waits := map[string][]string{}
	for item, their := range p.foreignWaits {
		if it := p.state.Items[item]; it != nil && it.Status == domain.StatusReady {
			waits[their] = append(waits[their], item)
		}
	}
	for _, f := range p.foreign {
		kind := "cluster"
		if strings.HasPrefix(f.ID, "#") {
			kind = "pull_request"
		}
		holds := waits[f.ID]
		slices.Sort(holds)
		out.Work = append(out.Work, OtherWork{ID: f.ID, Kind: kind, Title: f.Title, URL: f.URL, Branch: f.Branch, Author: f.Author, Machine: f.Machine,
			Updated: f.Updated, Stale: f.Stale, Files: len(f.Files), Holds: holds})
	}
	for _, it := range p.state.Items {
		if len(it.HeldBy) > 0 && !it.Status.Terminal() {
			out.Assigned = append(out.Assigned, AssignedItem{ID: it.ID, Title: it.Title, Ref: it.TrackerRef, URL: it.TrackerURL, People: it.HeldBy})
		}
	}
	p.mu.Unlock()
	slices.SortFunc(out.Work, func(x, y OtherWork) int { return strings.Compare(x.ID, y.ID) })
	slices.SortFunc(out.Assigned, func(x, y AssignedItem) int { return strings.Compare(x.ID, y.ID) })
	return out, nil
}
