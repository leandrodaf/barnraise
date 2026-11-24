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

// AccountLoginPB is a login session as the API answers it.
func AccountLoginPB(s domain.LoginSession) *pb.AccountLogin {
	out := &pb.AccountLogin{}
	if err := ToPB(s, out); err != nil {
		return &pb.AccountLogin{Machine: s.Machine, Account: s.Account, State: domain.LoginFailed, Error: err.Error()}
	}
	return out
}

// accountLogin runs one step of an account's login: on this machine and the
// requested peers when the daemon reaches them, on this machine otherwise.
func (h *Handler) accountLogin(ctx context.Context, op string, r *pb.AccountLoginRequest) (*connect.Response[pb.AccountLoginResponse], error) {
	a := actorFrom(ctx)
	if err := a.Require(app.ScopeAdmin, ""); err != nil {
		return nil, connectErr(err)
	}
	if op == app.LoginOpAnswer && (len(r.Machines) != 1 || r.All) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("name the one machine whose login takes the code"))
	}
	var out *pb.AccountLoginResponse
	if h.srv.opts.AccountLogin != nil {
		var err error
		if out, err = h.srv.opts.AccountLogin(ctx, a, op, r); err != nil {
			return nil, connectErr(err)
		}
	} else {
		for _, m := range r.Machines {
			if m != h.svc().Machine() {
				return nil, connect.NewError(connect.CodeUnimplemented, errors.New("this daemon cannot reach other machines"))
			}
		}
		account, err := h.svc().LoginAccountID(r.Account, r.Provider)
		if err != nil {
			return nil, connectErr(err)
		}
		s, err := h.svc().Login(ctx, a, op, account, r.Provider, r.Code)
		if err != nil {
			return nil, connectErr(err)
		}
		out = &pb.AccountLoginResponse{Account: account}
		if s.State != "" {
			out.Logins = append(out.Logins, AccountLoginPB(s))
		}
	}
	if op != app.LoginOpState {
		var machines []string
		for _, l := range out.Logins {
			machines = append(machines, l.Machine)
		}
		h.audit(ctx, "account.login."+op, out.Account, "", strings.Join(machines, ","))
	}
	return connect.NewResponse(out), nil
}

func (h *Handler) StartAccountLogin(ctx context.Context, r *connect.Request[pb.AccountLoginRequest]) (*connect.Response[pb.AccountLoginResponse], error) {
	return h.accountLogin(ctx, app.LoginOpStart, r.Msg)
}

func (h *Handler) GetAccountLogin(ctx context.Context, r *connect.Request[pb.AccountLoginRequest]) (*connect.Response[pb.AccountLoginResponse], error) {
	return h.accountLogin(ctx, app.LoginOpState, r.Msg)
}

func (h *Handler) AnswerAccountLogin(ctx context.Context, r *connect.Request[pb.AccountLoginRequest]) (*connect.Response[pb.AccountLoginResponse], error) {
	return h.accountLogin(ctx, app.LoginOpAnswer, r.Msg)
}

func (h *Handler) CancelAccountLogin(ctx context.Context, r *connect.Request[pb.AccountLoginRequest]) (*connect.Response[pb.AccountLoginResponse], error) {
	return h.accountLogin(ctx, app.LoginOpCancel, r.Msg)
}
