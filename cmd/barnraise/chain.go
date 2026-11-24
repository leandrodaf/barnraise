package main

import (
	"context"
	"log/slog"
	"time"

	natsx "github.com/leandrodaf/barnraise/internal/adapters/out/nats"
	"github.com/leandrodaf/barnraise/internal/adapters/out/sqlite"
	"github.com/leandrodaf/barnraise/internal/app"
	"github.com/leandrodaf/barnraise/internal/domain"
)

// The hash chain of the event log at run time (ADR-0029): the daemon checks
// the tail of the chain and the last signed anchor when it starts, then
// every chainEvery writes a new anchor (the head signed with the machine
// key of daemon.yaml) once the tail checks out.
const (
	chainTail  = 2000
	chainEvery = time.Hour
)

type chainWatch struct {
	store    *sqlite.Store
	svc      *app.Service
	seed     string
	pub      string
	machine  string
	log      *slog.Logger
	now      func() time.Time
	reported string // the last problem reported, so it is told once
}

func newChainWatch(d *daemon) *chainWatch {
	return &chainWatch{store: d.store, svc: d.svc, seed: d.df.NKeySeed, pub: d.df.NKeyPublic, machine: d.df.Machine, log: d.log, now: time.Now}
}

func (w *chainWatch) run(ctx context.Context) {
	if w.check(ctx, "start") {
		w.anchor(ctx)
	}
	t := time.NewTicker(chainEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if w.check(ctx, "anchor") {
			w.anchor(ctx)
		}
	}
}

// check verifies the tail and the anchor, reporting a new problem once.
func (w *chainWatch) check(ctx context.Context, where string) bool {
	head, _, err := w.store.ChainHead(ctx)
	if err != nil {
		w.log.Warn("event chain", "err", err)
		return false
	}
	rep, err := w.store.VerifyChain(ctx, max(head-chainTail+1, 0))
	if err != nil {
		w.log.Warn("event chain", "err", err)
		return false
	}
	if rep.Anchor, err = w.store.CheckAnchor(ctx, natsx.AnchorVerifier(w.pub)); err != nil {
		w.log.Warn("event chain anchor", "err", err)
	}
	if rep.OK() {
		return true
	}
	note, _ := app.ChainProblem(rep)
	if note != w.reported {
		w.reported = note
		w.svc.ReportChain(ctx, rep, where)
	}
	return false
}

// anchor signs the current head.
func (w *chainWatch) anchor(ctx context.Context) {
	if w.seed == "" {
		return
	}
	seq, h, err := w.store.ChainHead(ctx)
	if err != nil || seq == 0 {
		return
	}
	a := domain.ChainAnchor{Seq: seq, Hash: h, Time: w.now().UTC(), Machine: w.machine}
	if a.PublicKey, a.Signature, err = natsx.Sign(w.seed, a.SignedBytes()); err != nil {
		w.log.Warn("sign the event chain anchor", "err", err)
		return
	}
	if err := w.store.SaveAnchor(ctx, a); err != nil {
		w.log.Warn("save the event chain anchor", "err", err)
	}
}
