package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"

	pb "github.com/leandrodaf/barnraise/gen/barnraise/v1"
	natsx "github.com/leandrodaf/barnraise/internal/adapters/out/nats"
	"github.com/leandrodaf/barnraise/internal/daemoncfg"
	"github.com/leandrodaf/barnraise/internal/paths"
)

// maxAgentsV is the owner's limit of agents on this machine (daemon.yaml
// max_agents); 0 measures it from the machine.
var maxAgentsV atomic.Int64

func (d *daemon) maxAgents() int { return int(maxAgentsV.Load()) }

// setMaxAgents reports whether the limit changed.
func (d *daemon) setMaxAgents(n int) bool { return maxAgentsV.Swap(int64(n)) != int64(n) }

// applyMaxAgents keeps the limit in daemon.yaml and uses it at once.
func (d *daemon) applyMaxAgents(n int) error {
	cur, err := daemoncfg.Load()
	if err != nil {
		return err
	}
	if cur.MaxAgents != n {
		cur.MaxAgents = n
		if err := cur.Save(); err != nil {
			return fmt.Errorf("limit not kept in %s: %w", paths.DaemonConfig(), err)
		}
	}
	d.setMaxAgents(n)
	d.machine.Store(nil)
	return nil
}

// setMachineAgents sets the limit on this machine and, through NATS, on the
// named peers.
func (d *daemon) setMachineAgents(ctx context.Context, n int, machines []string) (*pb.MaxAgentsResponse, error) {
	self, peers := d.targets(machines, false)
	out := &pb.MaxAgentsResponse{}
	if self {
		res := &pb.PeerResult{Name: d.df.Machine, Ok: true}
		if err := d.applyMaxAgents(n); err != nil {
			res.Ok, res.Error = false, err.Error()
		}
		out.Machines = append(out.Machines, res)
	}
	res := make([]*pb.PeerResult, len(peers))
	var wg sync.WaitGroup
	for i, name := range peers {
		wg.Go(func() {
			res[i] = &pb.PeerResult{Name: name}
			if d.repl == nil {
				res[i].Error = "this daemon has no connection to other machines"
				return
			}
			_, err := d.repl.Request(ctx, name, natsx.RPCSubject(name, "agents"), []byte(strconv.Itoa(n)), 10*time.Second, 0)
			switch {
			case err == nil:
				res[i].Ok = true
			case errors.Is(err, nats.ErrNoResponders):
				res[i].Error = peerError(err, "a limit of agents set from another machine")
			default:
				res[i].Error = err.Error()
			}
		})
	}
	wg.Wait()
	out.Machines = append(out.Machines, res...)
	return out, nil
}

// serveRemoteAgents sets this machine's limit when a paired machine asks.
func (d *daemon) serveRemoteAgents(ctx context.Context) {
	b := d.bus.v.Load()
	if b == nil {
		return
	}
	_ = natsx.Serve(ctx, b.Conn(), natsx.RPCSubject(d.df.Machine, "agents"), func(_ context.Context, data []byte) ([]byte, error) {
		var n int
		if err := json.Unmarshal(data, &n); err != nil || n < 0 || n > 64 {
			return nil, errors.New("from 1 to 64 agents, or 0 to measure it from the machine")
		}
		d.log.Info("a paired machine set this machine's limit of agents", "max_agents", n)
		if err := d.applyMaxAgents(n); err != nil {
			return nil, err
		}
		return []byte("ok"), nil
	})
}
