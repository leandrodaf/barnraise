package main

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	natsx "github.com/leandrodaf/barnraise/internal/adapters/out/nats"
	"github.com/leandrodaf/barnraise/internal/adapters/out/sqlite"
	"github.com/leandrodaf/barnraise/internal/app"
	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/testkit"
)

func TestChainWatchAnchorsAndReports(t *testing.T) {
	ctx := context.Background()
	st, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := app.New(app.Deps{Store: st, Clock: testkit.NewClock(time.Now()), IDs: testkit.NewIDs("e"), Log: log, Machine: "m1"})
	seed, pub, _ := natsx.NewMachineKey()
	w := &chainWatch{store: st, svc: svc, seed: seed, pub: pub, machine: "m1", log: log, now: time.Now}

	// An empty log checks out and has nothing to anchor.
	if !w.check(ctx, "start") {
		t.Fatal("empty log")
	}
	w.anchor(ctx)
	if c, _ := st.CheckAnchor(ctx, nil); c != nil {
		t.Fatal("anchored an empty log")
	}
	for range 3 {
		_, _ = st.Append(ctx, domain.NewEvent("", domain.EvJournal, "p", "", "m1", "a", time.Now(), domain.Payload{Note: "x"}))
	}
	if !w.check(ctx, "start") {
		t.Fatal("intact log")
	}
	w.anchor(ctx)
	c, err := st.CheckAnchor(ctx, natsx.AnchorVerifier(pub))
	if err != nil || c == nil || !c.OK || c.Seq != 3 {
		t.Fatalf("anchor %+v %v", c, err)
	}
	// Another machine's key does not vouch for this log.
	_, other, _ := natsx.NewMachineKey()
	if c, _ := st.CheckAnchor(ctx, natsx.AnchorVerifier(other)); c.OK {
		t.Fatal("foreign key accepted")
	}
	if c, _ := st.CheckAnchor(ctx, natsx.AnchorVerifier("")); c.OK {
		t.Fatal("no key accepted")
	}
	// Tampering is reported once, and the anchor is not moved past it.
	if _, err := st.DB().Exec(`UPDATE events SET data = '{}' WHERE seq = 2`); err != nil {
		t.Fatal(err)
	}
	if w.check(ctx, "anchor") || w.reported == "" {
		t.Fatal("tampering not seen")
	}
	first := w.reported
	if w.check(ctx, "anchor") || w.reported != first {
		t.Fatal("reported again")
	}
	w.seed = ""
	w.anchor(ctx) // no key: nothing is signed
	w.seed = "bad"
	w.anchor(ctx)
	if c, _ := st.CheckAnchor(ctx, nil); c.Seq != 3 {
		t.Fatalf("anchor moved: %+v", c)
	}
}
