package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// ProposeResult is a new proposal or a comment on an existing one.
type ProposeResult struct {
	Proposal  *domain.Proposal `json:"proposal"`
	Duplicate string           `json:"duplicate,omitempty"` // id it became a comment on
	// Item is the item a blocking proposal became (or the item that already
	// covers it): the proposing item waits for it.
	Item string `json:"item,omitempty"`
}

// Propose records a proposal in the fixed shape, with duplicate detection: a
// similar proposal or item in the same area gets a comment. A proposal an
// agent's item cannot be delivered without (Blocks) is not left to triage:
// it becomes an item at once, and the result names it for the proposing item
// to wait for. What is not needed is weighed by triage, not counted.
func (s *Service) Propose(ctx context.Context, project string, a Actor, in domain.Proposal) (ProposeResult, error) {
	p, err := s.Project(project)
	if err != nil {
		return ProposeResult{}, err
	}
	blocking := in.Blocks && a.Kind == ActorAgent && a.ItemID != ""
	in.Blocks = blocking
	res, err := s.propose(ctx, p, a, in)
	if err != nil || !blocking {
		return res, err
	}
	switch {
	case strings.HasPrefix(res.Duplicate, "I-"):
		// An item already covers it: wait for that one.
		res.Item = res.Duplicate
		return res, nil
	case res.Duplicate != "":
		res.Proposal = &domain.Proposal{ID: res.Duplicate}
	}
	if res.Proposal == nil {
		return res, nil
	}
	pr, err := s.DecideProposal(ctx, p.Name, System(), res.Proposal.ID, "approve",
		fmt.Sprintf("%s cannot be delivered without it (reported by %s)", a.ItemID, actorName(a)))
	if err != nil {
		if errors.Is(err, ErrConflict) {
			// Decided before: approved already, or turned down by a person.
			if cur := p.Snapshot().Proposals[res.Proposal.ID]; cur != nil {
				res.Proposal, res.Item = cur, cur.ItemID
			}
			return res, nil
		}
		return res, err
	}
	res.Proposal, res.Item = pr, pr.ItemID
	return res, nil
}

func (s *Service) propose(ctx context.Context, p *Project, a Actor, in domain.Proposal) (ProposeResult, error) {
	if err := a.Require(ScopePropose, p.Name); err != nil {
		return ProposeResult{}, err
	}
	in.Area = domain.AreaName(in.Area)
	in.Title = strings.TrimSpace(domain.SanitizeExternal(in.Title))
	in.Problem = domain.SanitizeExternal(in.Problem)
	if in.Kind == "" {
		in.Kind = "improvement"
	}
	if err := in.Validate(); err != nil {
		return ProposeResult{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if in.By == "" {
		in.By = actorName(a)
	}
	now := s.d.Clock.Now().UTC()
	p.mu.Lock()
	defer p.mu.Unlock()
	props := p.state.ProposalList()
	if a.Kind == ActorAgent {
		in.FromItem = a.ItemID
	}
	if a.Kind == ActorAgent && !in.Blocks {
		if err := domain.CheckProposalLimit(props, in.By, in.FromItem, now, p.Cfg().Proposals.PerAgentPerDay); err != nil {
			return ProposeResult{}, fmt.Errorf("%w: %v", ErrConflict, err)
		}
	}
	if m, dup := domain.FindDuplicate(&in, props, p.state.ItemList()); dup && p.Cfg().Proposals.Duplicate == "comment" {
		body := fmt.Sprintf("%s — evidence: %s", in.Problem, in.Evidence)
		comment := fmt.Sprintf("%s (%s): %s", in.By, now.Format("2006-01-02"), body)
		if strings.HasPrefix(m.ID, "P-") {
			cur := *p.state.Proposals[m.ID]
			// The tracker's import sends the same issues again every few
			// minutes and at every start: the same finding is said once,
			// and copies left by earlier imports go.
			comments := dedupeComments(cur.Comments)
			said := slices.ContainsFunc(comments, func(c string) bool { return commentBody(c) == body })
			if !said {
				comments = append(comments, comment)
			}
			if said && len(comments) == len(cur.Comments) {
				return ProposeResult{Proposal: &cur, Duplicate: m.ID}, nil
			}
			cur.Version++
			cur.Comments = comments
			if _, err := s.emitLocked(ctx, p, domain.EvProposalUpdated, cur.ID, actorName(a), domain.Payload{Proposal: &cur, Note: "duplicate became a comment"}); err != nil {
				return ProposeResult{}, err
			}
			return ProposeResult{Proposal: &cur, Duplicate: m.ID}, nil
		}
		_, _ = s.emitLocked(ctx, p, domain.EvMessageSent, m.ID, actorName(a), domain.Payload{Message: &domain.Message{
			ID: s.d.IDs.NewID(), From: actorName(a), To: "item:" + m.ID, ItemID: m.ID, Text: "Related finding: " + truncate(comment, 1500), At: now}})
		return ProposeResult{Duplicate: m.ID}, nil
	}
	in.ID = p.state.NextID(domain.KindProposal)
	in.Version = 1
	in.Status = domain.ProposalOpen
	in.CreatedAt = now
	if _, err := s.emitLocked(ctx, p, domain.EvProposalCreated, in.ID, actorName(a), domain.Payload{Proposal: &in}); err != nil {
		return ProposeResult{}, err
	}
	return ProposeResult{Proposal: &in}, nil
}

// DecideProposal approves (creates the item), rejects or defers ("later").
func (s *Service) DecideProposal(ctx context.Context, project string, a Actor, id, decision, reason string) (*domain.Proposal, error) {
	p, err := s.Project(project)
	if err != nil {
		return nil, err
	}
	if err := a.Require(ScopeProposalDecide, p.Name); err != nil && !a.Can(ScopeApprove, p.Name) {
		return nil, err
	}
	id = strings.ToUpper(strings.TrimSpace(id))
	p.mu.Lock()
	cur, ok := p.state.Proposals[id]
	if !ok {
		p.mu.Unlock()
		return nil, fmt.Errorf("%w: proposal %s", ErrNotFound, id)
	}
	pr := *cur
	p.mu.Unlock()
	if pr.Status != domain.ProposalOpen && pr.Status != domain.ProposalLater {
		return nil, fmt.Errorf("%w: %s is %s", ErrConflict, id, pr.Status)
	}
	// Features and self-improvement go to the owner; agents (analyst) decide the rest.
	if (pr.Kind == "feature" || pr.Kind == "self_improvement") && !a.IsPerson() && a.Kind != ActorSystem {
		return nil, fmt.Errorf("%w: new features and self-improvement are approved by the owner", ErrForbidden)
	}
	now := s.d.Clock.Now().UTC()
	pr.Version++
	pr.DecidedAt, pr.DecidedBy, pr.Reason = now, actorName(a), reason
	switch decision {
	case "approve":
		kind := "improvement"
		switch pr.Kind {
		case "bug":
			kind = "bug"
		case "self_improvement":
			kind = "self_improvement"
		case "feature":
			kind = "feature"
		}
		body, derr := RenderDocument(p.Cfg(), domain.DocItem, DocData{
			Item:     DocItem{Title: pr.Title, Kind: kind, Priority: pr.Priority.String(), Area: pr.Area},
			Proposal: DocProposal{ID: pr.ID, Title: pr.Title, Kind: pr.Kind, Problem: pr.Problem, Evidence: pr.Evidence, Impact: pr.Impact, Goal: pr.GoalLink, Area: pr.Area}})
		if derr != nil {
			s.d.Log.Warn("item document", "proposal", pr.ID, "err", derr)
		}
		res, err := s.CreateItem(ctx, p.Name, Owner(actorName(a)), NewItem{Title: pr.Title, Kind: kind, Priority: pr.Priority.String(), Area: pr.Area, Size: pr.Size,
			Body: strings.TrimSpace(body)})
		if err != nil {
			return nil, err
		}
		pr.Status = domain.ProposalApproved
		if res.Item != nil {
			pr.ItemID = res.Item.ID
		}
	case "reject":
		pr.Status = domain.ProposalRejected
	case "later":
		pr.Status = domain.ProposalLater
		pr.LaterUntil = now.Add(7 * 24 * time.Hour)
	default:
		return nil, fmt.Errorf("%w: decision must be approve, reject or later", ErrInvalid)
	}
	if _, err := s.emit(ctx, p, domain.EvProposalUpdated, pr.ID, actorName(a), domain.Payload{Proposal: &pr}); err != nil {
		return nil, err
	}
	return &pr, nil
}

// ListProposals returns proposals, optionally grouped by area.
func (s *Service) ListProposals(project string, a Actor, all bool) ([]*domain.Proposal, []domain.ProposalGroup, error) {
	p, err := s.Project(project)
	if err != nil {
		return nil, nil, err
	}
	if err := a.Require(ScopeItemsRead, p.Name); err != nil {
		return nil, nil, err
	}
	snap := p.Snapshot()
	list := snap.ProposalList()
	var out []*domain.Proposal
	for _, pr := range list {
		if all || pr.Status == domain.ProposalOpen || pr.Status == domain.ProposalLater {
			out = append(out, pr)
		}
	}
	return out, domain.GroupProposals(list), nil
}

// expireProposals closes proposals not approved in time, with the reason.
func (s *Service) expireProposals(ctx context.Context, p *Project) int {
	now := s.d.Clock.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, pr := range domain.ExpireProposals(p.state.ProposalList(), now, p.Cfg().Proposals.Expire) {
		c := *pr
		c.Version++
		c.Status = domain.ProposalExpired
		c.Reason = fmt.Sprintf("not approved within %s", p.Cfg().Proposals.Expire)
		c.DecidedAt = now.UTC()
		c.DecidedBy = "barnraise"
		if _, err := s.emitLocked(ctx, p, domain.EvProposalUpdated, c.ID, "barnraise", domain.Payload{Proposal: &c}); err == nil {
			n++
		}
	}
	return n
}

// AddTweak records a small fix that rides with the next item of its area.
func (s *Service) AddTweak(ctx context.Context, project string, a Actor, area, text string) (*domain.Tweak, *domain.Item, error) {
	p, err := s.Project(project)
	if err != nil {
		return nil, nil, err
	}
	if err := a.Require(ScopeTweak, p.Name); err != nil {
		return nil, nil, err
	}
	area = domain.AreaName(strings.TrimSpace(area))
	text = strings.TrimSpace(domain.SanitizeExternal(text))
	if area == "" || text == "" {
		return nil, nil, fmt.Errorf("%w: a tweak needs an area and a text", ErrInvalid)
	}
	p.mu.Lock()
	t := &domain.Tweak{ID: "T-" + s.d.IDs.NewID(), Area: area, Text: truncate(text, 500), By: actorName(a), CreatedAt: s.d.Clock.Now().UTC()}
	if _, err := s.emitLocked(ctx, p, domain.EvTweakAdded, t.ID, actorName(a), domain.Payload{Tweak: t}); err != nil {
		p.mu.Unlock()
		return nil, nil, err
	}
	pending := domain.PendingTweaks(p.state.TweakList(), area)
	limit := p.Cfg().Tweaks.BecomeItemAt
	p.mu.Unlock()
	if limit > 0 && len(pending) >= limit {
		// Enough tweaks accumulated: they become one item.
		var lines []string
		for _, pt := range pending {
			lines = append(lines, pt.Text)
		}
		res, err := s.CreateItem(ctx, p.Name, System(), NewItem{Title: fmt.Sprintf("Tweaks in %s (%d)", area, len(pending)), Kind: "improvement", Area: area,
			Checklist: lines, Body: "Small fixes accumulated in the area:\n- " + strings.Join(lines, "\n- ") + "\n\n[NEEDS CLARIFICATION: analyst writes EARS criteria]"})
		if err != nil {
			return t, nil, err
		}
		p.mu.Lock()
		for _, pt := range pending {
			c := *pt
			c.ItemID = res.Item.ID
			_, _ = s.emitLocked(ctx, p, domain.EvTweakAttached, c.ID, "barnraise", domain.Payload{Tweak: &c})
		}
		p.mu.Unlock()
		return t, res.Item, nil
	}
	return t, nil, nil
}

// InventoryEntry is one item of a goal's inventory.
type InventoryEntry struct {
	Title string
	Area  string
	Spec  string
	Body  string
}

// inventory lists the finite work of a goal once: "screens docs/prototipo-mobile"
// turns each screen file into an item; "files <glob>" each file; "lines <file>" each line.
func (s *Service) inventory(p *Project, spec string) ([]InventoryEntry, error) {
	kind, arg, _ := strings.Cut(strings.TrimSpace(spec), " ")
	arg = strings.TrimSpace(arg)
	root := p.Cfg().Root
	var out []InventoryEntry
	switch kind {
	case "screens", "files":
		base := filepath.Join(root, arg)
		info, err := os.Stat(base)
		if err != nil {
			return nil, fmt.Errorf("%w: inventory source %s: %v", ErrInvalid, arg, err)
		}
		var paths []string
		if info.IsDir() {
			_ = filepath.WalkDir(base, func(pth string, d os.DirEntry, err error) error {
				if err != nil || d.IsDir() || strings.HasPrefix(d.Name(), ".") {
					return nil
				}
				if kind == "screens" && filepath.Dir(pth) != base {
					// A screen is a top-level file or folder of the prototype.
					return nil
				}
				paths = append(paths, pth)
				return nil
			})
			if kind == "screens" {
				ents, _ := os.ReadDir(base)
				for _, e := range ents {
					if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
						paths = append(paths, filepath.Join(base, e.Name()))
					}
				}
			}
		} else {
			paths = []string{base}
		}
		sort.Strings(paths)
		for _, pth := range paths {
			rel, _ := filepath.Rel(root, pth)
			name := strings.TrimSuffix(filepath.Base(pth), filepath.Ext(pth))
			title := strings.ReplaceAll(strings.ReplaceAll(name, "-", " "), "_", " ")
			if kind == "screens" {
				title = "Screen: " + title
			}
			out = append(out, InventoryEntry{Title: title, Area: domain.Slug(name, 30), Spec: filepath.ToSlash(rel),
				Body: fmt.Sprintf("Inventory of the goal from %s.\n\n[NEEDS CLARIFICATION: analyst writes EARS criteria for %s]", spec, filepath.ToSlash(rel))})
		}
	case "lines":
		data, err := os.ReadFile(filepath.Join(root, arg))
		if err != nil {
			return nil, fmt.Errorf("%w: inventory source %s: %v", ErrInvalid, arg, err)
		}
		for l := range strings.SplitSeq(string(data), "\n") {
			l = strings.TrimSpace(strings.TrimLeft(l, "-*# "))
			if l == "" {
				continue
			}
			out = append(out, InventoryEntry{Title: l, Spec: arg, Body: "Inventory line from " + arg + ".\n\n[NEEDS CLARIFICATION: analyst writes EARS criteria]"})
		}
	default:
		return nil, fmt.Errorf("%w: inventory %q: use \"screens <dir>\", \"files <path>\" or \"lines <file>\"", ErrInvalid, spec)
	}
	return out, nil
}

// commentBody is a proposal comment without its "who (date): " prefix.
func commentBody(c string) string {
	if i := strings.Index(c, "): "); i >= 0 && strings.Contains(c[:i], " (") {
		return c[i+3:]
	}
	return c
}

// dedupeComments keeps the first comment of each body.
func dedupeComments(comments []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(comments))
	for _, c := range comments {
		if b := commentBody(c); !seen[b] {
			seen[b] = true
			out = append(out, c)
		}
	}
	return out
}
