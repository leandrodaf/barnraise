package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	pb "github.com/leandrodaf/barnraise/gen/barnraise/v1"
	"github.com/leandrodaf/barnraise/internal/adapters/out/sqlite"
	"github.com/leandrodaf/barnraise/internal/app"
	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
	"github.com/leandrodaf/barnraise/internal/testkit"
)

type stubProber struct{}

func (stubProber) Probe(context.Context, ports.ProbeRequest) domain.MachineProbe {
	return domain.MachineProbe{OS: "linux", Arch: "amd64", ProbedAt: time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC),
		Tools: map[string]domain.ToolProbe{"git": {Name: "git", Found: true, Version: "2.43.0"}}, LandlockABI: 6}
}
func (stubProber) Invalidate() {}

type stubInstaller struct{}

func (stubInstaller) Install(_ context.Context, id string) domain.ToolResult {
	return domain.ToolResult{Tool: id, OK: true, Changed: true, Version: "1"}
}
func (stubInstaller) Remove(_ context.Context, id string) domain.ToolResult {
	return domain.ToolResult{Tool: id, OK: true}
}
func (stubInstaller) BinDir() string { return "/x" }

func capServer(t *testing.T, opts Options) (*fixture, *app.Service) {
	t.Helper()
	st, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := app.New(app.Deps{Store: st, Clock: testkit.NewClock(time.Now()), IDs: testkit.NewIDs("e"), Log: log, Machine: "m1",
		DataDir: t.TempDir(), Prober: stubProber{}, Installer: stubInstaller{}})
	opts.OwnerToken, opts.Store, opts.Log = "owner-secret", st, log
	hs := httptest.NewServer(New(svc, opts).Handler())
	t.Cleanup(hs.Close)
	return &fixture{srv: hs, svc: svc, store: st}, svc
}

func TestCapabilitiesEndpoint(t *testing.T) {
	f, svc := capServer(t, Options{})
	ctx := context.Background()
	owner := f.client("owner-secret")
	res, err := owner.Capabilities(ctx, connect.NewRequest(&pb.CapabilitiesRequest{Refresh: true}))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Msg.Machines) != 1 {
		t.Fatalf("%+v", res.Msg)
	}
	m := res.Msg.Machines[0]
	if m.Machine != "m1" || m.Platform != "linux/amd64" || !strings.HasPrefix(m.ProbedAt, "2026-11-24T12:00:00") || len(m.Items) == 0 || len(m.Tools) == 0 || m.Counts["active"] == 0 {
		t.Fatalf("report %+v", m)
	}
	var landlock *pb.CapabilityItem
	for _, it := range m.Items {
		if it.Id == "sandbox.landlock" {
			landlock = it
		}
		if it.Id == "merge.structural" && (it.Tool != "mergiraf" || it.Fix != "barnraise tools install mergiraf" || it.Status != "degraded") {
			t.Fatalf("structural %+v", it)
		}
	}
	if landlock == nil || landlock.Status != "active" || landlock.Version != "ABI 6" {
		t.Fatalf("landlock %+v", landlock)
	}
	if _, err := owner.Capabilities(ctx, connect.NewRequest(&pb.CapabilitiesRequest{Machines: []string{"m2"}})); code(err) != connect.CodeUnimplemented {
		t.Fatalf("peers without fan-out: %v", err)
	}
	inst, err := owner.InstallTools(ctx, connect.NewRequest(&pb.InstallToolsRequest{Tools: []string{"rtk"}, Machines: []string{"m1"}}))
	if err != nil || len(inst.Msg.Results) != 1 || !inst.Msg.Results[0].Ok || inst.Msg.Results[0].Machine != "m1" || !inst.Msg.Results[0].Changed {
		t.Fatalf("install %+v %v", inst, err)
	}
	if _, err := owner.InstallTools(ctx, connect.NewRequest(&pb.InstallToolsRequest{})); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("no tools: %v", err)
	}
	if _, err := owner.InstallTools(ctx, connect.NewRequest(&pb.InstallToolsRequest{Tools: []string{"rtk"}, Machines: []string{"m9"}})); code(err) != connect.CodeUnimplemented {
		t.Fatalf("peer: %v", err)
	}
	audit, _ := f.store.ListAudit(ctx, sqlite.AuditQuery{})
	if len(audit) == 0 || audit[0].Action != "machine.tools.install" {
		t.Fatalf("audit %+v", audit)
	}
	// A session token never reaches them, whatever its scopes.
	tok := svc.Tokens().Issue(app.Actor{Kind: app.ActorAgent, Name: "dev-1", Role: "implementer", Scopes: app.AllScopes, Session: "s"}, time.Hour)
	agent := f.client(tok)
	if _, err := agent.Capabilities(ctx, connect.NewRequest(&pb.CapabilitiesRequest{})); code(err) != connect.CodePermissionDenied {
		t.Fatalf("session capabilities: %v", err)
	}
	if _, err := agent.InstallTools(ctx, connect.NewRequest(&pb.InstallToolsRequest{Tools: []string{"rtk"}})); code(err) != connect.CodePermissionDenied {
		t.Fatalf("session install: %v", err)
	}
}

func TestCapabilitiesFanOutOption(t *testing.T) {
	var gotActor app.Actor
	f, _ := capServer(t, Options{
		Capabilities: func(_ context.Context, r *pb.CapabilitiesRequest) (*pb.CapabilitiesResponse, error) {
			return &pb.CapabilitiesResponse{Machines: []*pb.MachineCapabilities{{Machine: "m1"}, {Machine: "m2", Error: "unreachable"}}}, nil
		},
		InstallTools: func(_ context.Context, a app.Actor, r *pb.InstallToolsRequest) (*pb.InstallToolsResponse, error) {
			gotActor = a
			return &pb.InstallToolsResponse{Results: []*pb.ToolInstallResult{{Machine: "m2", Tool: r.Tools[0], Ok: true, Changed: true}}}, nil
		},
	})
	ctx := context.Background()
	owner := f.client("owner-secret")
	res, err := owner.Capabilities(ctx, connect.NewRequest(&pb.CapabilitiesRequest{All: true}))
	if err != nil || len(res.Msg.Machines) != 2 || res.Msg.Machines[1].Error == "" {
		t.Fatalf("%+v %v", res, err)
	}
	if _, err := owner.InstallTools(ctx, connect.NewRequest(&pb.InstallToolsRequest{Tools: []string{"rtk"}, All: true})); err != nil || gotActor.Kind != app.ActorOwner {
		t.Fatalf("%v %+v", err, gotActor)
	}
}

func TestMachineCountsFitTheProto(t *testing.T) {
	m := &pb.Machine{}
	if err := ToPB(domain.Machine{Name: "m1", CapabilityCounts: map[string]int{"active": 3, "missing": 1}}, m); err != nil {
		t.Fatal(err)
	}
	if m.CapabilityCounts["active"] != 3 || m.CapabilityCounts["missing"] != 1 {
		t.Fatalf("%+v", m)
	}
	if r := CapabilityReportPB(domain.CapabilityReport{Machine: "m1", Items: []domain.CapabilityItem{{ID: "x", NeedsRoot: true}}}); !r.Items[0].NeedsRoot {
		t.Fatalf("%+v", r)
	}
}
