package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/leandrodaf/barnraise/gen/barnraise/v1"
	"github.com/leandrodaf/barnraise/gen/barnraise/v1/barnraisev1connect"
	"github.com/leandrodaf/barnraise/internal/app"
	"github.com/leandrodaf/barnraise/internal/testkit"
)

// The panel switches another machine alone: this one stays as it is.
func TestSetStandbyOfAnotherMachineOnly(t *testing.T) {
	svc := app.New(app.Deps{Store: testkit.NewStore(), Clock: testkit.NewClock(time.Now()), Machine: "m1", Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	type call struct {
		on        bool
		peers     []string
		peersOnly bool
	}
	var got []call
	set := func(_ context.Context, on bool, _ string, peers []string, peersOnly bool) (*pb.StandbyResponse, error) {
		got = append(got, call{on, peers, peersOnly})
		out := &pb.StandbyResponse{On: on}
		for _, p := range peers {
			out.Peers = append(out.Peers, &pb.PeerResult{Name: p, Ok: true})
		}
		return out, nil
	}
	hs := httptest.NewServer(New(svc, Options{OwnerToken: "owner", SetStandby: set}).Handler())
	defer hs.Close()
	c := barnraisev1connect.NewBarnraiseServiceClient(hs.Client(), hs.URL, connect.WithInterceptors(connect.UnaryInterceptorFunc(
		func(next connect.UnaryFunc) connect.UnaryFunc {
			return func(ctx context.Context, r connect.AnyRequest) (connect.AnyResponse, error) {
				r.Header().Set("Authorization", "Bearer owner")
				return next(ctx, r)
			}
		})))
	ctx := context.Background()
	if _, err := c.SetStandby(ctx, connect.NewRequest(&pb.StandbyRequest{On: true, PeersOnly: true})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("peers_only without peers: %v", err)
	}
	r, err := c.SetStandby(ctx, connect.NewRequest(&pb.StandbyRequest{On: true, Peers: []string{"m2"}, PeersOnly: true}))
	if err != nil || len(r.Msg.Peers) != 1 || !r.Msg.Peers[0].Ok {
		t.Fatalf("switch m2: %v %v", r, err)
	}
	if _, err := c.SetStandby(ctx, connect.NewRequest(&pb.StandbyRequest{On: false, Peers: []string{"m2", "m3"}})); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !got[0].peersOnly || got[0].peers[0] != "m2" || got[1].peersOnly || len(got[1].peers) != 2 {
		t.Fatalf("calls %+v", got)
	}
}
