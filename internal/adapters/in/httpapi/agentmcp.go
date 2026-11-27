package httpapi

import (
	"context"
	"time"

	"connectrpc.com/connect"

	pb "github.com/leandrodaf/barnraise/gen/barnraise/v1"
	"github.com/leandrodaf/barnraise/internal/app"
	"github.com/leandrodaf/barnraise/internal/domain"
)

// ListAgentMCP lists the MCP servers of the machines and what agents get.
func (h *Handler) ListAgentMCP(ctx context.Context, r *connect.Request[pb.ProjectRef]) (*connect.Response[pb.AgentMCPResponse], error) {
	out, err := h.agentMCP(ctx, r.Msg.Project)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(out), nil
}

// SetAgentMCP lets agents use an MCP server, or no longer.
func (h *Handler) SetAgentMCP(ctx context.Context, r *connect.Request[pb.AgentMCPGrantRequest]) (*connect.Response[pb.AgentMCPResponse], error) {
	m := r.Msg
	g := domain.AgentMCPGrant{Name: m.Name, Allowed: m.Allowed, Roles: m.Roles, Hosts: m.Hosts}
	if err := h.svc().SetAgentMCP(ctx, m.Project, actorFrom(ctx), g); err != nil {
		return nil, connectErr(err)
	}
	state := "off"
	if m.Allowed {
		state = "on"
	}
	h.audit(ctx, "agent_mcp."+state, m.Name, m.Project, "")
	out, err := h.agentMCP(ctx, m.Project)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(out), nil
}

// TestAgentMCP tests an MCP server on every connected machine.
func (h *Handler) TestAgentMCP(ctx context.Context, r *connect.Request[pb.AgentMCPTestRequest]) (*connect.Response[pb.AgentMCPTestResponse], error) {
	if err := actorFrom(ctx).Require(app.ScopeAdmin, r.Msg.Project); err != nil {
		return nil, connectErr(err)
	}
	if _, err := h.svc().Project(r.Msg.Project); err != nil {
		return nil, connectErr(err)
	}
	if h.srv.opts.TestAgentMCP == nil {
		st, found, err := h.svc().TestMCP(ctx, r.Msg.Project, r.Msg.Name)
		if err != nil {
			return nil, connectErr(err)
		}
		return connect.NewResponse(&pb.AgentMCPTestResponse{Machines: []*pb.AgentMCPStatus{MCPStatusPB("", found, st)}}), nil
	}
	out, err := h.srv.opts.TestAgentMCP(ctx, r.Msg.Project, r.Msg.Name)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return connect.NewResponse(out), nil
}

func (h *Handler) agentMCP(ctx context.Context, project string) (*pb.AgentMCPResponse, error) {
	roles, servers, err := h.svc().AgentMCPView(ctx, project, actorFrom(ctx))
	if err != nil {
		return nil, connectErr(err)
	}
	out := &pb.AgentMCPResponse{Project: project, Roles: roles}
	for _, s := range servers {
		x := &pb.AgentMCPServer{Name: s.Name, Kind: s.Kind, Summary: s.Summary, FromYaml: s.FromYAML, Allowed: s.Allowed, Roles: s.Roles, Hosts: s.Hosts}
		for _, m := range s.Machines {
			x.Machines = append(x.Machines, MCPStatusPB(m.Machine, m.Found, m.Status))
		}
		out.Servers = append(out.Servers, x)
	}
	return out, nil
}

// MCPStatusPB is one machine's status of one server, for the API.
func MCPStatusPB(machine string, found bool, st domain.MCPStatus) *pb.AgentMCPStatus {
	x := &pb.AgentMCPStatus{Machine: machine, Found: found, Source: st.Source, Ok: st.OK, Tools: st.Tools, Error: st.Error}
	if !st.TestedAt.IsZero() {
		x.TestedAt = st.TestedAt.UTC().Format(time.RFC3339)
	}
	return x
}
