package httpapi

import (
	"context"
	"errors"
	"strings"

	"connectrpc.com/connect"

	pb "github.com/leandrodaf/barnraise/gen/barnraise/v1"
	"github.com/leandrodaf/barnraise/internal/app"
	"github.com/leandrodaf/barnraise/internal/domain"
)

// What runs on each machine and the rootless tool installs (ADR-0046).
// Owner and admin tokens only; a session token never reaches them (they
// are not on the session allow-list of internal/app/grant.go).

// CapabilityReportPB converts one machine's report.
func CapabilityReportPB(rep domain.CapabilityReport) *pb.MachineCapabilities {
	out := &pb.MachineCapabilities{}
	if err := ToPB(rep, out); err != nil {
		return &pb.MachineCapabilities{Machine: rep.Machine, Error: err.Error()}
	}
	return out
}

// ToolResultsPB converts install results.
func ToolResultsPB(res []domain.ToolResult) []*pb.ToolInstallResult {
	out := make([]*pb.ToolInstallResult, 0, len(res))
	for _, r := range res {
		out = append(out, &pb.ToolInstallResult{Machine: r.Machine, Tool: r.Tool, Ok: r.OK, Version: r.Version, Error: r.Error, Changed: r.Changed})
	}
	return out
}

func (h *Handler) Capabilities(ctx context.Context, r *connect.Request[pb.CapabilitiesRequest]) (*connect.Response[pb.CapabilitiesResponse], error) {
	if err := actorFrom(ctx).Require(app.ScopeAdmin, ""); err != nil {
		return nil, connectErr(err)
	}
	if h.srv.opts.Capabilities != nil {
		out, err := h.srv.opts.Capabilities(ctx, r.Msg)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		return connect.NewResponse(out), nil
	}
	if len(r.Msg.Machines) > 0 || r.Msg.All {
		for _, m := range r.Msg.Machines {
			if m != h.svc().Machine() {
				return nil, connect.NewError(connect.CodeUnimplemented, errors.New("this daemon cannot ask other machines"))
			}
		}
	}
	rep, err := h.svc().Capabilities(ctx, r.Msg.Project, r.Msg.Refresh)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	return connect.NewResponse(&pb.CapabilitiesResponse{Machines: []*pb.MachineCapabilities{CapabilityReportPB(rep)}}), nil
}

func (h *Handler) InstallTools(ctx context.Context, r *connect.Request[pb.InstallToolsRequest]) (*connect.Response[pb.InstallToolsResponse], error) {
	a := actorFrom(ctx)
	if err := a.Require(app.ScopeAdmin, ""); err != nil {
		return nil, connectErr(err)
	}
	if len(r.Msg.Tools) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("name the tools: "+strings.Join(domain.ToolIDs(), ", ")))
	}
	action := "machine.tools.install"
	if r.Msg.Remove {
		action = "machine.tools.remove"
	}
	var out *pb.InstallToolsResponse
	if h.srv.opts.InstallTools != nil {
		var err error
		if out, err = h.srv.opts.InstallTools(ctx, a, r.Msg); err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	} else {
		for _, m := range r.Msg.Machines {
			if m != h.svc().Machine() {
				return nil, connect.NewError(connect.CodeUnimplemented, errors.New("this daemon cannot reach other machines"))
			}
		}
		out = &pb.InstallToolsResponse{Results: ToolResultsPB(h.svc().InstallTools(ctx, a, r.Msg.Tools, r.Msg.Remove))}
	}
	var machines []string
	for _, res := range out.Results {
		if res.Ok && res.Changed && !containsStr(machines, res.Machine) {
			machines = append(machines, res.Machine)
		}
	}
	h.audit(ctx, action, strings.Join(machines, ","), "", strings.Join(r.Msg.Tools, ","))
	return connect.NewResponse(out), nil
}

func containsStr(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
