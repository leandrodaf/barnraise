package app

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// Find is the layered search: exact, filters, full text (and vectors when they prove useful).
func (s *Service) Find(ctx context.Context, project string, a Actor, q string, f ports.SearchFilters, limit int) ([]ports.SearchHit, error) {
	p, err := s.Project(project)
	if err != nil {
		return nil, err
	}
	if err := a.Require(ScopeItemsRead, p.Name); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 20
	}
	if strings.EqualFold(strings.TrimSpace(f.Kind), "regex") {
		// Search the files' text by a regular expression: a bad one is the
		// caller's mistake, said before the index is asked.
		pat := strings.TrimSpace(q)
		if rest, ok := strings.CutPrefix(pat, "file:"); ok {
			_, pat, _ = strings.Cut(rest, " ")
		}
		if _, err := regexp.Compile(strings.TrimSpace(pat)); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
	}
	if s.d.Knowledge == nil {
		return s.findInState(p, q, limit), nil
	}
	return s.d.Knowledge.Search(ctx, p.Name, q, f, limit)
}

// findInState is the fallback without an index: ids and titles.
func (s *Service) findInState(p *Project, q string, limit int) []ports.SearchHit {
	var out []ports.SearchHit
	ql := strings.ToLower(q)
	for _, it := range p.Snapshot().ItemList() {
		if strings.EqualFold(it.ID, q) || strings.Contains(strings.ToLower(it.Title+" "+it.Body), ql) {
			out = append(out, ports.SearchHit{ID: it.ID, Kind: "item", Title: it.Title, Area: it.Area, Tokens: domain.EstimateTokens(it.Body), Layer: "text"})
		}
		if len(out) >= limit {
			break
		}
	}
	return out
}

// Get returns the full content of an id.
func (s *Service) Get(ctx context.Context, project string, a Actor, id string) (any, error) {
	p, err := s.Project(project)
	if err != nil {
		return nil, err
	}
	if err := a.Require(ScopeItemsRead, p.Name); err != nil {
		return nil, err
	}
	snap := p.Snapshot()
	up := strings.ToUpper(id)
	if it, ok := snap.Items[up]; ok {
		return it, nil
	}
	if pr, ok := snap.Proposals[up]; ok {
		return pr, nil
	}
	if g, ok := snap.Goals[up]; ok {
		return g, nil
	}
	if q, ok := snap.Questions[up]; ok {
		return q, nil
	}
	if s.d.Knowledge != nil {
		doc, err := s.d.Knowledge.Get(ctx, p.Name, id)
		if err == nil && doc != nil {
			return doc, nil
		}
	}
	return nil, fmt.Errorf("%w: %s (no item, proposal, decision or document with that id)", ErrNotFound, id)
}

// Related lists what is linked to an id: dependencies, dependents, anchors, links.
func (s *Service) Related(ctx context.Context, project string, a Actor, id string) (map[string]any, error) {
	p, err := s.Project(project)
	if err != nil {
		return nil, err
	}
	if err := a.Require(ScopeItemsRead, p.Name); err != nil {
		return nil, err
	}
	snap := p.Snapshot()
	if it, ok := snap.Items[strings.ToUpper(id)]; ok {
		id = it.ID // "i-12" and "I-12" are the same item, in the index too
	}
	out := map[string]any{"id": id}
	if it, ok := snap.Items[id]; ok {
		var dependents, sameArea []string
		for _, o := range snap.ItemList() {
			if domain.Contains(o.Deps, it.ID) {
				dependents = append(dependents, o.ID+" "+o.Title)
			}
			if o.ID != it.ID && it.Area != "" && o.Area == it.Area {
				sameArea = append(sameArea, o.ID+" "+o.Title+" ("+string(o.Status)+")")
			}
		}
		out["depends_on"] = it.Deps
		out["dependents"] = dependents
		out["same_area"] = sameArea
		out["anchors"] = it.Anchors
		if it.PR != "" {
			out["pr"] = it.PR
		}
		if it.TrackerRef != "" {
			out["tracker"] = it.TrackerRef
		}
	}
	if s.d.Knowledge != nil {
		if links, err := s.d.Knowledge.Links(ctx, p.Name, id); err == nil {
			out["links"] = links
		}
	}
	if p.Ports().Git != nil && strings.HasPrefix(strings.ToUpper(id), "I-") {
		if commits, err := p.Ports().Git.Log(ctx, "--all", "--grep", domain.CommitTrailer(strings.ToUpper(id)), "-n", "50"); err == nil {
			var cs []string
			for _, c := range commits {
				cs = append(cs, short(c.SHA)+" "+c.Subject)
			}
			out["commits"] = cs
		}
	}
	return out, nil
}

// Why explains a line: the commit that wrote it, the items in its trailers and their decisions.
func (s *Service) Why(ctx context.Context, project string, a Actor, target string) (map[string]any, error) {
	p, err := s.Project(project)
	if err != nil {
		return nil, err
	}
	if err := a.Require(ScopeItemsRead, p.Name); err != nil {
		return nil, err
	}
	if p.Ports().Git == nil {
		return nil, fmt.Errorf("%w: no repository", ErrInvalid)
	}
	file, lineStr, ok := strings.Cut(target, ":")
	line := 1
	if ok {
		n, err := strconv.Atoi(lineStr)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("%w: use path:line", ErrInvalid)
		}
		line = n
	}
	sha, err := p.Ports().Git.Blame(ctx, file, line)
	if err != nil {
		return nil, fmt.Errorf("%w: blame %s: %v", ErrNotFound, target, err)
	}
	out := map[string]any{"target": target, "commit": sha}
	commits, err := p.Ports().Git.Log(ctx, "-n", "1", sha)
	if err == nil && len(commits) == 1 {
		c := commits[0]
		out["subject"] = c.Subject
		out["author"] = c.Author
		out["date"] = c.Time
		if id := c.Trailers["Barnraise-Item"]; id != "" {
			out["item"] = id
			if it := p.Snapshot().Items[id]; it != nil {
				out["item_title"] = it.Title
				out["decisions"] = it.Anchors.Decisions
				out["criteria"] = it.Criteria
			}
		}
	}
	if rp, ok := s.d.Knowledge.(rulesProvider); ok && s.d.Knowledge != nil {
		if docs, err := rp.Rules(p.Name, file); err == nil {
			var ids []string
			for _, d := range docs {
				ids = append(ids, d.ID+" "+d.Title)
			}
			out["rules"] = ids
		}
	}
	return out, nil
}

// History lists what happened to a symbol or path: commits and their items.
func (s *Service) History(ctx context.Context, project string, a Actor, target string) ([]map[string]any, error) {
	p, err := s.Project(project)
	if err != nil {
		return nil, err
	}
	if err := a.Require(ScopeItemsRead, p.Name); err != nil {
		return nil, err
	}
	if p.Ports().Git == nil {
		return nil, fmt.Errorf("%w: no repository", ErrInvalid)
	}
	var commits []ports.Commit
	if strings.Contains(target, "/") || strings.Contains(target, ".") && !strings.Contains(target, " ") && fileExists(p.Cfg().Root, target) {
		commits, err = p.Ports().Git.Log(ctx, "-n", "50", "--", target)
	} else {
		name := target
		if i := strings.LastIndexByte(name, '.'); i >= 0 {
			name = name[i+1:]
		}
		commits, err = p.Ports().Git.Log(ctx, "-n", "50", "-S", name)
	}
	if err != nil {
		return nil, err
	}
	snap := p.Snapshot()
	var out []map[string]any
	for _, c := range commits {
		e := map[string]any{"commit": short(c.SHA), "subject": c.Subject, "date": c.Time.Format("2006-01-02")}
		if id := c.Trailers["Barnraise-Item"]; id != "" {
			e["item"] = id
			if it := snap.Items[id]; it != nil {
				e["item_title"] = it.Title
			}
		}
		out = append(out, e)
	}
	return out, nil
}

// Rules lists the standards and decisions that apply to a path.
func (s *Service) Rules(ctx context.Context, project string, a Actor, path string) ([]ports.Doc, error) {
	p, err := s.Project(project)
	if err != nil {
		return nil, err
	}
	if err := a.Require(ScopeItemsRead, p.Name); err != nil {
		return nil, err
	}
	if rp, ok := s.d.Knowledge.(rulesProvider); ok && s.d.Knowledge != nil {
		return rp.Rules(p.Name, path)
	}
	area := p.Cfg().AreaOfFile(strings.TrimSuffix(path, "/") + "/x")
	if area == "" {
		return nil, nil
	}
	return []ports.Doc{{ID: "area:" + area, Kind: "area", Title: "Area " + area}}, nil
}

// Reindex refreshes the knowledge index after an integration (only changed files).
func (s *Service) Reindex(ctx context.Context, project string, changed []string) error {
	p, err := s.Project(project)
	if err != nil {
		return err
	}
	if s.d.Knowledge == nil {
		return nil
	}
	commit := ""
	if p.Ports().Git != nil {
		commit, _ = p.Ports().Git.MainHead(ctx)
	}
	if sc, ok := s.d.Knowledge.(symbolsConfigurer); ok {
		sc.SetSymbols(p.Name, p.Cfg().Knowledge.Symbols)
	}
	if err := s.d.Knowledge.Reindex(ctx, p.Name, p.Cfg().Root, commit, changed); err != nil {
		return err
	}
	snap := p.Snapshot()
	return s.d.Knowledge.IndexItems(ctx, p.Name, snap.ItemList(), snap.ProposalList())
}

// symbolsConfigurer is the optional knowledge extension that takes the
// project's knowledge.symbols mode.
type symbolsConfigurer interface {
	SetSymbols(project, mode string)
}

func fileExists(root, rel string) bool {
	_, err := osStat(root + "/" + rel)
	return err == nil
}

var osStat = os.Stat
