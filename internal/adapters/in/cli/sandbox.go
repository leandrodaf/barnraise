package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/spf13/cobra"

	"github.com/leandrodaf/barnraise/internal/adapters/out/landlock"
	"github.com/leandrodaf/barnraise/internal/adapters/out/microvm"
	"github.com/leandrodaf/barnraise/internal/daemoncfg"
	"github.com/leandrodaf/barnraise/internal/domain"
)

// sandboxExecCmd is the hidden helper the daemon wraps confined commands
// with: it applies the confinement to itself, then becomes the command.
func (e *env) sandboxExecCmd() *cobra.Command {
	var spec string
	c := &cobra.Command{Use: landlock.ExecCommand + " --spec <json> -- <command…>", Hidden: true, Args: cobra.MinimumNArgs(1),
		Short: "Run a command confined by Landlock (used by the daemon)",
		RunE: func(cmd *cobra.Command, args []string) error {
			err := landlock.Exec(spec, args)
			// Exec only returns on failure; the command never runs unconfined.
			fmt.Fprintln(e.errOut, "barnraise sandbox:", err)
			return exitCode(126)
		}}
	c.Flags().StringVar(&spec, "spec", "", "the confinement, as JSON")
	return c
}

// sandboxStatus is what `barnraise sandbox status` shows.
type sandboxStatus struct {
	Bubblewrap string                  `json:"bubblewrap"`
	Layers     []domain.SandboxSupport `json:"layers"`
	Profiles   []domain.SandboxProfile `json:"profiles"`
}

func (e *env) sandboxCmd() *cobra.Command {
	c := &cobra.Command{Use: "sandbox", Short: "How this machine confines agents, setup and checks"}
	status := &cobra.Command{Use: "status", Short: "Bubblewrap, Landlock (ABI version) and the microVM runner on this machine, and the profiles", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			st := sandboxStatus{Bubblewrap: bwrapState()}
			exe, _ := os.Executable()
			st.Layers = append(st.Layers, landlock.New(exe).Support())
			if df, err := daemoncfg.Load(); err == nil && df.Isolation.Backend == "microvm" {
				m := df.Isolation.MicroVM
				st.Layers = append(st.Layers, microvm.New(microvm.Options{Runner: m.Runner, Template: m.Command, Image: m.Image}).Support())
			}
			for _, n := range domain.SandboxProfileNames() {
				p, _ := domain.LookupSandboxProfile(n)
				st.Profiles = append(st.Profiles, p)
			}
			e.printSandbox(st)
			return nil
		}}
	c.AddCommand(status)
	return c
}

func (e *env) printSandbox(st sandboxStatus) {
	if e.json {
		data, _ := json.MarshalIndent(st, "", "  ")
		fmt.Fprintln(e.out, string(data))
		return
	}
	writeSandbox(e.out, st)
}

func writeSandbox(w io.Writer, st sandboxStatus) {
	fmt.Fprintf(w, "bubblewrap  %s\n", st.Bubblewrap)
	for _, l := range st.Layers {
		fmt.Fprintf(w, "%-11s %s\n", l.Backend, l.String())
	}
	fmt.Fprintln(w, "Profiles:")
	for _, p := range st.Profiles {
		fmt.Fprintf(w, "  %-7s network %-10s %s\n", p.Name, p.Network, p.About)
	}
}

// bwrapState probes bubblewrap the way the daemon does.
func bwrapState() string {
	if _, err := exec.LookPath("bwrap"); err != nil {
		return "not installed"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if exec.CommandContext(ctx, "bwrap", "--ro-bind", "/", "/", "--dev", "/dev", "--unshare-net", "true").Run() != nil {
		return "installed, but cannot start here (user namespaces refused)"
	}
	return "works"
}
