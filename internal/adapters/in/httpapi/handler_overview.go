package httpapi

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/leandrodaf/barnraise/gen/barnraise/v1"
	"github.com/leandrodaf/barnraise/internal/app"
	"github.com/leandrodaf/barnraise/internal/domain"
)

// Overview answers what every item and machine of the cluster is doing.
func (h *Handler) Overview(ctx context.Context, r *connect.Request[pb.OverviewRequest]) (*connect.Response[pb.OverviewResponse], error) {
	q := app.OverviewQuery{Project: r.Msg.Project, Item: r.Msg.Item, Activity: r.Msg.Activity, Machine: r.Msg.Machine, All: r.Msg.All,
		Limit: int(r.Msg.Limit), Offset: int(r.Msg.Offset), Brief: r.Msg.Brief}
	if r.Msg.Finished != "" {
		d, err := domain.ParseDuration(r.Msg.Finished)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		q.Finished = d
	}
	out, err := h.svc().Overview(ctx, actorFrom(ctx), q)
	return respond[pb.OverviewResponse](out, err)
}
