package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	pb "github.com/leandrodaf/barnraise/gen/barnraise/v1"
	"github.com/leandrodaf/barnraise/internal/adapters/in/httpapi"
	"github.com/leandrodaf/barnraise/internal/adapters/out/capprobe"
	"github.com/leandrodaf/barnraise/internal/adapters/out/execx"
	"github.com/leandrodaf/barnraise/internal/adapters/out/toolinstall"
	"github.com/leandrodaf/barnraise/internal/config"
	"github.com/leandrodaf/barnraise/internal/daemoncfg"
	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/paths"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// `barnraise capabilities` and `barnraise tools` (ADR-0045): what runs on
// each machine, what falls back and why, and the rootless installs of the
// optional tools. With the daemon down they work on this machine alone.

var capSymbols = map[string]string{
	domain.CapActive: "✔", domain.CapAvailable: "○", domain.CapDegraded: "◐", domain.CapMissing: "✘", domain.CapUnsupported: "-",
}

// machineFlags are --machine and --all.
type machineFlags struct {
	machines []string
	all      bool
}

func (m *machineFlags) add(c *cobra.Command, what string) {
	c.Flags().StringSliceVar(&m.machines, "machine", nil, what+" on this machine (repeat, or comma-separated; default: this machine)")
	c.Flags().BoolVar(&m.all, "all", false, what+" on every connected machine")
}

func (m machineFlags) check() error {
	if m.all && len(m.machines) > 0 {
		return errors.New("--all and --machine contradict each other: choose one")
	}
	return nil
}

// onlyHere reports whether the flags name this machine alone.
func (m machineFlags) onlyHere(self string) bool {
	if m.all {
		return false
	}
	for _, n := range m.machines {
		if n != self {
			return false
		}
	}
	return true
}

func (e *env) capabilitiesCmd() *cobra.Command {
	var mf machineFlags
	var refresh bool
	c := &cobra.Command{Use: "capabilities", Aliases: []string{"caps"}, Args: cobra.NoArgs,
		Short: "What is active on each machine, what falls back and why, and how to fix it",
		Long: "Lists every Barnraise feature whose working depends on the machine or on a switch, with its\n" +
			"status: active, available (all it needs is here; switched off), degraded (on, running on a\n" +
			"fallback), missing (cannot run here) or unsupported (not for this system), the reason, what\n" +
			"runs instead, and the exact fix: `barnraise tools install <tool>` for the tools Barnraise\n" +
			"installs without root, or the command or configuration key you run yourself (Barnraise never\n" +
			"runs anything that needs root).\n\n" +
			"Switches of barnraise.yaml are read from --project (default: the project of the current folder;\n" +
			"none: every project of the machine). With the daemon stopped it probes this machine itself.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := mf.check(); err != nil {
				return err
			}
			res, err := e.capabilityReports(cmd.Context(), mf, refresh)
			if err != nil {
				return err
			}
			e.print(res, func(w io.Writer) { writeCapabilities(w, res) })
			return nil
		}}
	mf.add(c, "probe")
	c.Flags().BoolVar(&refresh, "refresh", false, "probe again instead of using the report of the last minutes")
	return c
}

// capabilityReports asks the daemon, or probes this machine when it is down.
func (e *env) capabilityReports(ctx context.Context, mf machineFlags, refresh bool) (*pb.CapabilitiesResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	project := e.proj(ctx)
	if e.daemonRunning() {
		res, err := e.client().Capabilities(ctx, req(&pb.CapabilitiesRequest{Machines: mf.machines, All: mf.all, Project: project, Refresh: refresh}))
		if err != nil {
			return nil, err
		}
		return res.Msg, nil
	}
	df, err := daemoncfg.Load()
	if err != nil {
		return nil, err
	}
	if !mf.onlyHere(df.Machine) {
		return nil, errors.New("the daemon is not running: only this machine can be probed (other machines are reached through it; barnraise up)")
	}
	rep := localCapabilities(ctx, df, project)
	return &pb.CapabilitiesResponse{Machines: []*pb.MachineCapabilities{httpapi.CapabilityReportPB(rep)}}, nil
}

// localCapabilities evaluates this machine without the daemon.
func localCapabilities(ctx context.Context, df *daemoncfg.File, project string) domain.CapabilityReport {
	var cfgs []*domain.ProjectConfig
	for _, root := range df.Projects {
		cfg, err := config.Load(root)
		if err != nil {
			continue
		}
		cfgs = append(cfgs, cfg)
	}
	if project != "" {
		var only []*domain.ProjectConfig
		for _, c := range cfgs {
			if c.Name == project {
				only = append(only, c)
			}
		}
		if len(only) > 0 {
			cfgs = only
		}
	}
	settings := df.CapabilitySettings("") // the egress proxy runs with the daemon only
	data := paths.DataDir()
	p := capprobe.New(capprobe.Options{Exec: execx.New(), DataDir: data, ToolsBin: toolinstall.BinDir(toolinstall.Root(data))})
	probe := p.Probe(ctx, ports.ProbeRequest{Programs: domain.CapabilityPrograms(cfgs, settings)})
	return domain.BuildCapabilityReport(df.Machine, domain.CapabilityInput{Probe: probe, Settings: settings, Projects: cfgs})
}

func groupOrder(g string) int {
	if i := slices.Index(domain.CapGroups, g); i >= 0 {
		return i
	}
	return len(domain.CapGroups)
}

func writeCapabilities(w io.Writer, res *pb.CapabilitiesResponse) {
	for i, m := range res.Machines {
		if i > 0 {
			fmt.Fprintln(w)
		}
		if m.Error != "" {
			fmt.Fprintf(w, "Machine %s: %s\n", m.Machine, m.Error)
			continue
		}
		var counts []string
		for _, s := range domain.CapStatuses {
			if n := m.Counts[s]; n > 0 {
				counts = append(counts, fmt.Sprintf("%d %s", n, s))
			}
		}
		fmt.Fprintf(w, "Machine %s (%s), probed %s: %s\n", m.Machine, m.Platform, when(m.ProbedAt), strings.Join(counts, ", "))
		items := slices.Clone(m.Items)
		slices.SortStableFunc(items, func(a, b *pb.CapabilityItem) int { return groupOrder(a.Group) - groupOrder(b.Group) })
		group := ""
		for _, it := range items {
			if it.Group != group {
				group = it.Group
				fmt.Fprintf(w, "\n%s\n", group)
			}
			ver := ""
			if it.Version != "" && it.Version != "installed" {
				ver = " (" + it.Version + ")"
			}
			fmt.Fprintf(w, "  %s %-11s %s%s  [%s]\n", capSymbols[it.Status], it.Status, it.Title, ver, it.Id)
			if it.Status == domain.CapActive {
				continue
			}
			if it.Reason != "" {
				fmt.Fprintf(w, "      why:   %s\n", it.Reason)
			}
			if it.Fallback != "" && it.Status == domain.CapDegraded {
				fmt.Fprintf(w, "      falls back: %s\n", it.Fallback)
			}
			if it.Fix != "" {
				root := ""
				if it.NeedsRoot {
					root = "  (needs root: Barnraise never runs it)"
				}
				fmt.Fprintf(w, "      fix:   %s%s\n", it.Fix, root)
			}
		}
	}
}

func (e *env) toolsCmd() *cobra.Command {
	c := &cobra.Command{Use: "tools", Short: "The optional tools Barnraise installs without root (mergiraf, rtk, osv-scanner, ast-grep, smolvm)",
		Long: "Barnraise installs these tools at a pinned version, from their official release, after checking\n" +
			"the sha256 written in Barnraise, into <data dir>/tools; their programs are linked into\n" +
			"<data dir>/tools/bin, which comes first on the PATH of the daemon, setup, checks, hooks and agent\n" +
			"sessions. Nothing that needs root is installed: `barnraise capabilities` shows that command instead."}
	var listFlags machineFlags
	list := &cobra.Command{Use: "list", Short: "The installable tools and where each stands on the machines", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := listFlags.check(); err != nil {
				return err
			}
			res, err := e.capabilityReports(cmd.Context(), listFlags, false)
			if err != nil {
				return err
			}
			e.print(res, func(w io.Writer) { writeTools(w, res) })
			return nil
		}}
	listFlags.add(list, "list")
	c.AddCommand(list, e.toolsInstallCmd(false), e.toolsInstallCmd(true))
	return c
}

func (e *env) toolsInstallCmd(remove bool) *cobra.Command {
	var mf machineFlags
	use, short := "install <tool>...", "Install tools (or upgrade them to the pinned version)"
	if remove {
		use, short = "remove <tool>...", "Remove tools Barnraise installed"
	}
	c := &cobra.Command{Use: use, Short: short, Args: cobra.MinimumNArgs(1),
		ValidArgs: domain.ToolIDs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := mf.check(); err != nil {
				return err
			}
			for _, a := range args {
				if !slices.Contains(domain.ToolIDs(), a) {
					return fmt.Errorf("unknown tool %q (known: %s)", a, strings.Join(domain.ToolIDs(), ", "))
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
			defer cancel()
			var results []*pb.ToolInstallResult
			if e.daemonRunning() {
				res, err := e.client().InstallTools(ctx, req(&pb.InstallToolsRequest{Tools: args, Machines: mf.machines, All: mf.all, Remove: remove}))
				if err != nil {
					return err
				}
				results = res.Msg.Results
			} else {
				df, err := daemoncfg.Load()
				if err != nil {
					return err
				}
				if !mf.onlyHere(df.Machine) {
					return errors.New("the daemon is not running: only this machine can be changed (barnraise up reaches the others)")
				}
				in := toolinstall.New(toolinstall.Options{Root: toolinstall.Root(paths.DataDir())})
				for _, id := range args {
					var r domain.ToolResult
					if remove {
						r = in.Remove(ctx, id)
					} else {
						fmt.Fprintf(e.errOut, "Downloading %s…\n", id)
						r = in.Install(ctx, id)
					}
					r.Machine = df.Machine
					results = append(results, httpapi.ToolResultsPB([]domain.ToolResult{r})...)
				}
			}
			out := &pb.InstallToolsResponse{Results: results}
			e.print(out, func(w io.Writer) { writeToolResults(w, results, remove) })
			for _, r := range results {
				if !r.Ok {
					return exitCode(1)
				}
			}
			return nil
		}}
	mf.add(c, map[bool]string{false: "install", true: "remove"}[remove])
	return c
}

func writeTools(w io.Writer, res *pb.CapabilitiesResponse) {
	for i, m := range res.Machines {
		if i > 0 {
			fmt.Fprintln(w)
		}
		if m.Error != "" {
			fmt.Fprintf(w, "Machine %s: %s\n", m.Machine, m.Error)
			continue
		}
		fmt.Fprintf(w, "Machine %s (%s)\n", m.Machine, m.Platform)
		for _, t := range m.Tools {
			state := "not installed"
			switch {
			case t.Installed != "" && t.Managed:
				state = "installed by Barnraise: " + t.Installed
			case t.Installed != "":
				state = "on the PATH: " + t.Installed + " (" + t.Path + ")"
			}
			if !t.Available {
				state += "; no build for this platform"
			}
			fmt.Fprintf(w, "  %-12s pinned %-8s %s  — %s\n", t.Id, t.Pinned, state, t.Title)
		}
	}
}

func writeToolResults(w io.Writer, results []*pb.ToolInstallResult, remove bool) {
	for _, r := range results {
		switch {
		case !r.Ok:
			fmt.Fprintf(w, "✘ %s on %s: %s\n", r.Tool, r.Machine, r.Error)
		case !r.Changed && remove:
			fmt.Fprintf(w, "✔ %s on %s: not installed by Barnraise; nothing to remove\n", r.Tool, r.Machine)
		case !r.Changed:
			fmt.Fprintf(w, "✔ %s %s on %s: already installed\n", r.Tool, r.Version, r.Machine)
		case remove:
			fmt.Fprintf(w, "✔ %s removed from %s\n", r.Tool, r.Machine)
		default:
			fmt.Fprintf(w, "✔ %s %s installed on %s\n", r.Tool, r.Version, r.Machine)
		}
	}
}

// capabilityHint tells, after `barnraise up`, what is switched on but
// cannot run as designed on this machine.
func (e *env) capabilityHint(ctx context.Context, project string) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	res, err := e.client().Capabilities(ctx, req(&pb.CapabilitiesRequest{Project: project}))
	if err != nil || len(res.Msg.Machines) == 0 {
		return
	}
	var names []string
	for _, it := range res.Msg.Machines[0].Items {
		if (it.Status == domain.CapDegraded || it.Status == domain.CapMissing) && attentionOn(it) {
			names = append(names, it.Id+" ("+it.Status+")")
		}
	}
	if len(names) > 0 {
		fmt.Fprintf(e.out, "  Not all runs as designed here: %s. See `barnraise capabilities`.\n", strings.Join(names, ", "))
	}
}

// attentionOn: the proto carries no switch, so an item counts when it is
// degraded (always on: degraded means it runs) or missing for a reason
// other than being switched off.
func attentionOn(it *pb.CapabilityItem) bool {
	return it.Status == domain.CapDegraded || !strings.HasPrefix(it.Reason, "Switched off")
}
