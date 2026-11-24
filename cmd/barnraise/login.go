package main

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	pb "github.com/leandrodaf/barnraise/gen/barnraise/v1"
	"github.com/leandrodaf/barnraise/internal/adapters/in/httpapi"
	natsx "github.com/leandrodaf/barnraise/internal/adapters/out/nats"
	"github.com/leandrodaf/barnraise/internal/app"
	"github.com/leandrodaf/barnraise/internal/domain"
)

// peerLoginTimeout bounds one step of a login asked of another machine: the
// login itself goes on there, and is followed with further steps.
const peerLoginTimeout = 20 * time.Second

// remoteLogin is one step of an account's login, asked of a paired machine.
// Only the step travels: each machine keeps its own login.
type remoteLogin struct {
	Op       string `json:"op"`
	Account  string `json:"account"`
	Provider string `json:"provider,omitempty"`
	Code     string `json:"code,omitempty"`
	From     string `json:"from,omitempty"`
}

// accountLogin runs one step of an account's login on this machine and on
// the requested peers, all at the same time.
func (d *daemon) accountLogin(ctx context.Context, a app.Actor, op string, r *pb.AccountLoginRequest) (*pb.AccountLoginResponse, error) {
	account, err := d.svc.LoginAccountID(r.Account, r.Provider)
	if err != nil {
		return nil, err
	}
	self, peers := d.targets(r.Machines, r.All)
	failed := func(machine, msg string) *pb.AccountLogin {
		return &pb.AccountLogin{Machine: machine, Account: account, Provider: r.Provider, State: domain.LoginFailed, Error: msg}
	}
	res := make([]*pb.AccountLogin, len(peers)+1)
	var wg sync.WaitGroup
	if self {
		wg.Go(func() {
			s, err := d.svc.Login(ctx, a, op, account, r.Provider, r.Code)
			if err != nil {
				res[0] = failed(d.df.Machine, err.Error())
				return
			}
			res[0] = httpapi.AccountLoginPB(s)
		})
	}
	body, _ := json.Marshal(remoteLogin{Op: op, Account: account, Provider: r.Provider, Code: r.Code, From: d.df.Machine})
	for i, name := range peers {
		wg.Go(func() {
			if d.repl == nil {
				res[i+1] = failed(name, "this daemon has no connection to other machines")
				return
			}
			data, err := d.repl.Request(ctx, name, natsx.RPCSubject(name, "login"), body, peerLoginTimeout, 0)
			if err != nil {
				res[i+1] = failed(name, peerError(err, "logins from the panel"))
				return
			}
			var s domain.LoginSession
			if err := json.Unmarshal(data, &s); err != nil {
				res[i+1] = failed(name, "unreadable answer: "+err.Error())
				return
			}
			res[i+1] = httpapi.AccountLoginPB(s)
		})
	}
	wg.Wait()
	out := &pb.AccountLoginResponse{Account: account}
	for _, l := range res {
		// A machine asked for the state of a login it does not run says nothing.
		if l != nil && l.State != "" {
			out.Logins = append(out.Logins, l)
		}
	}
	return out, nil
}

// serveRemoteLogin answers the peers' login steps: the login runs here, as
// the owner asked from the other machine.
func (d *daemon) serveRemoteLogin(ctx context.Context) {
	b := d.bus.v.Load()
	if b == nil {
		return
	}
	_ = natsx.Serve(ctx, b.Conn(), natsx.RPCSubject(d.df.Machine, "login"), func(ctx context.Context, data []byte) ([]byte, error) {
		var rl remoteLogin
		if err := json.Unmarshal(data, &rl); err != nil {
			return nil, err
		}
		if rl.Op != app.LoginOpState {
			d.log.Info("a paired machine asked for an account's login here", "from", rl.From, "step", rl.Op, "account", rl.Account)
		}
		s, err := d.svc.Login(ctx, app.Owner("owner@"+rl.From), rl.Op, rl.Account, rl.Provider, rl.Code)
		if err != nil {
			return nil, err
		}
		return json.Marshal(s)
	})
}
