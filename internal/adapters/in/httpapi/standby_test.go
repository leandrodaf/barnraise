package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	pb "github.com/leandrodaf/barnraise/gen/barnraise/v1"
	"github.com/leandrodaf/barnraise/internal/app"
	"github.com/leandrodaf/barnraise/internal/testkit"
)

// The panel switches another machine alone: the one serving it stays as it is.
func TestMachineStandbyRoute(t *testing.T) {
	svc := app.New(app.Deps{Store: testkit.NewStore(), Clock: testkit.NewClock(time.Now()), Machine: "m1", Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	type call struct {
		on        bool
		peers     []string
		peersOnly bool
	}
	var got []call
	set := func(_ context.Context, on bool, _ string, peers []string, peersOnly bool) (*pb.StandbyResponse, error) {
		got = append(got, call{on, peers, peersOnly})
		return &pb.StandbyResponse{On: on}, nil
	}
	hs := httptest.NewServer(New(svc, Options{OwnerToken: "owner", SetStandby: set}).Handler())
	defer hs.Close()
	post := func(path, token, body string) int {
		req, _ := http.NewRequest("POST", hs.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	if c := post("/v1/machines/m2/standby", "owner", `{"on":true,"why":"x"}`); c != 200 {
		t.Fatalf("m2: %d", c)
	}
	if c := post("/v1/machines/m1/standby", "owner", `{"on":false}`); c != 200 {
		t.Fatalf("m1: %d", c)
	}
	if c := post("/v1/machines/m2/standby", "owner", `nope`); c != http.StatusBadRequest {
		t.Fatalf("bad body: %d", c)
	}
	tok := svc.Tokens().Issue(app.Actor{Kind: app.ActorAgent, Name: "a", Project: "demo", Scopes: app.RoleScopes("implementer", false)}, time.Hour)
	if c := post("/v1/machines/m2/standby", tok, `{"on":true}`); c != http.StatusForbidden {
		t.Fatalf("agent: %d", c)
	}
	if len(got) != 2 || !got[0].on || !got[0].peersOnly || got[0].peers[0] != "m2" || got[1].on || got[1].peersOnly || len(got[1].peers) != 0 {
		t.Fatalf("calls %+v", got)
	}
}
