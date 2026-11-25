package httpapi

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	pb "github.com/leandrodaf/barnraise/gen/barnraise/v1"
	"github.com/leandrodaf/barnraise/gen/barnraise/v1/barnraisev1connect"
	"github.com/leandrodaf/barnraise/internal/app"
	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// Handler implements the Connect service over the application.
type Handler struct {
	barnraisev1connect.UnimplementedBarnraiseServiceHandler
	srv *Server
}

func (h *Handler) svc() *app.Service { return h.srv.svc }

func respond[T any, PT interface {
	*T
	proto.Message
}](v any, err error) (*connect.Response[T], error) {
	if err != nil {
		return nil, connectErr(err)
	}
	out := PT(new(T))
	if v != nil {
		if err := ToPB(v, out); err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}
	return connect.NewResponse((*T)(out)), nil
}

func decode(src proto.Message, dst any) error {
	if err := FromPB(src, dst); err != nil {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	return nil
}

func empty(err error) (*connect.Response[pb.Empty], error) {
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&pb.Empty{}), nil
}

func (h *Handler) audit(ctx context.Context, action, target, project, detail string) {
	h.srv.audit(ctx, action, target, project, detail)
}

// ---------- projects ----------

func (h *Handler) ListProjects(ctx context.Context, _ *connect.Request[pb.Empty]) (*connect.Response[pb.ProjectsResponse], error) {
	on, why := h.svc().Runtime().Standby()
	v := h.svc().Versions()
	return connect.NewResponse(&pb.ProjectsResponse{Projects: h.svc().Projects(), Machine: h.svc().Machine(), Version: h.srv.opts.Version, Standby: on, StandbyWhy: why,
		BuildTime: rfc3339(v.Self.Time), Outdated: v.Outdated, NewestMachine: v.NewestMachine, NewestVersion: v.Newest.Version,
		NewestBuildTime: rfc3339(v.Newest.Time), SelfUpdate: v.SelfUpdate, Autopilot: h.svc().AutopilotProjects(), AdvisorDown: h.advisorDown()}), nil
}

// advisorDown says, per project, why its advisor cannot answer now.
func (h *Handler) advisorDown() map[string]string {
	out := map[string]string{}
	for _, p := range h.svc().Projects() {
		if why := h.svc().AdvisorStatus(p); why != "" {
			out[p] = why
		}
	}
	return out
}

// SetAutopilot switches the autopilot of one project, or of every project.
func (h *Handler) SetAutopilot(ctx context.Context, r *connect.Request[pb.AutopilotRequest]) (*connect.Response[pb.AutopilotResponse], error) {
	a := actorFrom(ctx)
	targets := []string{r.Msg.Project}
	if r.Msg.All {
		targets = h.svc().Projects()
	}
	out := &pb.AutopilotResponse{}
	done := 0
	for _, p := range targets {
		res := &pb.ProjectAutopilot{Project: p, On: r.Msg.On}
		if err := h.svc().SetAutopilot(ctx, p, a, r.Msg.On); err != nil {
			if !r.Msg.All {
				return nil, connectErr(err)
			}
			res.On, res.Error = !r.Msg.On, err.Error()
		} else {
			done++
		}
		out.Projects = append(out.Projects, res)
	}
	action := "autopilot.off"
	if r.Msg.On {
		action = "autopilot.on"
	}
	if done > 0 {
		h.audit(ctx, action, strings.Join(targets, ","), "", "")
	}
	return connect.NewResponse(out), nil
}

func (h *Handler) RegisterProject(ctx context.Context, r *connect.Request[pb.RegisterProjectRequest]) (*connect.Response[pb.ProjectsResponse], error) {
	a := actorFrom(ctx)
	if err := a.Require(app.ScopeAdmin, ""); err != nil {
		return nil, connectErr(err)
	}
	if h.srv.opts.Register == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("this daemon cannot register projects"))
	}
	if err := h.srv.opts.Register(ctx, r.Msg.Root, r.Msg.Paused); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	h.audit(ctx, "project.register", r.Msg.Root, "", "")
	return h.ListProjects(ctx, nil)
}

func (h *Handler) ReloadProject(ctx context.Context, r *connect.Request[pb.ProjectRef]) (*connect.Response[pb.ProjectsResponse], error) {
	a := actorFrom(ctx)
	if err := a.Require(app.ScopeAdmin, r.Msg.Project); err != nil {
		return nil, connectErr(err)
	}
	if h.srv.opts.Reload == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("reload not available"))
	}
	if err := h.srv.opts.Reload(ctx, r.Msg.Project); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return h.ListProjects(ctx, nil)
}

// ---------- items ----------

func (h *Handler) CreateItem(ctx context.Context, r *connect.Request[pb.CreateItemRequest]) (*connect.Response[pb.CreateItemResponse], error) {
	var in app.NewItem
	if err := decode(r.Msg, &in); err != nil {
		return nil, err
	}
	res, err := h.svc().CreateItem(ctx, r.Msg.Project, actorFrom(ctx), in)
	if err == nil {
		h.audit(ctx, "item.create", idOf(res.Item), r.Msg.Project, in.Title)
	}
	return respond[pb.CreateItemResponse](res, err)
}

func idOf(it *domain.Item) string {
	if it == nil {
		return ""
	}
	return it.ID
}

func (h *Handler) GetItem(ctx context.Context, r *connect.Request[pb.ItemRef]) (*connect.Response[pb.ItemResponse], error) {
	it, err := h.svc().GetItem(r.Msg.Project, actorFrom(ctx), r.Msg.Id)
	return respond[pb.ItemResponse](map[string]any{"item": it}, err)
}

func (h *Handler) ListItems(ctx context.Context, r *connect.Request[pb.ListItemsRequest]) (*connect.Response[pb.ItemsResponse], error) {
	var f app.ItemFilter
	if err := decode(r.Msg, &f); err != nil {
		return nil, err
	}
	items, err := h.svc().ListItems(r.Msg.Project, actorFrom(ctx), f)
	return respond[pb.ItemsResponse](map[string]any{"items": items}, err)
}

func (h *Handler) UpdateItem(ctx context.Context, r *connect.Request[pb.UpdateItemRequest]) (*connect.Response[pb.ItemResponse], error) {
	var patch app.ItemPatch
	if err := FromStruct(r.Msg.Patch, &patch); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	it, err := h.svc().UpdateItem(ctx, r.Msg.Project, actorFrom(ctx), r.Msg.Id, patch)
	if err == nil {
		h.audit(ctx, "item.update", it.ID, r.Msg.Project, patch.Reason)
	}
	return respond[pb.ItemResponse](map[string]any{"item": it}, err)
}

func (h *Handler) CloseItem(ctx context.Context, r *connect.Request[pb.CloseItemRequest]) (*connect.Response[pb.ItemResponse], error) {
	it, err := h.svc().CloseItem(ctx, r.Msg.Project, actorFrom(ctx), r.Msg.Id, r.Msg.Reason, r.Msg.By, r.Msg.Note)
	if err == nil {
		h.audit(ctx, "item.close", it.ID, r.Msg.Project, r.Msg.Reason)
	}
	return respond[pb.ItemResponse](map[string]any{"item": it}, err)
}

func (h *Handler) NextItem(ctx context.Context, r *connect.Request[pb.NextItemRequest]) (*connect.Response[pb.Assignment], error) {
	m := domain.Machine{}
	if h.srv.opts.Probe != nil {
		m = h.srv.opts.Probe()
	}
	a, err := h.svc().ClaimForAgent(ctx, r.Msg.Project, actorFrom(ctx), m)
	if errors.Is(err, app.ErrNoWork) || errors.Is(err, app.ErrPaused) {
		return connect.NewResponse(&pb.Assignment{None: true, Note: "No authorized work. Record your state and end your turn; do not create work."}), nil
	}
	if err != nil {
		return nil, connectErr(err)
	}
	step, err := ToStruct(a.Step)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	out := &pb.Assignment{Project: a.Project, New: a.New, Step: step, Item: &pb.Item{}}
	if err := ToPB(a.Item, out.Item); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(out), nil
}

func (h *Handler) Deliver(ctx context.Context, r *connect.Request[pb.DeliverRequest]) (*connect.Response[pb.ItemResponse], error) {
	var d domain.Delivery
	if r.Msg.Delivery != nil {
		if err := decode(r.Msg.Delivery, &d); err != nil {
			return nil, err
		}
	}
	it, err := h.svc().Deliver(ctx, r.Msg.Project, actorFrom(ctx), d)
	if err == nil {
		h.audit(ctx, "delivery.submit", it.ID, r.Msg.Project, string(d.Status))
	}
	return respond[pb.ItemResponse](map[string]any{"item": it}, err)
}

func (h *Handler) SubmitReview(ctx context.Context, r *connect.Request[pb.ReviewRequest]) (*connect.Response[pb.ItemResponse], error) {
	var rv domain.Review
	if r.Msg.Review != nil {
		if err := decode(r.Msg.Review, &rv); err != nil {
			return nil, err
		}
	}
	it, err := h.svc().Review(ctx, r.Msg.Project, actorFrom(ctx), rv)
	if err == nil {
		h.audit(ctx, "review.submit", it.ID, r.Msg.Project, string(rv.Decision))
	}
	return respond[pb.ItemResponse](map[string]any{"item": it}, err)
}

func (h *Handler) ReportCheck(ctx context.Context, r *connect.Request[pb.ReportCheckRequest]) (*connect.Response[pb.ItemResponse], error) {
	if r.Msg.Result == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("result is required"))
	}
	res := domain.CheckResult{ID: r.Msg.Result.Id, ItemID: r.Msg.Result.ItemId, Check: r.Msg.Result.Check, Commit: r.Msg.Result.Commit, Passed: r.Msg.Result.Passed,
		Flaky: r.Msg.Result.Flaky, Summary: r.Msg.Result.Summary, LogPath: r.Msg.Result.LogPath, LogHash: r.Msg.Result.LogHash, Artifacts: r.Msg.Result.Artifacts,
		Duration: time.Duration(r.Msg.Result.Duration), Criteria: r.Msg.Result.Criteria, RunsOn: r.Msg.Result.RunsOn}
	it, err := h.svc().ReportCheck(ctx, r.Msg.Project, actorFrom(ctx), res)
	if err == nil {
		h.audit(ctx, "check.report", it.ID, r.Msg.Project, fmt.Sprintf("%s passed=%v", res.Check, res.Passed))
	}
	return respond[pb.ItemResponse](map[string]any{"item": it}, err)
}

func (h *Handler) Timeline(ctx context.Context, r *connect.Request[pb.ItemRef]) (*connect.Response[pb.TimelineResponse], error) {
	t, err := h.svc().Timeline(ctx, r.Msg.Project, actorFrom(ctx), r.Msg.Id)
	return respond[pb.TimelineResponse](map[string]any{"entries": t}, err)
}

func (h *Handler) Estimate(ctx context.Context, r *connect.Request[pb.ItemRef]) (*connect.Response[pb.EstimateResponse], error) {
	e, err := h.svc().EstimateItem(ctx, r.Msg.Project, actorFrom(ctx), r.Msg.Id)
	return respond[pb.EstimateResponse](e, err)
}

func (h *Handler) Takeover(ctx context.Context, r *connect.Request[pb.ItemRef]) (*connect.Response[pb.TakeoverResponse], error) {
	dir, it, err := h.svc().Takeover(ctx, r.Msg.Project, actorFrom(ctx), r.Msg.Id)
	if err == nil {
		h.audit(ctx, "item.takeover", it.ID, r.Msg.Project, dir)
	}
	return respond[pb.TakeoverResponse](map[string]any{"item": it, "worktree": dir}, err)
}

func (h *Handler) Handback(ctx context.Context, r *connect.Request[pb.HandbackRequest]) (*connect.Response[pb.ItemResponse], error) {
	it, err := h.svc().Handback(ctx, r.Msg.Project, actorFrom(ctx), r.Msg.Id, r.Msg.Note)
	if err == nil {
		h.audit(ctx, "item.handback", it.ID, r.Msg.Project, r.Msg.Note)
	}
	return respond[pb.ItemResponse](map[string]any{"item": it}, err)
}

func (h *Handler) Redo(ctx context.Context, r *connect.Request[pb.RedoRequest]) (*connect.Response[pb.ItemResponse], error) {
	it, err := h.svc().Redo(ctx, r.Msg.Project, actorFrom(ctx), r.Msg.Id, r.Msg.Step, r.Msg.Why)
	if err == nil {
		h.audit(ctx, "item.redo", it.ID, r.Msg.Project, r.Msg.Step)
	}
	return respond[pb.ItemResponse](map[string]any{"item": it}, err)
}

// ---------- proposals and tweaks ----------

func (h *Handler) Propose(ctx context.Context, r *connect.Request[pb.ProposeRequest]) (*connect.Response[pb.ProposeResponse], error) {
	var p domain.Proposal
	if r.Msg.Proposal != nil {
		if err := decode(r.Msg.Proposal, &p); err != nil {
			return nil, err
		}
	}
	res, err := h.svc().Propose(ctx, r.Msg.Project, actorFrom(ctx), p)
	return respond[pb.ProposeResponse](res, err)
}

func (h *Handler) ListProposals(ctx context.Context, r *connect.Request[pb.ListProposalsRequest]) (*connect.Response[pb.ListProposalsResponse], error) {
	list, groups, err := h.svc().ListProposals(r.Msg.Project, actorFrom(ctx), r.Msg.All)
	return respond[pb.ListProposalsResponse](map[string]any{"proposals": list, "groups": groups}, err)
}

func (h *Handler) DecideProposal(ctx context.Context, r *connect.Request[pb.DecideProposalRequest]) (*connect.Response[pb.ProposalResponse], error) {
	p, err := h.svc().DecideProposal(ctx, r.Msg.Project, actorFrom(ctx), r.Msg.Id, r.Msg.Decision, r.Msg.Reason)
	if err == nil {
		h.audit(ctx, "proposal."+r.Msg.Decision, p.ID, r.Msg.Project, r.Msg.Reason)
	}
	return respond[pb.ProposalResponse](map[string]any{"proposal": p}, err)
}

func (h *Handler) AddTweak(ctx context.Context, r *connect.Request[pb.AddTweakRequest]) (*connect.Response[pb.AddTweakResponse], error) {
	t, it, err := h.svc().AddTweak(ctx, r.Msg.Project, actorFrom(ctx), r.Msg.Area, r.Msg.Text)
	return respond[pb.AddTweakResponse](map[string]any{"tweak": t, "item": it}, err)
}

// ---------- owner ----------

func (h *Handler) AskOwner(ctx context.Context, r *connect.Request[pb.AskOwnerRequest]) (*connect.Response[pb.QuestionResponse], error) {
	var q domain.Question
	if r.Msg.Question != nil {
		if err := decode(r.Msg.Question, &q); err != nil {
			return nil, err
		}
	}
	out, err := h.svc().AskOwner(ctx, r.Msg.Project, actorFrom(ctx), q)
	return respond[pb.QuestionResponse](map[string]any{"question": out}, err)
}

func (h *Handler) ListQuestions(ctx context.Context, r *connect.Request[pb.ProjectRef]) (*connect.Response[pb.QuestionsResponse], error) {
	st, err := h.svc().GetStatus(ctx, r.Msg.Project, actorFrom(ctx))
	return respond[pb.QuestionsResponse](map[string]any{"questions": st.Waiting}, err)
}

func (h *Handler) Answer(ctx context.Context, r *connect.Request[pb.AnswerRequest]) (*connect.Response[pb.QuestionResponse], error) {
	q, err := h.svc().Answer(ctx, r.Msg.Project, actorFrom(ctx), r.Msg.Id, r.Msg.Answer, r.Msg.Note)
	if err == nil {
		h.audit(ctx, "question.answer", q.ID, r.Msg.Project, r.Msg.Answer)
	}
	return respond[pb.QuestionResponse](map[string]any{"question": q}, err)
}

func (h *Handler) Approve(ctx context.Context, r *connect.Request[pb.ApproveRequest]) (*connect.Response[pb.Empty], error) {
	err := h.svc().Approve(ctx, r.Msg.Project, actorFrom(ctx), r.Msg.Target, r.Msg.What, r.Msg.Approve, r.Msg.Note)
	if err == nil {
		verb := "approve"
		if !r.Msg.Approve {
			verb = "reject"
		}
		h.audit(ctx, verb+" the "+r.Msg.What, r.Msg.Target, r.Msg.Project, r.Msg.Note)
	}
	return empty(err)
}

func (h *Handler) SetGoal(ctx context.Context, r *connect.Request[pb.SetGoalRequest]) (*connect.Response[pb.SetGoalResponse], error) {
	var g domain.Goal
	if r.Msg.Goal != nil {
		if err := decode(r.Msg.Goal, &g); err != nil {
			return nil, err
		}
	}
	goal, inv, err := h.svc().SetGoal(ctx, r.Msg.Project, actorFrom(ctx), g)
	if err == nil {
		h.audit(ctx, "goal.set", goal.ID, r.Msg.Project, goal.Name)
	}
	return respond[pb.SetGoalResponse](map[string]any{"goal": goal, "inventory": inv}, err)
}

func (h *Handler) SetFocus(ctx context.Context, r *connect.Request[pb.FocusRequest]) (*connect.Response[pb.FocusResponse], error) {
	var f domain.Focus
	if r.Msg.Focus != nil {
		f = domain.Focus{Name: r.Msg.Focus.Name, Area: r.Msg.Focus.Area, Items: r.Msg.Focus.Items, Share: int(r.Msg.Focus.Share)}
	}
	if r.Msg.Until != "" {
		u, err := domain.ParseUntil(r.Msg.Until, time.Now())
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		f.Until = u
	}
	eff, err := h.svc().Focus(ctx, r.Msg.Project, actorFrom(ctx), f, r.Msg.Preview)
	if err == nil && !r.Msg.Preview {
		h.audit(ctx, "focus.set", f.Name, r.Msg.Project, "")
	}
	return respond[pb.FocusResponse](map[string]any{"effect": eff, "applied": !r.Msg.Preview}, err)
}

func (h *Handler) ClearFocus(ctx context.Context, r *connect.Request[pb.ProjectRef]) (*connect.Response[pb.Empty], error) {
	return empty(h.svc().ClearFocus(ctx, r.Msg.Project, actorFrom(ctx)))
}

func (h *Handler) Pause(ctx context.Context, r *connect.Request[pb.PauseRequest]) (*connect.Response[pb.Empty], error) {
	err := h.svc().Pause(ctx, r.Msg.Project, actorFrom(ctx), r.Msg.Why)
	if err == nil {
		h.audit(ctx, "pause", "", r.Msg.Project, r.Msg.Why)
	}
	return empty(err)
}

func (h *Handler) Resume(ctx context.Context, r *connect.Request[pb.ProjectRef]) (*connect.Response[pb.Empty], error) {
	err := h.svc().Resume(ctx, r.Msg.Project, actorFrom(ctx))
	if err == nil {
		h.audit(ctx, "resume", "", r.Msg.Project, "")
	}
	return empty(err)
}

// ---------- views ----------

func (h *Handler) Status(ctx context.Context, r *connect.Request[pb.ProjectRef]) (*connect.Response[pb.StatusResponse], error) {
	st, err := h.svc().GetStatus(ctx, r.Msg.Project, actorFrom(ctx))
	if err != nil {
		return nil, connectErr(err)
	}
	out := &pb.StatusResponse{}
	if err := ToPB(st, out); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	// Machines embed domain.Machine plus health; JSON flattening already matches.
	return connect.NewResponse(out), nil
}

func (h *Handler) Metrics(ctx context.Context, r *connect.Request[pb.MetricsRequest]) (*connect.Response[pb.MetricsResponse], error) {
	w := 7 * 24 * time.Hour
	if r.Msg.Window != "" {
		d, err := domain.ParseDuration(r.Msg.Window)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		w = d
	}
	m, err := h.svc().Metrics(ctx, r.Msg.Project, actorFrom(ctx), w)
	return respond[pb.MetricsResponse](m, err)
}

func (h *Handler) Digest(ctx context.Context, r *connect.Request[pb.ProjectRef]) (*connect.Response[pb.DigestResponse], error) {
	if err := actorFrom(ctx).Require(app.ScopeItemsRead, r.Msg.Project); err != nil {
		return nil, connectErr(err)
	}
	d, err := h.svc().BuildDigest(ctx, r.Msg.Project)
	if err != nil {
		return nil, connectErr(err)
	}
	out := &pb.DigestResponse{}
	if err := ToPB(d, out); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	out.Text = d.Text()
	return connect.NewResponse(out), nil
}

func (h *Handler) Queue(ctx context.Context, r *connect.Request[pb.ProjectRef]) (*connect.Response[pb.QueueResponse], error) {
	q, err := h.svc().Queue(r.Msg.Project, actorFrom(ctx))
	var items []*domain.Item
	for _, e := range q {
		items = append(items, e.Item)
	}
	return respond[pb.QueueResponse](map[string]any{"items": items}, err)
}

func (h *Handler) ProcessQueue(ctx context.Context, r *connect.Request[pb.ProjectRef]) (*connect.Response[pb.QueueResponse], error) {
	if err := actorFrom(ctx).Require(app.ScopeAdmin, r.Msg.Project); err != nil {
		return nil, connectErr(err)
	}
	n, err := h.svc().ProcessQueue(ctx, r.Msg.Project)
	if err != nil {
		return nil, connectErr(err)
	}
	h.audit(ctx, "queue.run", "", r.Msg.Project, fmt.Sprint(n))
	return h.Queue(ctx, r)
}

func (h *Handler) ListEvents(ctx context.Context, r *connect.Request[pb.EventsRequest]) (*connect.Response[pb.EventsResponse], error) {
	a := actorFrom(ctx)
	if err := a.Require(app.ScopeEvents, r.Msg.Project); err != nil && !a.Can(app.ScopeItemsRead, r.Msg.Project) {
		return nil, connectErr(err)
	}
	limit := int(r.Msg.Limit)
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	evs, err := h.srv.events(ctx, r.Msg.Project, r.Msg.Subject, r.Msg.Types, r.Msg.AfterSeq, limit)
	return respond[pb.EventsResponse](map[string]any{"events": evs}, err)
}

// ---------- messaging ----------

func (h *Handler) SendMessage(ctx context.Context, r *connect.Request[pb.SendMessageRequest]) (*connect.Response[pb.MessageResponse], error) {
	var m domain.Message
	if r.Msg.Message != nil {
		if err := decode(r.Msg.Message, &m); err != nil {
			return nil, err
		}
	}
	out, err := h.svc().SendMessage(ctx, r.Msg.Project, actorFrom(ctx), m)
	return respond[pb.MessageResponse](map[string]any{"message": out}, err)
}

func (h *Handler) Inbox(ctx context.Context, r *connect.Request[pb.InboxRequest]) (*connect.Response[pb.InboxResponse], error) {
	a := actorFrom(ctx)
	agent, role, item := r.Msg.Agent, r.Msg.Role, r.Msg.ItemId
	if a.Kind == app.ActorAgent {
		agent, role, item = a.Name, a.Role, a.ItemID // agents only read their own mail
	} else if err := a.Require(app.ScopeItemsRead, r.Msg.Project); err != nil {
		return nil, connectErr(err)
	}
	msgs, err := h.svc().Inbox(ctx, r.Msg.Project, agent, role, item, r.Msg.Mark)
	return respond[pb.InboxResponse](map[string]any{"messages": msgs}, err)
}

func (h *Handler) Journal(ctx context.Context, r *connect.Request[pb.JournalRequest]) (*connect.Response[pb.Empty], error) {
	return empty(h.svc().Journal(ctx, r.Msg.Project, actorFrom(ctx), r.Msg.Text))
}

func (h *Handler) ReportViolation(ctx context.Context, r *connect.Request[pb.PolicyViolationRequest]) (*connect.Response[pb.Empty], error) {
	a := actorFrom(ctx)
	p, err := h.svc().Project(r.Msg.Project)
	if err != nil {
		return nil, connectErr(err)
	}
	agent := r.Msg.Agent
	if a.Kind == app.ActorAgent {
		agent = a.Name
	}
	err = h.svc().RecordViolation(ctx, p.Name, agent, r.Msg.ItemId, r.Msg.Tool, r.Msg.Rule, r.Msg.Reason, r.Msg.Excerpt)
	return empty(err)
}

// ---------- knowledge ----------

func (h *Handler) Context(ctx context.Context, r *connect.Request[pb.ContextRequest]) (*connect.Response[pb.ContextResponse], error) {
	pack, err := h.svc().Context(ctx, r.Msg.Project, actorFrom(ctx), r.Msg.Id, int(r.Msg.Budget))
	if err != nil {
		return nil, connectErr(err)
	}
	out := &pb.ContextResponse{}
	if err := ToPB(pack, out); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	out.Text = pack.Render()
	return connect.NewResponse(out), nil
}

func (h *Handler) Find(ctx context.Context, r *connect.Request[pb.FindRequest]) (*connect.Response[pb.FindResponse], error) {
	hits, err := h.svc().Find(ctx, r.Msg.Project, actorFrom(ctx), r.Msg.Query, ports.SearchFilters{Kind: r.Msg.Kind, Area: r.Msg.Area, Status: r.Msg.Status, Goal: r.Msg.Goal}, int(r.Msg.Limit))
	return respond[pb.FindResponse](map[string]any{"hits": hits}, err)
}

func knowledgeResp(v any, err error) (*connect.Response[pb.KnowledgeResponse], error) {
	if err != nil {
		return nil, connectErr(err)
	}
	st, err := ToStruct(v)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&pb.KnowledgeResponse{Result: st, Text: renderKnowledge(v)}), nil
}

func (h *Handler) Get(ctx context.Context, r *connect.Request[pb.KnowledgeRequest]) (*connect.Response[pb.KnowledgeResponse], error) {
	return knowledgeResp(h.svc().Get(ctx, r.Msg.Project, actorFrom(ctx), r.Msg.Target))
}

func (h *Handler) Related(ctx context.Context, r *connect.Request[pb.KnowledgeRequest]) (*connect.Response[pb.KnowledgeResponse], error) {
	return knowledgeResp(h.svc().Related(ctx, r.Msg.Project, actorFrom(ctx), r.Msg.Target))
}

func (h *Handler) Why(ctx context.Context, r *connect.Request[pb.KnowledgeRequest]) (*connect.Response[pb.KnowledgeResponse], error) {
	return knowledgeResp(h.svc().Why(ctx, r.Msg.Project, actorFrom(ctx), r.Msg.Target))
}

func (h *Handler) History(ctx context.Context, r *connect.Request[pb.KnowledgeRequest]) (*connect.Response[pb.KnowledgeResponse], error) {
	v, err := h.svc().History(ctx, r.Msg.Project, actorFrom(ctx), r.Msg.Target)
	return knowledgeResp(map[string]any{"history": v}, err)
}

func (h *Handler) Rules(ctx context.Context, r *connect.Request[pb.KnowledgeRequest]) (*connect.Response[pb.KnowledgeResponse], error) {
	v, err := h.svc().Rules(ctx, r.Msg.Project, actorFrom(ctx), r.Msg.Target)
	return knowledgeResp(map[string]any{"rules": v}, err)
}

func (h *Handler) Revalidate(ctx context.Context, r *connect.Request[pb.RevalidateRequest]) (*connect.Response[pb.RevalidateResponse], error) {
	var ch domain.Change
	if err := FromStruct(r.Msg.Change, &ch); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	res, err := h.svc().Revalidate(ctx, r.Msg.Project, actorFrom(ctx), ch)
	if err != nil {
		return nil, connectErr(err)
	}
	st, err := ToStruct(res)
	return connect.NewResponse(&pb.RevalidateResponse{Result: st}), err
}

func (h *Handler) Sweep(ctx context.Context, r *connect.Request[pb.ProjectRef]) (*connect.Response[pb.RevalidateResponse], error) {
	if err := actorFrom(ctx).Require(app.ScopeItemsWrite, r.Msg.Project); err != nil {
		return nil, connectErr(err)
	}
	res, err := h.svc().Sweep(ctx, r.Msg.Project)
	if err != nil {
		return nil, connectErr(err)
	}
	st, err := ToStruct(res)
	return connect.NewResponse(&pb.RevalidateResponse{Result: st}), err
}

// ---------- accounts and machines ----------

func (h *Handler) UpsertAccount(ctx context.Context, r *connect.Request[pb.AccountRequest]) (*connect.Response[pb.AccountResponse], error) {
	var acc domain.Account
	if r.Msg.Account != nil {
		if err := decode(r.Msg.Account, &acc); err != nil {
			return nil, err
		}
	}
	out, err := h.svc().UpsertAccount(ctx, r.Msg.Project, actorFrom(ctx), acc)
	if err == nil {
		h.audit(ctx, "account.upsert", acc.ID, r.Msg.Project, acc.Provider)
	}
	return respond[pb.AccountResponse](map[string]any{"account": out}, err)
}

func (h *Handler) ListAccounts(ctx context.Context, r *connect.Request[pb.ProjectRef]) (*connect.Response[pb.AccountsResponse], error) {
	st, err := h.svc().GetStatus(ctx, r.Msg.Project, actorFrom(ctx))
	return respond[pb.AccountsResponse](map[string]any{"accounts": st.Accounts}, err)
}

func (h *Handler) ReportUsage(ctx context.Context, r *connect.Request[pb.ReportUsageRequest]) (*connect.Response[pb.Empty], error) {
	var u domain.Usage
	if r.Msg.Usage != nil {
		if err := decode(r.Msg.Usage, &u); err != nil {
			return nil, err
		}
	}
	return empty(h.svc().ReportUsage(ctx, r.Msg.Project, actorFrom(ctx), r.Msg.Account, u))
}

func (h *Handler) ListMachines(ctx context.Context, r *connect.Request[pb.ProjectRef]) (*connect.Response[pb.MachinesResponse], error) {
	st, err := h.svc().GetStatus(ctx, r.Msg.Project, actorFrom(ctx))
	if err != nil {
		return nil, connectErr(err)
	}
	out := &pb.MachinesResponse{}
	if err := ToPB(map[string]any{"machines": st.Machines}, out); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	for _, s := range h.svc().Runtime().Sessions() {
		if st, err := ToStruct(s); err == nil {
			out.Sessions = append(out.Sessions, st)
		}
	}
	return connect.NewResponse(out), nil
}

func (h *Handler) MachineHistory(ctx context.Context, r *connect.Request[pb.MachineHistoryRequest]) (*connect.Response[pb.MachineHistoryResponse], error) {
	var window time.Duration
	if w := strings.TrimSpace(r.Msg.Window); w != "" {
		d, err := domain.ParseDuration(w)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("window: %w", err))
		}
		window = d
	}
	hist, err := h.svc().MachineHistory(ctx, r.Msg.Project, actorFrom(ctx), r.Msg.Machine, window)
	if err != nil {
		return nil, connectErr(err)
	}
	names := make([]string, 0, len(hist))
	for n := range hist {
		names = append(names, n)
	}
	sort.Strings(names)
	out := &pb.MachineHistoryResponse{}
	for _, n := range names {
		series := &pb.MachineSeries{Machine: n}
		for _, s := range hist[n] {
			series.Samples = append(series.Samples, &pb.MachineSample{At: s.At.Format(time.RFC3339), CpuPct: s.CPUPct, MemPct: s.MemPct,
				TempC: s.TempC, GpuPct: s.GPUPct, GpuTempC: s.GPUTempC, Busy: int32(s.Busy), Slots: int32(s.Slots)})
		}
		out.Series = append(out.Series, series)
	}
	return connect.NewResponse(out), nil
}

func (h *Handler) LeaveMachine(ctx context.Context, r *connect.Request[pb.ProjectRef]) (*connect.Response[pb.LeaveResponse], error) {
	var all []string
	projects := []string{r.Msg.Project}
	if r.Msg.Project == "" {
		projects = h.svc().Projects()
	}
	for _, p := range projects {
		ret, err := h.svc().MachineLeave(ctx, p, actorFrom(ctx))
		if err != nil {
			return nil, connectErr(err)
		}
		all = append(all, ret...)
	}
	h.audit(ctx, "machine.leave", h.svc().Machine(), r.Msg.Project, strings.Join(all, ","))
	return connect.NewResponse(&pb.LeaveResponse{Returned: all}), nil
}

func (h *Handler) ListPeers(ctx context.Context, _ *connect.Request[pb.Empty]) (*connect.Response[pb.PeersResponse], error) {
	if err := actorFrom(ctx).Require(app.ScopeAdmin, ""); err != nil {
		return nil, connectErr(err)
	}
	out := &pb.PeersResponse{}
	if h.srv.opts.Peers != nil {
		out.Peers = h.srv.opts.Peers()
	}
	return connect.NewResponse(out), nil
}

func (h *Handler) StopPeers(ctx context.Context, r *connect.Request[pb.StopPeersRequest]) (*connect.Response[pb.StopPeersResponse], error) {
	if err := actorFrom(ctx).Require(app.ScopeAdmin, ""); err != nil {
		return nil, connectErr(err)
	}
	if h.srv.opts.StopPeers == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("this daemon cannot stop other machines"))
	}
	out := &pb.StopPeersResponse{Results: h.srv.opts.StopPeers(ctx, r.Msg.Names)}
	var stopped []string
	for _, res := range out.Results {
		if res.Stopped {
			stopped = append(stopped, res.Name)
		}
	}
	h.audit(ctx, "machine.stop", strings.Join(stopped, ","), "", "")
	return connect.NewResponse(out), nil
}

func (h *Handler) SetStandby(ctx context.Context, r *connect.Request[pb.StandbyRequest]) (*connect.Response[pb.StandbyResponse], error) {
	if err := actorFrom(ctx).Require(app.ScopeAdmin, ""); err != nil {
		return nil, connectErr(err)
	}
	if h.srv.opts.SetStandby == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("this daemon has no standby"))
	}
	out, err := h.srv.opts.SetStandby(ctx, r.Msg.On, r.Msg.Why, r.Msg.Peers)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	action := "machine.standby.off"
	if r.Msg.On {
		action = "machine.standby.on"
	}
	h.audit(ctx, action, h.svc().Machine(), "", r.Msg.Why)
	return connect.NewResponse(out), nil
}

// UnlockFiles lets an item change locked files.
func (h *Handler) UnlockFiles(ctx context.Context, r *connect.Request[pb.UnlockFilesRequest]) (*connect.Response[pb.ItemResponse], error) {
	it, err := h.svc().UnlockFiles(ctx, r.Msg.Project, actorFrom(ctx), r.Msg.Item, r.Msg.Files, r.Msg.Why)
	if err == nil {
		h.audit(ctx, "item.unlock", it.ID, r.Msg.Project, strings.Join(r.Msg.Files, ","))
	}
	return respond[pb.ItemResponse](map[string]any{"item": it}, err)
}

// ShareProject has other machines clone and register a project.
func (h *Handler) ShareProject(ctx context.Context, r *connect.Request[pb.ShareProjectRequest]) (*connect.Response[pb.ShareProjectResponse], error) {
	if err := actorFrom(ctx).Require(app.ScopeAdmin, ""); err != nil {
		return nil, connectErr(err)
	}
	if _, err := h.svc().Project(r.Msg.Project); err != nil {
		return nil, connectErr(err)
	}
	if len(r.Msg.Machines) == 0 && !r.Msg.All {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("name the machines, or all"))
	}
	if h.srv.opts.ShareProject == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("this daemon cannot reach other machines"))
	}
	out, err := h.srv.opts.ShareProject(ctx, r.Msg)
	if err != nil {
		return nil, connectErr(err)
	}
	var ok []string
	for _, m := range out.Machines {
		if m.Ok {
			ok = append(ok, m.Name)
		}
	}
	h.audit(ctx, "project.share", strings.Join(ok, ","), r.Msg.Project, "")
	return connect.NewResponse(out), nil
}

// SetMaxAgents sets how many agents the named machines run at once.
func (h *Handler) SetMaxAgents(ctx context.Context, r *connect.Request[pb.MaxAgentsRequest]) (*connect.Response[pb.MaxAgentsResponse], error) {
	if err := actorFrom(ctx).Require(app.ScopeAdmin, ""); err != nil {
		return nil, connectErr(err)
	}
	if r.Msg.MaxAgents < 0 || r.Msg.MaxAgents > 64 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("from 1 to 64 agents, or 0 to measure it from the machine"))
	}
	if h.srv.opts.SetMaxAgents == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("this daemon cannot change its limit of agents"))
	}
	out, err := h.srv.opts.SetMaxAgents(ctx, int(r.Msg.MaxAgents), r.Msg.Machines)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	h.audit(ctx, "machine.max_agents", strings.Join(r.Msg.Machines, ","), "", strconv.Itoa(int(r.Msg.MaxAgents)))
	return connect.NewResponse(out), nil
}

// ---------- tokens and audit ----------

func (h *Handler) CreateToken(ctx context.Context, r *connect.Request[pb.CreateTokenRequest]) (*connect.Response[pb.CreateTokenResponse], error) {
	if err := actorFrom(ctx).Require(app.ScopeAdmin, r.Msg.Project); err != nil {
		return nil, connectErr(err)
	}
	if err := app.ValidateScopes(r.Msg.Scopes); err != nil {
		return nil, connectErr(err)
	}
	if len(r.Msg.Scopes) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("a token needs at least one scope, e.g. checks:report"))
	}
	var ttl time.Duration
	if r.Msg.Ttl != "" {
		d, err := domain.ParseDuration(r.Msg.Ttl)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		ttl = d
	}
	tok, secret, err := h.srv.createToken(ctx, r.Msg.Name, r.Msg.Project, r.Msg.Scopes, ttl)
	if err != nil {
		return nil, connectErr(err)
	}
	h.audit(ctx, "token.create", tok.ID, r.Msg.Project, strings.Join(r.Msg.Scopes, ","))
	out := &pb.CreateTokenResponse{Token: &pb.Token{}, Secret: secret}
	_ = ToPB(tok, out.Token)
	return connect.NewResponse(out), nil
}

func (h *Handler) ListTokens(ctx context.Context, _ *connect.Request[pb.Empty]) (*connect.Response[pb.TokensResponse], error) {
	if err := actorFrom(ctx).Require(app.ScopeAdmin, ""); err != nil {
		return nil, connectErr(err)
	}
	list, err := h.srv.listTokens(ctx)
	return respond[pb.TokensResponse](map[string]any{"tokens": list}, err)
}

func (h *Handler) RevokeToken(ctx context.Context, r *connect.Request[pb.RevokeTokenRequest]) (*connect.Response[pb.Empty], error) {
	if err := actorFrom(ctx).Require(app.ScopeAdmin, ""); err != nil {
		return nil, connectErr(err)
	}
	err := h.srv.revokeToken(ctx, r.Msg.Id)
	if err == nil {
		h.audit(ctx, "token.revoke", r.Msg.Id, "", "")
	}
	return empty(err)
}

func (h *Handler) ListAudit(ctx context.Context, r *connect.Request[pb.AuditRequest]) (*connect.Response[pb.AuditResponse], error) {
	if err := actorFrom(ctx).Require(app.ScopeAdmin, r.Msg.Project); err != nil {
		return nil, connectErr(err)
	}
	entries, err := h.srv.listAudit(ctx, r.Msg.Project, int(r.Msg.Limit))
	return respond[pb.AuditResponse](map[string]any{"entries": entries}, err)
}

// rfc3339 writes a time for the API, empty when it is not known.
func rfc3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// Discuss: the owner's conversation with the advisor about a decision.
func (h *Handler) Discuss(ctx context.Context, r *connect.Request[pb.DiscussRequest]) (*connect.Response[pb.DiscussionResponse], error) {
	conv, err := h.svc().Discuss(ctx, r.Msg.Project, actorFrom(ctx), r.Msg.Thread, r.Msg.Text)
	if errors.Is(err, app.ErrNoAdvisor) {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	return respond[pb.DiscussionResponse](map[string]any{"messages": conv}, err)
}

func (h *Handler) GetDiscussion(ctx context.Context, r *connect.Request[pb.DiscussionRef]) (*connect.Response[pb.DiscussionResponse], error) {
	conv, err := h.svc().Discussion(r.Msg.Project, actorFrom(ctx), r.Msg.Thread)
	return respond[pb.DiscussionResponse](map[string]any{"messages": conv}, err)
}

// ListParts: the parts of a project and how their work goes.
func (h *Handler) ListParts(ctx context.Context, r *connect.Request[pb.ProjectRef]) (*connect.Response[pb.PartsResponse], error) {
	v, err := h.svc().Parts(ctx, r.Msg.Project, actorFrom(ctx))
	return respond[pb.PartsResponse](v, err)
}

// Delegate: the advisor decides a question or a proposal for the owner.
func (h *Handler) Delegate(ctx context.Context, r *connect.Request[pb.DiscussionRef]) (*connect.Response[pb.DiscussionResponse], error) {
	conv, err := h.svc().Delegate(ctx, r.Msg.Project, actorFrom(ctx), r.Msg.Thread)
	if errors.Is(err, app.ErrNoAdvisor) {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	return respond[pb.DiscussionResponse](map[string]any{"messages": conv}, err)
}

// TriageProposals: the advisor's word on every open proposal.
func (h *Handler) TriageProposals(ctx context.Context, r *connect.Request[pb.ProjectRef]) (*connect.Response[pb.ProposalTriageResponse], error) {
	v, err := h.svc().TriageProposals(ctx, r.Msg.Project, actorFrom(ctx))
	if errors.Is(err, app.ErrNoAdvisor) {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	return respond[pb.ProposalTriageResponse](map[string]any{"verdicts": v}, err)
}
