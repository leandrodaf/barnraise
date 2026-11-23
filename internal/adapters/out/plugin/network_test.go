package plugin

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestNetworkContract(t *testing.T) {
	h, _ := newHost(t, nil)
	st, err := h.NetworkStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Self.Name != "alpha" || st.Self.Addresses[0] != "127.0.0.1" || len(st.Self.Tags) != 1 {
		t.Fatalf("self %+v", st.Self)
	}
	if len(st.Peers) != 3 || st.Peers[1].Name != "beta" || !st.Peers[1].Online || st.Peers[0].Online {
		t.Fatalf("peers %+v", st.Peers)
	}
}

func TestNetworkBadAnswers(t *testing.T) {
	for name, c := range map[string]struct {
		args []string
		want string
	}{
		"not a network plugin": {[]string{"-kinds=tracker"}, "does not implement network (it declares: tracker)"},
		"no self address":      {[]string{"-network=noself"}, "no address for this machine"},
		"a name as address":    {[]string{"-network=badaddr"}, `address "beta.example" for peer beta, which is not an IP address`},
		"a self name":          {[]string{"-self=alpha.example"}, `self address "alpha.example"`},
		"an error":             {[]string{"-network=error"}, "the overlay is down"},
	} {
		t.Run(name, func(t *testing.T) {
			h, _ := newHost(t, c.args)
			_, err := h.NetworkStatus(context.Background())
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want %q", err, c.want)
			}
		})
	}
}

func TestNetworkTimeout(t *testing.T) {
	h, _ := newHost(t, []string{"-network=hang"}, func(o *Options) { o.Def.Timeout = 200 * time.Millisecond })
	start := time.Now()
	_, err := h.NetworkStatus(context.Background())
	if err == nil || !strings.Contains(err.Error(), "did not answer network.status") {
		t.Fatal(err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("the timeout did not bound the call")
	}
}
