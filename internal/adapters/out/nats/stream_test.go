package nats

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

// A peer with only the peer permissions opens a stream on alpha: the first
// answer, then the flow; closing its side stops alpha's sender; a sender
// whose asker vanishes stops when the lease runs out.
func TestStreamBetweenMachines(t *testing.T) {
	old := StreamKeep
	StreamKeep = 50 * time.Millisecond
	t.Cleanup(func() { StreamKeep = old })
	seed, pub, err := NewMachineKey()
	if err != nil {
		t.Fatal(err)
	}
	a := startMachine(t, "alpha", t.TempDir(), -1, PeerPermissions(pub, "alpha"))
	defer a.stop()
	auth, err := NkeyOption(seed)
	if err != nil {
		t.Fatal(err)
	}
	peer, err := nats.Connect(a.srv.ClientURL(), auth)
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stopped atomic.Int32
	feeds := make(chan chan []byte, 4)
	subj := RPCSubject("alpha", "live")
	if err := ServeStream(ctx, a.nc, subj, func(_ context.Context, d []byte) ([]byte, <-chan []byte, func(), error) {
		if string(d) == "none" {
			return nil, nil, nil, errors.New("no session")
		}
		ch := make(chan []byte, 8)
		feeds <- ch
		return append([]byte("hello:"), d...), ch, func() { stopped.Add(1) }, nil
	}); err != nil {
		t.Fatal(err)
	}

	if _, _, err := RequestStream(ctx, peer, subj, []byte("none"), time.Second); err == nil || err.Error() != "remote: no session" {
		t.Fatalf("refused open: %v", err)
	}

	wctx, wcancel := context.WithCancel(ctx)
	first, next, err := RequestStream(wctx, peer, subj, []byte("I-1"), time.Second)
	if err != nil || string(first) != "hello:I-1" {
		t.Fatalf("open: %q %v", first, err)
	}
	feed := <-feeds
	feed <- []byte("line 1")
	feed <- []byte("line 2")
	for _, want := range []string{"line 1", "line 2"} {
		select {
		case got := <-next:
			if string(got) != want {
				t.Fatalf("got %q want %q", got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("no %q", want)
		}
	}
	// Several renewals pass: the stream stays open.
	time.Sleep(4 * StreamKeep)
	if stopped.Load() != 0 {
		t.Fatal("a renewed stream must stay open")
	}
	wcancel()
	waitFor(t, "the sender to stop", 2*time.Second, func() bool { return stopped.Load() == 1 })
	for range next { // closed on the asker's side
	}

	// The sender ends: the asker's flow closes.
	_, next, err = RequestStream(ctx, peer, subj, []byte("I-2"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	close(<-feeds)
	select {
	case _, ok := <-next:
		if ok {
			t.Fatal("expected the end of the flow")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the flow did not end with the sender")
	}

	// An asker that vanishes without a word: the lease runs out.
	gone, err := nats.Connect(a.srv.ClientURL(), auth)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := RequestStream(ctx, gone, subj, []byte("I-3"), time.Second); err != nil {
		t.Fatal(err)
	}
	<-feeds
	before := stopped.Load()
	gone.Close()
	waitFor(t, "the lease to run out", 3*time.Second, func() bool { return stopped.Load() > before })
}
