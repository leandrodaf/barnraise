package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

type fakeProber struct {
	mu          sync.Mutex
	probe       domain.MachineProbe
	lastReq     ports.ProbeRequest
	invalidated int
}

func (f *fakeProber) Probe(_ context.Context, r ports.ProbeRequest) domain.MachineProbe {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastReq = r
	return f.probe
}

func (f *fakeProber) Invalidate() {
	f.mu.Lock()
	f.invalidated++
	f.mu.Unlock()
}

type fakeInstaller struct{ calls []string }

func (f *fakeInstaller) Install(_ context.Context, id string) domain.ToolResult {
	f.calls = append(f.calls, "install "+id)
	switch id {
	case "rtk":
		return domain.ToolResult{Tool: id, OK: true, Changed: true, Version: "1.2.1", Path: "/d/tools/bin/rtk"}
	case "mergiraf":
		return domain.ToolResult{Tool: id, OK: true, Changed: true, Version: "0.20.0", Previous: "0.19.0"}
	case "ast-grep":
		return domain.ToolResult{Tool: id, OK: true, Version: "0.51.3"} // already there
	}
	return domain.ToolResult{Tool: id, Error: "unknown tool " + id}
}

func (f *fakeInstaller) Remove(_ context.Context, id string) domain.ToolResult {
	f.calls = append(f.calls, "remove "+id)
	return domain.ToolResult{Tool: id, OK: true, Changed: true, Version: "1.2.1"}
}

func (f *fakeInstaller) BinDir() string { return "/d/tools/bin" }

func TestCapabilitiesReport(t *testing.T) {
	e := newEnv(t)
	if _, err := e.svc.Capabilities(e.ctx, "", false); !errors.Is(err, ErrNoProbe) {
		t.Fatalf("no probe: %v", err)
	}
	if e.svc.CapabilityCounts() != nil {
		t.Fatal("no counts before a report")
	}
	fp := &fakeProber{probe: domain.MachineProbe{OS: "linux", Arch: "amd64", Tools: map[string]domain.ToolProbe{
		"git": {Name: "git", Found: true, Version: "2.43.0"}}, LandlockABI: 5}}
	e.svc.d.Prober = fp
	e.svc.SetMachineSettings(func() domain.MachineSettings {
		return domain.MachineSettings{Egress: "127.0.0.1:7421", NetworkPlugin: "netplug"}
	})
	e.p.Cfg().Plugins = map[string]domain.PluginDef{"jira": {Command: "jira-plug"}}
	rep, err := e.svc.Capabilities(e.ctx, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Machine != e.machine || rep.Platform != "linux/amd64" || len(rep.Items) == 0 || len(rep.Tools) == 0 {
		t.Fatalf("report %+v", rep)
	}
	if !fp.lastReq.Refresh || !strings.Contains(strings.Join(fp.lastReq.Programs, " "), "jira-plug") ||
		!strings.Contains(strings.Join(fp.lastReq.Programs, " "), "netplug") {
		t.Fatalf("probe request %+v", fp.lastReq)
	}
	items := map[string]domain.CapabilityItem{}
	for _, it := range rep.Items {
		items[it.ID] = it
	}
	if items["egress.proxy"].Status != domain.CapActive || items["plugins.jira"].Status != domain.CapMissing {
		t.Fatalf("items %+v %+v", items["egress.proxy"], items["plugins.jira"])
	}
	counts := e.svc.CapabilityCounts()
	if counts[domain.CapActive] == 0 || counts[domain.CapMissing] == 0 {
		t.Fatalf("counts %v", counts)
	}
	counts[domain.CapActive] = -1
	if e.svc.CapabilityCounts()[domain.CapActive] == -1 {
		t.Fatal("counts are a copy")
	}
	// A named project: its own switches; an unknown one: every project.
	if _, err := e.svc.Capabilities(e.ctx, e.p.Name, false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Capabilities(e.ctx, "elsewhere", false); err != nil {
		t.Fatal(err)
	}
	if e.svc.machineSettings().Egress == "" {
		t.Fatal("settings")
	}
}

func TestInstallToolsRecordsEvents(t *testing.T) {
	e := newEnv(t)
	if res := e.svc.InstallTools(e.ctx, Actor{Kind: ActorOwner}, nil, false); len(res) != 1 || !strings.Contains(res[0].Error, "name the tools") {
		t.Fatalf("no tools: %+v", res)
	}
	if res := e.svc.InstallTools(e.ctx, Actor{Kind: ActorOwner}, []string{"rtk"}, false); res[0].OK || !strings.Contains(res[0].Error, "cannot install") {
		t.Fatalf("no installer: %+v", res)
	}
	fi, fp := &fakeInstaller{}, &fakeProber{}
	e.svc.d.Installer, e.svc.d.Prober = fi, fp
	res := e.svc.InstallTools(e.ctx, Actor{Kind: ActorOwner, Name: "owner"}, []string{"rtk", "mergiraf", "ast-grep", "nope"}, false)
	if len(res) != 4 || !res[0].OK || res[3].OK || res[0].Machine != e.machine {
		t.Fatalf("results %+v", res)
	}
	if fp.invalidated != 1 {
		t.Fatal("the probe must forget what it knew")
	}
	rm := e.svc.InstallTools(e.ctx, Actor{Kind: ActorOwner, Name: "owner"}, []string{"rtk"}, true)
	if !rm[0].OK || fi.calls[len(fi.calls)-1] != "remove rtk" {
		t.Fatalf("remove %+v %v", rm, fi.calls)
	}
	evs := e.store.Events()
	var got []string
	for _, ev := range evs {
		if strings.HasPrefix(ev.Type, "machine.tool.") {
			pl, _ := ev.DecodePayload()
			got = append(got, ev.Type+" "+ev.Subject+" "+pl.Note)
		}
	}
	want := []string{
		"machine.tool.failed rtk this daemon cannot install tools",
		"machine.tool.installed rtk installed rtk 1.2.1",
		"machine.tool.installed mergiraf upgraded mergiraf from 0.19.0 to 0.20.0",
		"machine.tool.failed nope unknown tool nope",
		"machine.tool.removed rtk removed rtk",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("events:\n%s", strings.Join(got, "\n"))
	}
}

func TestInstalledToolsInConfinement(t *testing.T) {
	e := newEnv(t, withIsolation(Isolation{ToolDirs: []string{"/data/tools"}}))
	c, err := e.svc.confinement(e.p, domain.SandboxCheck, []string{t.TempDir()}, "true", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !domain.Contains(c.Tools, "/data/tools") {
		t.Fatalf("tools %v", c.Tools)
	}
}

func TestSessionTokensCannotReachCapabilities(t *testing.T) {
	a := Actor{Kind: ActorAgent, Project: "p", Scopes: AllScopes}
	for _, proc := range []string{"Capabilities", "InstallTools"} {
		if err := AuthorizeCall(a, proc, CallRefs{}); !errors.Is(err, ErrForbidden) {
			t.Fatalf("%s: %v", proc, err)
		}
		if err := AuthorizeCall(Actor{Kind: ActorOwner}, proc, CallRefs{}); err != nil {
			t.Fatalf("owner %s: %v", proc, err)
		}
	}
}
