package main

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/nats-io/nats.go"

	pb "github.com/leandrodaf/barnraise/gen/barnraise/v1"
	"github.com/leandrodaf/barnraise/internal/adapters/in/httpapi"
	natsx "github.com/leandrodaf/barnraise/internal/adapters/out/nats"
)

// Testing an MCP server from the panel runs it on every connected machine
// at once: each one starts or calls it with its own configuration and says
// what it got, so "it works here, not on dma" shows on one screen.

const peerMCPTestTimeout = 45 * time.Second

type remoteMCPTest struct {
	Project string `json:"project"`
	Name    string `json:"name"`
}

// testAgentMCP tests the server here and on every connected machine.
func (d *daemon) testAgentMCP(ctx context.Context, project, name string) (*pb.AgentMCPTestResponse, error) {
	if name == "" {
		return nil, errors.New("name the MCP server")
	}
	out := &pb.AgentMCPTestResponse{}
	st, found, err := d.svc.TestMCP(ctx, project, name)
	if err != nil {
		return nil, err
	}
	out.Machines = append(out.Machines, httpapi.MCPStatusPB(d.df.Machine, found, st))
	_, peers := d.targets(nil, true)
	body, _ := json.Marshal(remoteMCPTest{Project: project, Name: name})
	res := make([]*pb.AgentMCPStatus, len(peers))
	var wg sync.WaitGroup
	for i, peer := range peers {
		wg.Go(func() {
			res[i] = &pb.AgentMCPStatus{Machine: peer}
			if d.repl == nil {
				res[i].Error = "this daemon has no connection to other machines"
				return
			}
			data, err := d.repl.Request(ctx, peer, natsx.RPCSubject(peer, "mcp-test"), body, peerMCPTestTimeout, 0)
			if err != nil {
				if errors.Is(err, nats.ErrNoResponders) {
					res[i].Error = peerError(err, "MCP tests")
				} else {
					res[i].Error = err.Error()
				}
				return
			}
			var m pb.AgentMCPStatus
			if err := json.Unmarshal(data, &m); err != nil {
				res[i].Error = "unreadable answer: " + err.Error()
				return
			}
			m.Machine = peer
			res[i] = &m
		})
	}
	wg.Wait()
	out.Machines = append(out.Machines, res...)
	return out, nil
}

// serveRemoteMCPTest tests a server another machine asks about.
func (d *daemon) serveRemoteMCPTest(ctx context.Context) {
	b := d.bus.v.Load()
	if b == nil {
		return
	}
	_ = natsx.Serve(ctx, b.Conn(), natsx.RPCSubject(d.df.Machine, "mcp-test"), func(ctx context.Context, data []byte) ([]byte, error) {
		var r remoteMCPTest
		if err := json.Unmarshal(data, &r); err != nil {
			return nil, err
		}
		st, found, err := d.svc.TestMCP(ctx, r.Project, r.Name)
		if err != nil {
			return nil, err
		}
		return json.Marshal(httpapi.MCPStatusPB(d.df.Machine, found, st))
	})
}
