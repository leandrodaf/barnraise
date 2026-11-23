package nats

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/leandrodaf/barnraise/internal/domain"
)

func TestSubject(t *testing.T) {
	cases := []struct {
		e    domain.Event
		want string
	}{
		{domain.Event{Type: domain.EvItemCreated, Project: "nobira", Subject: "I-42"}, "br.nobira.item.I-42.created"},
		{domain.Event{Type: domain.EvItemStepStarted, Project: "nobira", Subject: "I-42"}, "br.nobira.item.I-42.step.started"},
		{domain.Event{Type: "machine.heartbeat", Project: "p", Machine: "pc leandro"}, "br.p.machine.pc_leandro.heartbeat"},
		{domain.Event{Type: domain.EvCheckCompleted, Project: "p", Subject: "C.1"}, "br.p.check.C_1.completed"},
		{domain.Event{Type: domain.EvOwnerQuestionAsked, Project: "p", Subject: "Q-1"}, "br.p.owner.question.asked.Q-1"},
		{domain.Event{Type: domain.EvGoalSet, Project: "my proj*"}, "br.my_proj_.goal.set._"},
		{domain.Event{Type: "weird", Project: "a>b", Subject: "x y"}, "br.a_b.weird.x_y"},
	}
	for _, c := range cases {
		if got := Subject(c.e); got != c.want {
			t.Errorf("Subject(%+v) = %q, want %q", c.e, got, c.want)
		}
	}
	if got := HeartbeatSubject("p", "m1"); got != "br.p.machine.m1.heartbeat" {
		t.Errorf("heartbeat subject %q", got)
	}
	if ProjectFilter("") != "br.>" || ProjectFilter("x.y") != "br.x_y.>" {
		t.Error("project filter")
	}
}

type machine struct {
	name string
	dir  string
	port int
	srv  *Server
	nc   *nats.Conn
	bus  *Bus
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func startMachine(t *testing.T, name, dir string, port int, users ...UserPerm) *machine {
	t.Helper()
	srv, err := StartServer(ServerOptions{Name: name, StoreDir: dir, Port: port, Users: users})
	if err != nil {
		t.Fatal(err)
	}
	nc, err := srv.Connect()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	bus, err := NewBus(ctx, nc, BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return &machine{name: name, dir: dir, port: port, srv: srv, nc: nc, bus: bus}
}

func (m *machine) stop() {
	m.nc.Close()
	m.srv.Shutdown()
}

type recorder struct {
	mu     sync.Mutex
	counts map[string]int
	order  []domain.Event
}

func newRecorder() *recorder { return &recorder{counts: map[string]int{}} }

func (r *recorder) apply(e domain.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.counts[e.ID]++
	r.order = append(r.order, e)
	return nil
}

func (r *recorder) len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.order)
}

func waitFor(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func ev(id, machine, item string, n int) domain.Event {
	return domain.NewEvent(id, domain.EvItemUpdated, "nobira", item, machine, "test", time.Now(), domain.Payload{Note: fmt.Sprint(n)})
}

func TestBusPublishSubscribe(t *testing.T) {
	m := startMachine(t, "solo", t.TempDir(), -1)
	defer m.stop()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	var got []domain.Event
	if err := m.bus.Subscribe(ctx, "nobira", func(e domain.Event) {
		mu.Lock()
		got = append(got, e)
		mu.Unlock()
	}); err != nil {
		t.Fatal(err)
	}
	var all []domain.Event
	if err := m.bus.Subscribe(ctx, "", func(e domain.Event) {
		mu.Lock()
		all = append(all, e)
		mu.Unlock()
	}); err != nil {
		t.Fatal(err)
	}
	for i := range 20 {
		if err := m.bus.Publish(ctx, ev(fmt.Sprintf("e%d", i), "solo", "I-1", i)); err != nil {
			t.Fatal(err)
		}
	}
	// duplicate id is dropped by the stream
	if err := m.bus.Publish(ctx, ev("e3", "solo", "I-1", 3)); err != nil {
		t.Fatal(err)
	}
	other := domain.NewEvent("x1", domain.EvGoalSet, "other", "G-1", "solo", "", time.Now(), domain.Payload{})
	if err := m.bus.Publish(ctx, other); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "events", 5*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(got) == 20 && len(all) == 21
	})
	mu.Lock()
	for i, e := range got {
		if e.ID != fmt.Sprintf("e%d", i) {
			t.Fatalf("order: position %d has %s", i, e.ID)
		}
	}
	mu.Unlock()
	if err := m.bus.Publish(ctx, domain.Event{Type: "x"}); err == nil {
		t.Fatal("publish without id must fail")
	}
}

// Two machines, each with its own server, bus and replicator.
func TestReplicationTwoMachines(t *testing.T) {
	base := t.TempDir()
	portB := freePort(t)
	a := startMachine(t, "alpha", filepath.Join(base, "a"), -1)
	defer a.stop()
	b := startMachine(t, "beta", filepath.Join(base, "b"), portB)

	recA, recB := newRecorder(), newRecorder()
	cursorFile := filepath.Join(base, "b-cursors.json")
	curB, err := NewFileCursorStore(cursorFile)
	if err != nil {
		t.Fatal(err)
	}
	repA := NewReplicator("alpha", recA.apply, NewMemCursorStore())
	repA.MaxBackoff = time.Second
	defer repA.Close()
	repB := NewReplicator("beta", recB.apply, curB)
	repB.MaxBackoff = time.Second
	if err := repA.AddPeer("beta", b.srv.ClientURL()); err != nil {
		t.Fatal(err)
	}
	if err := repB.AddPeer("alpha", a.srv.ClientURL()); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// Events from alpha, each published twice; plus one foreign event that
	// alpha merely relays (must not be applied by beta through alpha).
	for i := range 30 {
		e := ev(fmt.Sprintf("a%02d", i), "alpha", fmt.Sprintf("I-%d", i%3), i)
		for range 2 {
			if err := a.bus.Publish(ctx, e); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := a.bus.Publish(ctx, ev("relayed", "gamma", "I-9", 0)); err != nil {
		t.Fatal(err)
	}
	if err := b.bus.Publish(ctx, ev("b00", "beta", "I-5", 0)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "beta applies 30", 10*time.Second, func() bool { return recB.len() == 30 })
	waitFor(t, "alpha applies 1", 10*time.Second, func() bool { return recA.len() == 1 })

	// stop beta (server and replicator), publish on alpha, restart beta
	repB.Close()
	b.stop()
	for i := 30; i < 40; i++ {
		if err := a.bus.Publish(ctx, ev(fmt.Sprintf("a%02d", i), "alpha", fmt.Sprintf("I-%d", i%3), i)); err != nil {
			t.Fatal(err)
		}
	}
	b = startMachine(t, "beta", filepath.Join(base, "b"), portB)
	defer b.stop()
	curB2, err := NewFileCursorStore(cursorFile)
	if err != nil {
		t.Fatal(err)
	}
	if curB2.GetCursor("alpha") == 0 {
		t.Fatal("cursor not persisted")
	}
	repB2 := NewReplicator("beta", recB.apply, curB2)
	defer repB2.Close()
	if err := repB2.AddPeer("alpha", a.srv.ClientURL()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "beta catches up", 10*time.Second, func() bool { return recB.len() == 40 })

	// alpha reconnects to the restarted beta by itself
	if err := b.bus.Publish(ctx, ev("b01", "beta", "I-5", 1)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "alpha sees beta again", 15*time.Second, func() bool { return recA.len() == 2 })

	time.Sleep(200 * time.Millisecond)
	recB.mu.Lock()
	defer recB.mu.Unlock()
	if len(recB.order) != 40 {
		t.Fatalf("beta applied %d events, want 40", len(recB.order))
	}
	for id, n := range recB.counts {
		if n != 1 {
			t.Errorf("event %s applied %d times", id, n)
		}
		if id == "relayed" {
			t.Error("relayed event must not be applied")
		}
	}
	// order per item preserved
	last := map[string]int{}
	for _, e := range recB.order {
		p, _ := e.DecodePayload()
		var n int
		fmt.Sscan(p.Note, &n)
		if prev, ok := last[e.Subject]; ok && n <= prev {
			t.Errorf("item %s: %d after %d", e.Subject, n, prev)
		}
		last[e.Subject] = n
	}
	st := repB2.Peers()
	if len(st) != 1 || st[0].Name != "alpha" || st[0].Cursor == 0 {
		t.Errorf("peer status %+v", st)
	}
}

func TestReplicatorRetriesFailedApply(t *testing.T) {
	a := startMachine(t, "alpha", t.TempDir(), -1)
	defer a.stop()
	var mu sync.Mutex
	fails := 2
	var got []string
	rep := NewReplicator("beta", func(e domain.Event) error {
		mu.Lock()
		defer mu.Unlock()
		if fails > 0 {
			fails--
			return fmt.Errorf("busy")
		}
		got = append(got, e.ID)
		return nil
	}, nil)
	rep.MaxBackoff = 200 * time.Millisecond
	rep.Logf = t.Logf
	defer rep.Close()
	if err := rep.AddPeer("alpha", a.srv.ClientURL()); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for i := range 3 {
		if err := a.bus.Publish(ctx, ev(fmt.Sprint(i), "alpha", "I-1", i)); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "applied after retries", 10*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(got) == 3
	})
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(got, ",") != "0,1,2" {
		t.Fatalf("got %v", got)
	}
}

func TestAuthPeerPermissionsAndBlobs(t *testing.T) {
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
	auth, err := NkeyOption(seed)
	if err != nil {
		t.Fatal(err)
	}

	// without credentials the peer is rejected
	if nc, err := nats.Connect(a.srv.ClientURL()); err == nil {
		nc.Close()
		t.Fatal("anonymous connection accepted")
	}

	// with credentials it can replicate…
	rec := newRecorder()
	rep := NewReplicator("beta", rec.apply, nil)
	defer rep.Close()
	if err := rep.AddPeer("alpha", a.srv.ClientURL(), auth); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "replicated with auth", 10*time.Second, func() bool { return rec.len() == 1 })

	// …but not publish events into alpha's stream
	nc, err := nats.Connect(a.srv.ClientURL(), auth)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	peerBus := &Bus{nc: nc}
	peerBus.js, _ = jetstream.New(nc)
	pctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err := peerBus.Publish(pctx, ev("forged", "alpha", "I-1", 2)); err == nil {
		t.Fatal("peer must not publish into alpha's stream")
	}

	// RPC addressed to alpha is allowed
	if err := Serve(ctx, a.nc, RPCSubject("alpha", "ping"), func(_ context.Context, d []byte) ([]byte, error) {
		if string(d) == "boom" {
			return nil, fmt.Errorf("bad input")
		}
		return append([]byte("pong:"), d...), nil
	}); err != nil {
		t.Fatal(err)
	}
	out, err := rep.Request(ctx, "alpha", RPCSubject("alpha", "ping"), []byte("x"), time.Second, 2)
	if err != nil || string(out) != "pong:x" {
		t.Fatalf("request: %q %v", out, err)
	}
	if _, err := rep.Request(ctx, "alpha", RPCSubject("alpha", "ping"), []byte("boom"), time.Second, 0); err == nil || !strings.Contains(err.Error(), "bad input") {
		t.Fatalf("remote error: %v", err)
	}

	// blobs: alpha stores, beta fetches through the peer bucket with auth
	blobsA, err := NewObjectStore(ctx, t.TempDir(), a.bus.JetStream())
	if err != nil {
		t.Fatal(err)
	}
	content := bytes.Repeat([]byte("log line\n"), 50000) // > one chunk
	hash, err := blobsA.Put(ctx, bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	blobsB, err := NewObjectStore(ctx, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := blobsB.Get(ctx, hash); err == nil {
		t.Fatal("unknown blob must not be found")
	}
	blobsB.AddPeer("alpha", a.srv.ClientURL(), auth)
	r, err := blobsB.Get(ctx, hash)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(r)
	r.Close()
	if !bytes.Equal(got, content) {
		t.Fatal("blob content differs")
	}
	if blobsB.Path(hash) == "" || !strings.HasSuffix(blobsB.Path(hash), hash) {
		t.Fatal("path")
	}
	// cached: works without peers now
	blobsB.RemovePeer("alpha")
	r, err = blobsB.Get(ctx, hash)
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
}

func TestObjectStoreLocal(t *testing.T) {
	ctx := context.Background()
	s, err := NewObjectStore(ctx, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	h1, err := s.Put(ctx, strings.NewReader("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if h1 != "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824" {
		t.Fatalf("hash %s", h1)
	}
	h2, err := s.Put(ctx, strings.NewReader("hello"))
	if err != nil || h2 != h1 {
		t.Fatal("same content, same hash")
	}
	r, err := s.Get(ctx, h1)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(r)
	r.Close()
	if string(b) != "hello" {
		t.Fatal(string(b))
	}
	if _, err := s.Get(ctx, "../etc/passwd"); err == nil {
		t.Fatal("invalid hash accepted")
	}
}

func TestRequestRetriesUntilServed(t *testing.T) {
	m := startMachine(t, "solo", t.TempDir(), -1)
	defer m.stop()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	subj := RPCSubject("solo", "late")
	go func() {
		time.Sleep(300 * time.Millisecond)
		_ = Serve(ctx, m.nc, subj, func(context.Context, []byte) ([]byte, error) { return []byte("ok"), nil })
	}()
	out, err := Request(ctx, m.nc, subj, nil, 200*time.Millisecond, 6)
	if err != nil || string(out) != "ok" {
		t.Fatalf("%q %v", out, err)
	}
	if _, err := Request(ctx, m.nc, RPCSubject("solo", "nobody"), nil, 50*time.Millisecond, 1); err == nil {
		t.Fatal("expected failure")
	}
}

func TestHeartbeat(t *testing.T) {
	m := startMachine(t, "solo", t.TempDir(), -1)
	defer m.stop()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan domain.Event, 4)
	if err := m.bus.Subscribe(ctx, "p", func(e domain.Event) { got <- e }); err != nil {
		t.Fatal(err)
	}
	n := 0
	StartHeartbeat(ctx, m.bus, "p", func() domain.Machine { return domain.Machine{Name: "solo", Slots: 2} }, 50*time.Millisecond, func() string {
		n++
		return fmt.Sprint("hb", n)
	})
	select {
	case e := <-got:
		p, _ := e.DecodePayload()
		if e.Type != "machine.heartbeat" || p.MachineI == nil || p.MachineI.Name != "solo" {
			b, _ := json.Marshal(e)
			t.Fatalf("bad heartbeat %s", b)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no heartbeat")
	}
}
