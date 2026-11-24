package app

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// PartView is one part of a repository (a backend, a web app, a mobile app
// living side by side, each with its own barnraise.yaml) and how its work
// goes: what the owner sees on the Parts screen, decided here.
type PartView struct {
	Name      string   `json:"name"`
	Dir       string   `json:"dir,omitempty"`
	About     string   `json:"about,omitempty"`
	Stack     string   `json:"stack,omitempty"`
	Standard  string   `json:"standard,omitempty"`
	Paths     []string `json:"paths,omitempty"`
	Screens   []string `json:"screens,omitempty"`
	Contracts []string `json:"contracts,omitempty"`
	// Flows: the flow each item kind of this part goes through, and its steps.
	Flows     map[string]string    `json:"flows,omitempty"`
	FlowSteps map[string]StepNames `json:"flow_steps,omitempty"`
	// Checks that run when this part is touched.
	Checks []string `json:"checks,omitempty"`
	// ConfigFile is the part's own barnraise.yaml; Config is its text, for
	// a reader who may administer the project.
	ConfigFile string `json:"config_file,omitempty"`
	Config     string `json:"config,omitempty"`
	// Work: items by board column, the ones waiting for the owner, and what
	// the part's items cost.
	Columns          map[string]int     `json:"columns,omitempty"`
	Open             int                `json:"open"`
	NeedsOwner       int                `json:"needs_owner"`
	Usage            domain.UsageTotals `json:"usage"`
	LastIntegratedAt time.Time          `json:"last_integrated_at,omitzero"`
}

// StepNames are the steps of a flow, in order.
type StepNames struct {
	Steps []string `json:"steps"`
}

// PartsView is a project's parts and its own configuration.
type PartsView struct {
	Project    string      `json:"project"`
	ConfigFile string      `json:"config_file"`
	Config     string      `json:"config,omitempty"`
	Parts      []*PartView `json:"parts"`
}

// partConfigMax bounds the configuration text an answer carries.
const partConfigMax = 64 << 10

// Parts describes the parts of a project and how their work goes.
func (s *Service) Parts(ctx context.Context, project string, a Actor) (*PartsView, error) {
	p, err := s.Project(project)
	if err != nil {
		return nil, err
	}
	if err := a.Require(ScopeItemsRead, p.Name); err != nil {
		return nil, err
	}
	admin := a.Require(ScopeAdmin, p.Name) == nil
	cfg := p.Cfg()
	out := &PartsView{Project: p.Name, ConfigFile: "barnraise.yaml"}
	if admin {
		out.Config = readConfig(filepath.Join(cfg.Root, "barnraise.yaml"))
	}
	by := map[string]*PartView{}
	for name, ad := range cfg.Areas {
		pv := &PartView{Name: name, Dir: ad.Dir, About: ad.About, Stack: ad.Stack, Standard: ad.Standard, Paths: ad.Paths,
			Screens: ad.Screens, Contracts: ad.Contracts, Flows: map[string]string{}, FlowSteps: map[string]StepNames{}, Columns: map[string]int{}}
		// The part's own kinds first, then the project's for the kinds it leaves out.
		for kind, flow := range cfg.KindFlows {
			pv.Flows[kind] = flow
		}
		for kind, flow := range ad.KindFlows {
			pv.Flows[kind] = flow
		}
		for _, flow := range pv.Flows {
			if f := cfg.Flows[flow]; f != nil {
				var names []string
				for _, st := range f.Steps {
					names = append(names, st.Name)
				}
				pv.FlowSteps[flow] = StepNames{Steps: names}
			}
		}
		for cname, c := range cfg.Checks {
			if len(c.Areas) == 0 || slices.Contains(c.Areas, name) {
				pv.Checks = append(pv.Checks, cname)
			}
		}
		sort.Strings(pv.Checks)
		if ad.Dir != "" {
			pv.ConfigFile = filepath.ToSlash(filepath.Join(ad.Dir, "barnraise.yaml"))
			if admin {
				pv.Config = readConfig(filepath.Join(cfg.Root, ad.Dir, "barnraise.yaml"))
			}
		}
		by[name] = pv
		out.Parts = append(out.Parts, pv)
	}
	sort.Slice(out.Parts, func(i, j int) bool {
		// Parts with their own folder first (they are the repository's parts), then by name.
		if (out.Parts[i].Dir != "") != (out.Parts[j].Dir != "") {
			return out.Parts[i].Dir != ""
		}
		return out.Parts[i].Name < out.Parts[j].Name
	})

	// The board's columns come from the overview, so they match the queue.
	o, err := s.Overview(ctx, a, OverviewQuery{Project: p.Name, Brief: true, Limit: overviewLimitMax})
	if err != nil {
		return nil, err
	}
	for _, po := range o.Projects {
		for _, io := range po.Items {
			pv := by[io.Area]
			if pv == nil {
				continue
			}
			pv.Columns[io.Column]++
			if io.Column != ColDone {
				pv.Open++
			}
			if io.NeedsOwner {
				pv.NeedsOwner++
			}
		}
	}
	// What each part cost, over every item it ever had.
	snap := p.Snapshot()
	for _, it := range snap.Items {
		pv := by[it.Area]
		if pv == nil {
			continue
		}
		if snap.Ledger != nil {
			if il := snap.Ledger.ByItem[it.ID]; il != nil {
				pv.Usage.Merge(il.Usage)
			}
		}
		if it.IntegratedAt.After(pv.LastIntegratedAt) {
			pv.LastIntegratedAt = it.IntegratedAt
		}
	}
	return out, nil
}

// readConfig reads a configuration file for the owner; "" when it is not on
// this machine.
func readConfig(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if len(b) > partConfigMax {
		b = b[:partConfigMax]
	}
	return string(b)
}
