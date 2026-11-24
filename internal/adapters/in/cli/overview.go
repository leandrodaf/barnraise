package cli

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	pb "github.com/leandrodaf/barnraise/gen/barnraise/v1"
)

func (e *env) overviewCmd() *cobra.Command {
	var q pb.OverviewRequest
	var limit, offset int
	c := &cobra.Command{Use: "overview", Short: "Every item on every machine: what runs, where, its worktrees, processes, pull request and cost",
		Long: "Every item on every machine: what it is doing, which machine holds it, its worktrees, the processes that run for it,\n" +
			"its pull request and what it cost. Without --project it covers every project of the daemon. --json gives the whole structure.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := e.ctx()
			defer cancel()
			q.Project, q.Limit, q.Offset = e.project, int32(limit), int32(offset)
			res, err := e.client().Overview(ctx, req(&q))
			if err != nil {
				return err
			}
			e.print(res.Msg, func(w io.Writer) { renderOverview(w, res.Msg) })
			return nil
		}}
	c.Flags().StringVar(&q.Item, "item", "", "only this item, with everything about it")
	c.Flags().StringSliceVar(&q.Activity, "activity", nil, "only these activities or statuses: working, running, idle, stalled, waiting_owner, blocked, paused, queued, ready, draft, integrated, closed")
	c.Flags().StringVar(&q.Machine, "machine", "", "only what is on this machine")
	c.Flags().StringVar(&q.Finished, "finished", "", "integrated and closed items of this long ago (default 7d)")
	c.Flags().BoolVar(&q.All, "all", false, "every finished item, whatever its age")
	c.Flags().BoolVar(&q.Brief, "brief", false, "without the per-item breakdowns and process lists")
	c.Flags().IntVar(&limit, "limit", 0, "items per project (default 500, at most 5000)")
	c.Flags().IntVar(&offset, "offset", 0, "skip this many items")
	return c
}

func renderOverview(w io.Writer, o *pb.OverviewResponse) {
	fmt.Fprintf(w, "Cluster seen from %s · %s\n", o.Machine, tokensText(o.Usage))
	for _, m := range o.Machines {
		mm := m.GetMachine()
		fmt.Fprintf(w, "\nMachine %s (%s) · %d sessions · %d items · %d worktrees %s · disk free %s of %s%s\n", mm.GetName(), def(mm.GetHealth(), "unknown"),
			len(m.Sessions), len(m.Items), len(m.Worktrees), bytesText(m.WorktreeBytes), bytesText(m.DiskFreeBytes), bytesText(m.DiskTotalBytes), procsText(m.Procs))
		fmt.Fprintf(w, "  spent here: %s\n", tokensText(m.Usage))
		if !m.ProcsSupported && len(m.Worktrees) > 0 {
			fmt.Fprintln(w, "  processes are not measured on this machine")
		}
	}
	for _, p := range o.Projects {
		fmt.Fprintf(w, "\nProject %s", p.Project)
		if p.Paused {
			fmt.Fprintf(w, " · PAUSED: %s", p.PausedWhy)
		}
		fmt.Fprintf(w, " · %s\n", tokensText(p.Usage))
		var acts []string
		for k, n := range p.Activity {
			acts = append(acts, fmt.Sprintf("%s %d", k, n))
		}
		sort.Strings(acts)
		fmt.Fprintf(w, "  %s\n", strings.Join(acts, " · "))
		for _, it := range p.Items {
			where := def(it.Machine, "-")
			fmt.Fprintf(w, "  %-7s %-13s %-12s %-10s %s\n", it.Id, it.Activity, def(it.Step, "-"), where, it.Title)
			var facts []string
			if it.Detail != "" {
				facts = append(facts, it.Detail)
			}
			if it.Pr != nil {
				facts = append(facts, fmt.Sprintf("PR #%d %s", it.Pr.Number, it.Pr.State))
			}
			if it.TrackerRef != "" {
				facts = append(facts, "tracker "+it.TrackerRef)
			}
			if it.Usage.GetTokens() > 0 {
				facts = append(facts, tokensText(it.Usage))
			}
			for _, wt := range it.Worktrees {
				facts = append(facts, fmt.Sprintf("worktree on %s %s%s", wt.Machine, bytesText(wt.SizeBytes), procsText(wt.Procs)))
			}
			if len(facts) > 0 {
				fmt.Fprintf(w, "          %s\n", strings.Join(facts, " · "))
			}
			for _, pr := range it.GetProcs().GetTop() {
				fmt.Fprintf(w, "          %-9s %5.0f%% cpu %8s  %s\n", pr.Kind, pr.CpuPct, bytesText(pr.RssBytes), pr.Command)
			}
		}
		if p.Truncated {
			fmt.Fprintf(w, "  … %d of %d matching items shown; use --limit and --offset for the rest\n", len(p.Items), p.ItemsMatched)
		}
	}
}

func tokensText(u *pb.UsageTotals) string {
	if u.GetSessions() == 0 {
		return "no sessions yet"
	}
	s := fmt.Sprintf("%s tokens in %d sessions, %.1f agent hours", countText(u.Tokens), u.Sessions, u.Seconds/3600)
	if u.CostUsd > 0 {
		s += fmt.Sprintf(", $%.2f", u.CostUsd)
	}
	if u.CpuSeconds > 0 {
		s += fmt.Sprintf(", %.1f cpu hours", u.CpuSeconds/3600)
	}
	return s
}

func procsText(p *pb.ProcSummary) string {
	if p.GetCount() == 0 {
		return ""
	}
	return fmt.Sprintf(" · %d processes %.0f%% cpu %s", p.Count, p.CpuPct, bytesText(p.RssBytes))
}

func countText(n int64) string {
	switch {
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.2fB", float64(n)/1e9)
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	}
	return fmt.Sprint(n)
}

func bytesText(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f kB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
