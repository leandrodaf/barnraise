package main

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type capsJSON struct {
	Machines []struct {
		Machine string `json:"machine"`
		Error   string `json:"error"`
		Items   []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"items"`
		Counts map[string]int `json:"counts"`
	} `json:"machines"`
}

type toolsJSON struct {
	Results []struct {
		Machine string `json:"machine"`
		Tool    string `json:"tool"`
		Ok      bool   `json:"ok"`
		Error   string `json:"error"`
	} `json:"results"`
}

// TestCapabilitiesAcrossMachines: one machine answers for itself and for
// its peer, with the daemon running; with it stopped, only for itself. A
// tool removal asked on alpha runs on beta and comes back from beta.
func TestCapabilitiesAcrossMachines(t *testing.T) {
	if testing.Short() {
		t.Skip("cluster")
	}
	bin, base, alpha, beta := pairedMachines(t)

	// Without a daemon: this machine only, probed by the command itself.
	var local capsJSON
	if err := json.Unmarshal([]byte(alpha.run(t, bin, "capabilities", "--json")), &local); err != nil {
		t.Fatal(err)
	}
	if len(local.Machines) != 1 || local.Machines[0].Machine != "alpha" || len(local.Machines[0].Items) < 20 {
		t.Fatalf("offline report %+v", local)
	}
	cmd := exec.Command(bin, "capabilities", "--machine", "beta")
	cmd.Dir, cmd.Env = alpha.root, alpha.env
	if out, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(out), "daemon is not running") {
		t.Fatalf("peer without a daemon: %v %s", err, out)
	}

	alpha.start(t, bin, filepath.Join(base, "alpha.log"))
	beta.start(t, bin, filepath.Join(base, "beta.log"))
	alpha.waitConnected(t, "beta")
	var all capsJSON
	if err := json.Unmarshal([]byte(alpha.run(t, bin, "capabilities", "--all", "--json")), &all); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, m := range all.Machines {
		if m.Error != "" || m.Counts["active"] == 0 {
			t.Fatalf("%s: %+v", m.Machine, m)
		}
		got[m.Machine] = true
	}
	if !got["alpha"] || !got["beta"] || len(all.Machines) != 2 {
		t.Fatalf("machines %v", got)
	}
	if out := alpha.run(t, bin, "capabilities", "--machine", "beta"); !strings.Contains(out, "Machine beta") || strings.Contains(out, "Machine alpha") {
		t.Fatalf("beta only: %s", out)
	}

	var rm toolsJSON
	if err := json.Unmarshal([]byte(alpha.run(t, bin, "tools", "remove", "rtk", "--machine", "beta", "--json")), &rm); err != nil {
		t.Fatal(err)
	}
	if len(rm.Results) != 1 || rm.Results[0].Machine != "beta" || !rm.Results[0].Ok {
		t.Fatalf("remove on beta %+v", rm)
	}

	// The heartbeat carries the counts to the machines view.
	deadline := time.Now().Add(40 * time.Second)
	for {
		out := alpha.run(t, bin, "machine", "list", "--json")
		if strings.Contains(out, `"capability_counts"`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no capability counts in the machines list: %s", out)
		}
		time.Sleep(time.Second)
	}
}
