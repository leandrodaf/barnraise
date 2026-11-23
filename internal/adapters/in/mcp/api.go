package mcp

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/leandrodaf/barnraise/gen/barnraise/v1"
	"github.com/leandrodaf/barnraise/gen/barnraise/v1/barnraisev1connect"
)

// API is the part of the daemon API the MCP server uses. The real one is
// barnraisev1connect.BarnraiseServiceClient (wrapped by internal/client).
type API interface {
	NextItem(context.Context, *connect.Request[pb.NextItemRequest]) (*connect.Response[pb.Assignment], error)
	Deliver(context.Context, *connect.Request[pb.DeliverRequest]) (*connect.Response[pb.ItemResponse], error)
	SubmitReview(context.Context, *connect.Request[pb.ReviewRequest]) (*connect.Response[pb.ItemResponse], error)
	Propose(context.Context, *connect.Request[pb.ProposeRequest]) (*connect.Response[pb.ProposeResponse], error)
	AskOwner(context.Context, *connect.Request[pb.AskOwnerRequest]) (*connect.Response[pb.QuestionResponse], error)
	Answer(context.Context, *connect.Request[pb.AnswerRequest]) (*connect.Response[pb.QuestionResponse], error)
	SendMessage(context.Context, *connect.Request[pb.SendMessageRequest]) (*connect.Response[pb.MessageResponse], error)
	Status(context.Context, *connect.Request[pb.ProjectRef]) (*connect.Response[pb.StatusResponse], error)
	Context(context.Context, *connect.Request[pb.ContextRequest]) (*connect.Response[pb.ContextResponse], error)
	Find(context.Context, *connect.Request[pb.FindRequest]) (*connect.Response[pb.FindResponse], error)
	Approve(context.Context, *connect.Request[pb.ApproveRequest]) (*connect.Response[pb.Empty], error)
	SetFocus(context.Context, *connect.Request[pb.FocusRequest]) (*connect.Response[pb.FocusResponse], error)
	Pause(context.Context, *connect.Request[pb.PauseRequest]) (*connect.Response[pb.Empty], error)
	Resume(context.Context, *connect.Request[pb.ProjectRef]) (*connect.Response[pb.Empty], error)
	CreateItem(context.Context, *connect.Request[pb.CreateItemRequest]) (*connect.Response[pb.CreateItemResponse], error)
}

// The generated client satisfies API.
var _ API = barnraisev1connect.BarnraiseServiceClient(nil)
