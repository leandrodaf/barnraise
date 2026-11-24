package main

import (
	"encoding/json"
	"os/exec"
	"testing"
	"time"
)

type overviewJSON struct {
	Machine  string `json:"machine"`
	Projects []struct {
		Project  string         `json:"project"`
		Activity map[string]int `json:"activity"`
		Usage    struct {
			Tokens   string `json:"tokens"`
			Sessions int    `json:"sessions"`
		} `json:"usage"`
		Items []struct {
			ID       string `json:"id"`
			Activity string `json:"activity"`
			Machine  string `json:"machine"`
			Usage    struct {
				Tokens string `json:"tokens"`
			} `json:"usage"`
			UsageByMachine map[string]struct {
				Sessions int `json:"sessions"`
			} `json:"usage_by_machine"`
			Checks []struct {
				Check  string `json:"check"`
				Passed bool   `json:"passed"`
				RunsOn string `json:"runs_on"`
			} `json:"checks"`
		} `json:"items"`
	} `json:"projects"`
	Machines []struct {
		Machine struct {
			Name   string `json:"name"`
			Health string `json:"health"`
		} `json:"machine"`
		Worktrees []struct {
			Kind    string `json:"kind"`
			Machine string `json:"machine"`
			Path    string `json:"path"`
			Head    string `json:"head"`
		} `json:"worktrees"`
		ReportedAt     string `json:"reported_at"`
		DiskTotalBytes string `json:"disk_total_bytes"`
	} `json:"machines"`
}

// overview asks a machine's daemon for the cluster's overview.
func (m *machineEnv) overview(t *testing.T, bin string, args ...string) *overviewJSON {
	t.Helper()
	cmd := exec.Command(bin, append([]string{"overview", "--json"}, args...)...)
	cmd.Dir, cmd.Env = m.root, m.env
	out, err := cmd.Output()
	if err != nil {
		return nil // the daemon is still starting
	}
	var o overviewJSON
	if err := json.Unmarshal(out, &o); err != nil {
		t.Fatalf("overview is not the JSON it promises: %v\n%s", err, out)
	}
	return &o
}

// waitOverviewOfPeer waits until m's overview shows what peer keeps on its
// own disk: that only arrives as live state from peer itself.
func (m *machineEnv) waitOverviewOfPeer(t *testing.T, bin, peer string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		o := m.overview(t, bin)
		if o != nil {
			for _, mc := range o.Machines {
				if mc.Machine.Name != peer || mc.ReportedAt == "" {
					continue
				}
				for _, w := range mc.Worktrees {
					if w.Kind == "root" && w.Machine == peer && w.Path != "" && w.Head != "" {
						if mc.DiskTotalBytes == "" || mc.DiskTotalBytes == "0" {
							t.Fatalf("%s reported its worktrees without its disk: %+v", peer, mc)
						}
						return
					}
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never learned what %s keeps: %+v", m.name, peer, o)
		}
		time.Sleep(300 * time.Millisecond)
	}
}
