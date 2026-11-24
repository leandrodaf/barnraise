package app

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// What runs on this machine (ADR-0046): the capability catalog evaluated
// against a probe of the machine and the projects it drives, and the
// rootless installs of the optional tools.

// ErrNoProbe is returned when the daemon was built without a probe.
var ErrNoProbe = errors.New("this daemon cannot probe its machine")

// capState is the last report, for the machine's heartbeat.
type capState struct {
	mu       sync.Mutex
	settings func() domain.MachineSettings
	counts   map[string]int
}

// SetMachineSettings tells the evaluation the machine's own switches
// (daemon.yaml, the egress proxy).
func (s *Service) SetMachineSettings(f func() domain.MachineSettings) {
	s.caps.mu.Lock()
	s.caps.settings = f
	s.caps.mu.Unlock()
}

func (s *Service) machineSettings() domain.MachineSettings {
	s.caps.mu.Lock()
	f := s.caps.settings
	s.caps.mu.Unlock()
	if f == nil {
		return domain.MachineSettings{}
	}
	return f()
}

// CapabilityCounts are the statuses of the last report (nil before one).
func (s *Service) CapabilityCounts() map[string]int {
	s.caps.mu.Lock()
	defer s.caps.mu.Unlock()
	if s.caps.counts == nil {
		return nil
	}
	out := make(map[string]int, len(s.caps.counts))
	for k, v := range s.caps.counts {
		out[k] = v
	}
	return out
}

// capabilityConfigs are the projects evaluated: the named one, or every
// project of the machine when the name is empty or not loaded here (a
// peer asked about a project this machine does not drive).
func (s *Service) capabilityConfigs(project string) []*domain.ProjectConfig {
	if project != "" {
		if p, err := s.Project(project); err == nil {
			return []*domain.ProjectConfig{p.Cfg()}
		}
	}
	var out []*domain.ProjectConfig
	for _, name := range s.Projects() {
		if p, err := s.Project(name); err == nil {
			out = append(out, p.Cfg())
		}
	}
	return out
}

// Capabilities is this machine's report.
func (s *Service) Capabilities(ctx context.Context, project string, refresh bool) (domain.CapabilityReport, error) {
	if s.d.Prober == nil {
		return domain.CapabilityReport{}, ErrNoProbe
	}
	cfgs := s.capabilityConfigs(project)
	settings := s.machineSettings()
	probe := s.d.Prober.Probe(ctx, ports.ProbeRequest{Programs: domain.CapabilityPrograms(cfgs, settings), Refresh: refresh})
	probe.Logins = s.accountLogins(probe.Logins)
	rep := domain.BuildCapabilityReport(s.d.Machine, domain.CapabilityInput{Probe: probe, Settings: settings, Projects: cfgs})
	if project == "" {
		s.caps.mu.Lock()
		s.caps.counts = rep.Counts
		s.caps.mu.Unlock()
	}
	return rep, nil
}

// accountLogins adds to what the probe saw the providers with an account in
// the rotation that this machine runs and whose login folder is here: an
// account kept outside Barnraise's own folder (--config-dir) is logged in too.
func (s *Service) accountLogins(seen map[string]bool) map[string]bool {
	out := maps.Clone(seen)
	if out == nil {
		out = map[string]bool{}
	}
	home, _ := os.UserHomeDir()
	own := map[string]string{"claude": ".claude", "codex": ".codex", "gemini": ".gemini"}
	for _, name := range s.Projects() {
		p, err := s.Project(name)
		if err != nil {
			continue
		}
		for _, a := range p.Snapshot().AccountList() {
			if out[a.Provider] || !a.UsableOn(s.d.Machine) || a.Status == domain.AccountExpired || a.Status == domain.AccountOff {
				continue
			}
			dir := a.DirOn(s.d.Machine, s.d.DataDir)
			if dir == "" && own[a.Provider] != "" && home != "" {
				dir = filepath.Join(home, own[a.Provider])
			}
			if dir == "" {
				continue
			}
			if ents, err := os.ReadDir(dir); err == nil && len(ents) > 0 {
				out[a.Provider] = true
			}
		}
	}
	return out
}

// InstallTools installs (or, with remove, removes) tools on this machine.
// Each change, and each failure, is an event in every project of the
// machine; the probe forgets what it knew, so the next report sees it.
func (s *Service) InstallTools(ctx context.Context, a Actor, tools []string, remove bool) []domain.ToolResult {
	if len(tools) == 0 {
		return []domain.ToolResult{{Machine: s.d.Machine, Error: "name the tools: " + strings.Join(domain.ToolIDs(), ", ")}}
	}
	out := make([]domain.ToolResult, 0, len(tools))
	for _, id := range tools {
		var res domain.ToolResult
		switch {
		case s.d.Installer == nil:
			res = domain.ToolResult{Tool: id, Error: "this daemon cannot install tools"}
		case remove:
			res = s.d.Installer.Remove(ctx, id)
		default:
			res = s.d.Installer.Install(ctx, id)
		}
		res.Machine = s.d.Machine
		out = append(out, res)
		s.recordTool(ctx, a, res, remove)
	}
	if s.d.Prober != nil {
		s.d.Prober.Invalidate()
	}
	return out
}

// recordTool writes the event of one install or removal.
func (s *Service) recordTool(ctx context.Context, a Actor, res domain.ToolResult, remove bool) {
	typ, note := domain.EvMachineToolInstalled, "installed "+res.Tool+" "+res.Version
	switch {
	case res.Error != "":
		typ, note = domain.EvMachineToolFailed, res.Error
	case !res.Changed:
		return
	case remove:
		typ, note = domain.EvMachineToolRemoved, "removed "+res.Tool
	case res.Previous != "":
		note = fmt.Sprintf("upgraded %s from %s to %s", res.Tool, res.Previous, res.Version)
	}
	action := "install"
	if remove {
		action = "remove"
	}
	extra := map[string]any{"machine": s.d.Machine, "tool": res.Tool, "version": res.Version, "action": action}
	if res.Previous != "" {
		extra["previous"] = res.Previous
	}
	if res.Path != "" {
		extra["path"] = res.Path
	}
	for _, name := range s.Projects() {
		p, err := s.Project(name)
		if err != nil {
			continue
		}
		if _, err := s.emit(ctx, p, typ, res.Tool, actorName(a), domain.Payload{Note: note, Extra: extra}); err != nil {
			s.d.Log.Warn("tool event not recorded", "project", name, "tool", res.Tool, "err", err)
		}
	}
	s.d.Log.Info("tools", "action", action, "tool", res.Tool, "version", res.Version, "ok", res.OK, "err", res.Error)
}
