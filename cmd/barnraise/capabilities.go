package main

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/nats-io/nats.go"

	pb "github.com/leandrodaf/barnraise/gen/barnraise/v1"
	"github.com/leandrodaf/barnraise/internal/adapters/in/httpapi"
	natsx "github.com/leandrodaf/barnraise/internal/adapters/out/nats"
	"github.com/leandrodaf/barnraise/internal/app"
	"github.com/leandrodaf/barnraise/internal/daemoncfg"
	"github.com/leandrodaf/barnraise/internal/domain"
)

// What runs on each machine (ADR-0045): this daemon answers for itself and
// asks its peers over NATS, the way standby and stop are passed on.

// Timeouts of the requests to a peer.
const (
	peerCapabilitiesTimeout = 45 * time.Second
	peerToolsTimeout        = 20 * time.Minute // downloads of up to a few hundred MB
	capabilityRefresh       = 10 * time.Minute
)

// remoteCapabilities is what one daemon asks another on "capabilities".
type remoteCapabilities struct {
	Project string `json:"project,omitempty"`
	Refresh bool   `json:"refresh,omitempty"`
	From    string `json:"from,omitempty"`
}

// remoteTools is what one daemon asks another on "tools".
type remoteTools struct {
	Tools  []string `json:"tools"`
	Remove bool     `json:"remove,omitempty"`
	From   string   `json:"from,omitempty"`
}

// machineSettings are the switches of this machine, read again from
// daemon.yaml so an edit counts without a restart.
func (d *daemon) machineSettings() domain.MachineSettings {
	df := d.df
	if cur, err := daemoncfg.Load(); err == nil {
		df = cur
	}
	return df.CapabilitySettings(d.opts.Egress)
}

// targets splits the requested machines into this one and its peers.
func (d *daemon) targets(machines []string, all bool) (bool, []string) {
	if all {
		var peers []string
		for _, p := range d.peers() {
			if p.Connected {
				peers = append(peers, p.Name)
			}
		}
		return true, peers
	}
	if len(machines) == 0 {
		return true, nil
	}
	self := false
	var peers []string
	for _, m := range machines {
		switch {
		case m == d.df.Machine:
			self = true
		case m != "" && !slices.Contains(peers, m):
			peers = append(peers, m)
		}
	}
	return self, peers
}

func peerError(err error, what string) string {
	if errors.Is(err, nats.ErrNoResponders) {
		return "it runs an older Barnraise without " + what + "; update it (barnraise update) or run the command there"
	}
	return err.Error()
}

// capabilities answers the Capabilities call.
func (d *daemon) capabilities(ctx context.Context, r *pb.CapabilitiesRequest) (*pb.CapabilitiesResponse, error) {
	self, peers := d.targets(r.Machines, r.All)
	out := &pb.CapabilitiesResponse{}
	if self {
		rep, err := d.svc.Capabilities(ctx, r.Project, r.Refresh)
		if err != nil {
			out.Machines = append(out.Machines, &pb.MachineCapabilities{Machine: d.df.Machine, Platform: platform(), Error: err.Error()})
		} else {
			out.Machines = append(out.Machines, httpapi.CapabilityReportPB(rep))
		}
	}
	body, _ := json.Marshal(remoteCapabilities{Project: r.Project, Refresh: r.Refresh, From: d.df.Machine})
	res := make([]*pb.MachineCapabilities, len(peers))
	var wg sync.WaitGroup
	for i, name := range peers {
		wg.Go(func() {
			res[i] = &pb.MachineCapabilities{Machine: name}
			if d.repl == nil {
				res[i].Error = "this daemon has no connection to other machines"
				return
			}
			data, err := d.repl.Request(ctx, name, natsx.RPCSubject(name, "capabilities"), body, peerCapabilitiesTimeout, 0)
			if err != nil {
				res[i].Error = peerError(err, "capability reports")
				return
			}
			var rep domain.CapabilityReport
			if err := json.Unmarshal(data, &rep); err != nil {
				res[i].Error = "unreadable answer: " + err.Error()
				return
			}
			res[i] = httpapi.CapabilityReportPB(rep)
		})
	}
	wg.Wait()
	out.Machines = append(out.Machines, res...)
	return out, nil
}

// installTools answers the InstallTools call.
func (d *daemon) installTools(ctx context.Context, a app.Actor, r *pb.InstallToolsRequest) (*pb.InstallToolsResponse, error) {
	self, peers := d.targets(r.Machines, r.All)
	out := &pb.InstallToolsResponse{}
	if self {
		out.Results = append(out.Results, httpapi.ToolResultsPB(d.svc.InstallTools(ctx, a, r.Tools, r.Remove))...)
		go d.refreshCapabilities(context.WithoutCancel(ctx))
	}
	body, _ := json.Marshal(remoteTools{Tools: r.Tools, Remove: r.Remove, From: d.df.Machine})
	res := make([][]*pb.ToolInstallResult, len(peers))
	var wg sync.WaitGroup
	for i, name := range peers {
		wg.Go(func() {
			fail := func(msg string) {
				for _, t := range r.Tools {
					res[i] = append(res[i], &pb.ToolInstallResult{Machine: name, Tool: t, Error: msg})
				}
			}
			if d.repl == nil {
				fail("this daemon has no connection to other machines")
				return
			}
			data, err := d.repl.Request(ctx, name, natsx.RPCSubject(name, "tools"), body, peerToolsTimeout, 0)
			if err != nil {
				fail(peerError(err, "tool installs"))
				return
			}
			var got []domain.ToolResult
			if err := json.Unmarshal(data, &got); err != nil {
				fail("unreadable answer: " + err.Error())
				return
			}
			res[i] = httpapi.ToolResultsPB(got)
		})
	}
	wg.Wait()
	for _, r := range res {
		out.Results = append(out.Results, r...)
	}
	return out, nil
}

// serveRemoteCapabilities answers the peers' capability and tool requests.
func (d *daemon) serveRemoteCapabilities(ctx context.Context) {
	b := d.bus.v.Load()
	if b == nil {
		return
	}
	go func() {
		_ = natsx.Serve(ctx, b.Conn(), natsx.RPCSubject(d.df.Machine, "capabilities"), func(ctx context.Context, data []byte) ([]byte, error) {
			var rc remoteCapabilities
			if err := json.Unmarshal(data, &rc); err != nil {
				return nil, err
			}
			rep, err := d.svc.Capabilities(ctx, rc.Project, rc.Refresh)
			if err != nil {
				return nil, err
			}
			return json.Marshal(rep)
		})
	}()
	_ = natsx.Serve(ctx, b.Conn(), natsx.RPCSubject(d.df.Machine, "tools"), func(ctx context.Context, data []byte) ([]byte, error) {
		var rt remoteTools
		if err := json.Unmarshal(data, &rt); err != nil {
			return nil, err
		}
		d.log.Info("a paired machine asked for tools here", "from", rt.From, "tools", rt.Tools, "remove", rt.Remove)
		res := d.svc.InstallTools(ctx, app.Actor{Kind: app.ActorOwner, Name: "owner@" + rt.From}, rt.Tools, rt.Remove)
		go d.refreshCapabilities(context.WithoutCancel(ctx))
		return json.Marshal(res)
	})
}

// refreshCapabilities probes this machine again for the counts of its
// heartbeat.
func (d *daemon) refreshCapabilities(ctx context.Context) {
	if _, err := d.svc.Capabilities(ctx, "", true); err != nil {
		d.log.Debug("capabilities", "err", err)
		return
	}
	d.machine.Store(nil) // the next heartbeat carries the new counts
}

// watchCapabilities keeps the heartbeat's counts fresh, and says in the
// log what is switched on but cannot run as designed here.
func (d *daemon) watchCapabilities(ctx context.Context) {
	rep, err := d.svc.Capabilities(ctx, "", true)
	if err == nil {
		for _, it := range rep.Attention() {
			d.log.Warn("capability "+it.Status, "id", it.ID, "why", it.Reason, "fix", it.Fix)
		}
		if len(rep.Attention()) > 0 {
			d.log.Warn("run `barnraise capabilities` to see what runs on this machine and how to fix the rest")
		}
		d.machine.Store(nil)
	}
	t := time.NewTicker(capabilityRefresh)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			d.refreshCapabilities(ctx)
		}
	}
}
