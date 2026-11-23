package nats

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/leandrodaf/barnraise/internal/domain"
)

func heartbeat(id, machine string, load float64) domain.Event {
	m := domain.Machine{Name: machine, CPUs: 8, Load1: load}
	return domain.NewEvent(id, domain.EvMachineUpdated, "shop", machine, machine, "barnraise", time.Now(), domain.Payload{MachineI: &m})
}

// Live state is a last-value cache: a thousand heartbeats leave one message,
// the event log is not touched, and a peer that connects later receives
// only the latest reading.
func TestLiveStateKeepsOnlyTheLatestValue(t *testing.T) {
	seed, pub, err := NewMachineKey()
	if err != nil {
		t.Fatal(err)
	}
	a := startMachine(t, "alpha", t.TempDir(), -1, PeerPermissions(pub, "alpha"))
	defer a.stop()
	ctx := context.Background()
	if err := a.bus.Publish(ctx, ev("e1", "alpha", "I-1", 1)); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 1000; i++ {
		if err := a.bus.Publish(ctx, heartbeat(fmt.Sprintf("hb-%d", i), "alpha", float64(i))); err != nil {
			t.Fatal(err)
		}
	}
	info := func(name string) jetstream.StreamState {
		st, err := a.bus.JetStream().Stream(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		si, err := st.Info(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return si.State
	}
	if got := info(LiveStreamName).Msgs; got != 1 {
		t.Fatalf("the live stream holds %d messages after 1000 heartbeats, want 1", got)
	}
	if got := info(StreamName).Msgs; got != 1 {
		t.Fatalf("heartbeats must not enter the event log: %d messages, want the 1 item event", got)
	}

	auth, err := NkeyOption(seed)
	if err != nil {
		t.Fatal(err)
	}
	rec := newRecorder()
	rep := NewReplicator("beta", rec.apply, nil)
	defer rep.Close()
	if err := rep.AddPeer("alpha", a.srv.ClientURL(), auth); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the item event and the latest heartbeat", 10*time.Second, func() bool { return rec.len() == 2 })
	time.Sleep(200 * time.Millisecond)
	if rec.len() != 2 {
		t.Fatalf("a late peer must get one heartbeat, not the history: %d events", rec.len())
	}
	// A new reading reaches the peer as it happens.
	if err := a.bus.Publish(ctx, heartbeat("hb-next", "alpha", 3.5)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the next reading", 10*time.Second, func() bool { return rec.len() == 3 })
}

func TestLiveSubject(t *testing.T) {
	if got := LiveSubject(heartbeat("x", "pc ana", 1)); got != "brlive.shop.machine.updated.pc_ana" {
		t.Fatal(got)
	}
	s := domain.Event{Type: domain.EvAgentSession, Project: "shop", Subject: "implementer-pc-1", Machine: "pc"}
	if got := LiveSubject(s); got != "brlive.shop.agent.session.implementer-pc-1" {
		t.Fatal(got)
	}
}
