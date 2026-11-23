package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/types/known/structpb"

	pb "github.com/leandrodaf/barnraise/gen/barnraise/v1"
	"github.com/leandrodaf/barnraise/internal/config"
)

func findRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, config.FileName)); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("no barnraise.yaml here or above: run `barnraise init`")
		}
		dir = parent
	}
}

func projectNameAt(root string) string {
	cfg, err := config.Load(root)
	if err != nil {
		return ""
	}
	return cfg.Name
}

func (e *env) statusCmd() *cobra.Command {
	return &cobra.Command{Use: "status", Short: "Goal, focus, queue, agents, accounts and machines in one look", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := e.ctx()
			defer cancel()
			res, err := e.client().Status(ctx, req(&pb.ProjectRef{Project: e.proj(ctx)}))
			if err != nil {
				return err
			}
			st := res.Msg
			e.print(st, func(w io.Writer) {
				fmt.Fprintf(w, "Project %s · machine %s\n", st.Project, st.Machine)
				if st.Paused {
					fmt.Fprintf(w, "PAUSED: %s\n", st.PausedWhy)
				}
				if st.Goal != nil {
					p := st.Progress
					fmt.Fprintf(w, "Goal %s: %s — %d%% (%d of %d), pace %.1f/day", st.Goal.Id, st.Goal.Name, p.GetPercentage(), p.GetDone(), p.GetTotal(), p.GetPerDay())
					if p.GetForecast() != "" && !strings.HasPrefix(p.GetForecast(), "0001") {
						fmt.Fprintf(w, ", forecast %s", p.GetForecast()[:10])
					}
					fmt.Fprintln(w)
				} else {
					fmt.Fprintln(w, "No goal: set one with `barnraise goal set`.")
				}
				if st.Focus != nil {
					fmt.Fprintf(w, "Focus: %s — %d of %d items\n", st.Focus.Name, st.FocusDone, st.FocusAll)
				}
				var keys []string
				for k := range st.Counts {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				var parts []string
				for _, k := range keys {
					parts = append(parts, fmt.Sprintf("%s %d", k, st.Counts[k]))
				}
				fmt.Fprintf(w, "Items: %s · open proposals %d\n", strings.Join(parts, ", "), st.OpenProposals)
				if len(st.WaitingOwner) > 0 {
					fmt.Fprintln(w, "\nWaiting for you:")
					for _, q := range st.WaitingOwner {
						fmt.Fprintf(w, "  %s %s %s\n", q.Id, q.ItemId, q.Decision)
					}
				}
				if len(st.Agents) > 0 {
					fmt.Fprintln(w, "\nAgents:")
					for _, a := range st.Agents {
						fmt.Fprintf(w, "  %-28s %-10s %-8s %-14s %s %s\n", a.Agent, a.State, a.ItemId, a.Step, a.Model, when(a.At))
					}
				}
				if len(st.Accounts) > 0 {
					fmt.Fprintln(w, "\nAccounts:")
					for _, a := range st.Accounts {
						fmt.Fprintf(w, "  %-12s %-8s 5h %-5s week %-5s %s\n", a.Id, a.Status, pct(a.Usage.GetFiveHourPct()), pct(a.Usage.GetWeeklyPct()), resetText(a.Usage.GetWeeklyResets()))
					}
				}
				if len(st.Machines) > 0 {
					fmt.Fprintln(w, "\nMachines:")
					for _, m := range st.Machines {
						fmt.Fprintf(w, "  %-6s %-20s busy %d/%d  %s\n", m.Health, m.Name, m.Busy, m.Slots, when(m.LastSeen))
					}
				}
				for _, a := range st.Alerts {
					fmt.Fprintln(w, "! "+a)
				}
			})
			return nil
		}}
}

func resetText(ts string) string {
	if ts == "" || strings.HasPrefix(ts, "0001") {
		return ""
	}
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return ""
	}
	return "resets " + t.Local().Format("Mon 15:04")
}

// parseCriteria reads "WHEN …, THE SYSTEM SHALL … | check".
func parseCriteria(lines []string) []*pb.Criterion {
	var out []*pb.Criterion
	for i, l := range lines {
		text, check, _ := strings.Cut(l, "|")
		out = append(out, &pb.Criterion{Id: fmt.Sprintf("C%d", i+1), Text: strings.TrimSpace(text), Check: strings.TrimSpace(check)})
	}
	return out
}

func (e *env) itemCmd() *cobra.Command {
	c := &cobra.Command{Use: "item", Short: "Create, list, show and change items"}
	var title, body, kind, prio, area, goal, size, class string
	var criteria, preserve, deps, files, symbols []string
	var risky bool
	add := &cobra.Command{Use: "add [title]", Short: "Create an item (criteria in EARS: \"WHEN x, THE SYSTEM SHALL y | check\")", Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				title = args[0]
			}
			ctx, cancel := e.ctx()
			defer cancel()
			r := &pb.CreateItemRequest{Project: e.proj(ctx), Title: title, Body: body, Kind: kind, Priority: prio, Area: area, GoalId: goal, Size: size, Class: class,
				Criteria: parseCriteria(criteria), Preserve: preserve, Deps: deps, Risky: risky, Anchors: &pb.Anchors{Files: files, Symbols: symbols}}
			res, err := e.client().CreateItem(ctx, req(r))
			if err != nil {
				return err
			}
			e.print(res.Msg, func(w io.Writer) {
				if res.Msg.Item != nil {
					fmt.Fprintf(w, "Created %s: %s (%s)\n", res.Msg.Item.Id, res.Msg.Item.Title, res.Msg.Item.Status)
					if res.Msg.Item.Status == "draft" {
						fmt.Fprintln(w, "It is a draft: it needs criteria with checks and no open [NEEDS CLARIFICATION] before it can start.")
					}
				} else if res.Msg.Proposal != nil {
					fmt.Fprintf(w, "Recorded as proposal %s: %s\n", res.Msg.Proposal.Id, res.Msg.Note)
				}
			})
			return nil
		}}
	f := add.Flags()
	f.StringVar(&title, "title", "", "title")
	f.StringVar(&body, "body", "", "description (may contain [NEEDS CLARIFICATION: …])")
	f.StringVar(&kind, "kind", "feature", "item kind: feature, bug, improvement… (picks the flow)")
	f.StringVar(&prio, "priority", "P2", "P0 to P3")
	f.StringVar(&area, "area", "", "area")
	f.StringVar(&goal, "goal", "", "goal id (default: the active goal for features)")
	f.StringVar(&size, "size", "", "estimate, e.g. 4h")
	f.StringVar(&class, "class", "", "model class: simple, normal, complex, native")
	f.StringArrayVarP(&criteria, "criterion", "c", nil, "criterion \"WHEN …, THE SYSTEM SHALL … | check\" (repeat)")
	f.StringArrayVar(&preserve, "preserve", nil, "bugs: what must not change (repeat)")
	f.StringSliceVar(&deps, "deps", nil, "items this one depends on")
	f.StringSliceVar(&files, "files", nil, "anchor files")
	f.StringSliceVar(&symbols, "symbols", nil, "anchor symbols")
	f.BoolVar(&risky, "risky", false, "risky item")

	var lf struct {
		status, area, lane, kind string
		all                      bool
	}
	list := &cobra.Command{Use: "list", Aliases: []string{"ls"}, Short: "List items by priority", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := e.ctx()
			defer cancel()
			res, err := e.client().ListItems(ctx, req(&pb.ListItemsRequest{Project: e.proj(ctx), Status: lf.status, Area: lf.area, Lane: lf.lane, Kind: lf.kind, All: lf.all}))
			if err != nil {
				return err
			}
			e.print(res.Msg, func(w io.Writer) {
				if len(res.Msg.Items) == 0 {
					fmt.Fprintln(w, "No items.")
				}
				for _, it := range res.Msg.Items {
					fmt.Fprintln(w, itemLine(it))
				}
			})
			return nil
		}}
	list.Flags().StringVar(&lf.status, "status", "", "filter by status")
	list.Flags().StringVar(&lf.area, "area", "", "filter by area")
	list.Flags().StringVar(&lf.lane, "lane", "", "goal, bugs, improvements, self_improvement")
	list.Flags().StringVar(&lf.kind, "kind", "", "filter by kind")
	list.Flags().BoolVarP(&lf.all, "all", "a", false, "include integrated and closed items")

	show := &cobra.Command{Use: "show <id>", Short: "Show one item", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := e.ctx()
			defer cancel()
			res, err := e.client().GetItem(ctx, req(&pb.ItemRef{Project: e.proj(ctx), Id: args[0]}))
			if err != nil {
				return err
			}
			it := res.Msg.Item
			e.print(res.Msg, func(w io.Writer) {
				fmt.Fprintf(w, "%s %s\n%s · %s · P%d · lane %s · area %s · step %s\n", it.Id, it.Title, it.Kind, it.Status, it.Priority, it.Lane, it.Area, it.Step)
				if it.Body != "" {
					fmt.Fprintln(w, "\n"+it.Body)
				}
				fmt.Fprintln(w, "\nCriteria:")
				for _, c := range it.Criteria {
					m := " "
					if c.Met {
						m = "x"
					}
					fmt.Fprintf(w, "  [%s] %s %s (check: %s) %s\n", m, c.Id, c.Text, c.Check, c.Evidence)
				}
				if len(it.Preserve) > 0 {
					fmt.Fprintln(w, "What must not change:\n  - "+strings.Join(it.Preserve, "\n  - "))
				}
				if len(it.Rework) > 0 {
					fmt.Fprintf(w, "Rework rounds: %d (last from %s: %s)\n", len(it.Rework), it.Rework[len(it.Rework)-1].From, it.Rework[len(it.Rework)-1].Reason)
				}
				if it.Branch != "" {
					fmt.Fprintln(w, "Branch: "+it.Branch)
				}
				if it.Pr != "" {
					fmt.Fprintln(w, "PR: "+it.Pr)
				}
				if it.TrackerRef != "" {
					fmt.Fprintf(w, "Item %s appears as %s in the tracker\n", it.Id, it.TrackerRef)
				}
			})
			return nil
		}}

	var patchJSON, reason string
	var setCriteria []string
	edit := &cobra.Command{Use: "edit <id>", Short: "Change an item (analysts and the owner rewrite criteria)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			m := map[string]any{}
			if patchJSON != "" {
				if err := json.Unmarshal([]byte(patchJSON), &m); err != nil {
					return fmt.Errorf("--patch must be JSON: %w", err)
				}
			}
			for _, k := range []string{"title", "body", "priority", "area", "size", "class", "kind", "branch"} {
				if cmd.Flags().Changed(k) {
					v, _ := cmd.Flags().GetString(k)
					m[k] = v
				}
			}
			if len(setCriteria) > 0 {
				var cs []map[string]any
				for _, c := range parseCriteria(setCriteria) {
					cs = append(cs, map[string]any{"id": c.Id, "text": c.Text, "check": c.Check})
				}
				m["criteria"] = cs
			}
			if reason != "" {
				m["reason"] = reason
			}
			if cmd.Flags().Changed("clear-clarifications") {
				m["clarifications"] = []any{}
			}
			st, err := structpb.NewStruct(m)
			if err != nil {
				return err
			}
			ctx, cancel := e.ctx()
			defer cancel()
			res, err := e.client().UpdateItem(ctx, req(&pb.UpdateItemRequest{Project: e.proj(ctx), Id: args[0], Patch: st}))
			if err != nil {
				return err
			}
			e.print(res.Msg, func(w io.Writer) { fmt.Fprintln(w, "Updated "+itemLine(res.Msg.Item)) })
			return nil
		}}
	for _, k := range []string{"title", "body", "priority", "area", "size", "class", "kind"} {
		edit.Flags().String(k, "", k)
	}
	edit.Flags().String("branch", "", "continue on a branch that already exists (work started before Barnraise); only before the item starts")
	edit.Flags().StringArrayVarP(&setCriteria, "criterion", "c", nil, "replace criteria (repeat)")
	edit.Flags().StringVar(&patchJSON, "patch", "", "JSON patch of item fields")
	edit.Flags().StringVar(&reason, "reason", "", "why")
	edit.Flags().Bool("clear-clarifications", false, "mark open clarifications answered")

	var by, note string
	closeC := &cobra.Command{Use: "close <id> <obsolete|duplicate|done_by|split|abandoned>", Short: "Close an item with the reason", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := e.ctx()
			defer cancel()
			res, err := e.client().CloseItem(ctx, req(&pb.CloseItemRequest{Project: e.proj(ctx), Id: args[0], Reason: args[1], By: by, Note: note}))
			if err != nil {
				return err
			}
			e.print(res.Msg, func(w io.Writer) { fmt.Fprintln(w, "Closed "+itemLine(res.Msg.Item)) })
			return nil
		}}
	closeC.Flags().StringVar(&by, "by", "", "the item that did it or duplicates it")
	closeC.Flags().StringVar(&note, "note", "", "note")

	timeline := &cobra.Command{Use: "timeline <id>", Short: "Every event of an item in order", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := e.ctx()
			defer cancel()
			res, err := e.client().Timeline(ctx, req(&pb.ItemRef{Project: e.proj(ctx), Id: args[0]}))
			if err != nil {
				return err
			}
			e.print(res.Msg, func(w io.Writer) {
				for _, t := range res.Msg.Entries {
					ts, _ := time.Parse(time.RFC3339Nano, t.Time)
					fmt.Fprintf(w, "%s %-24s %-16s %s\n", ts.Local().Format("01-02 15:04:05"), t.Type, t.Actor, t.Summary)
				}
			})
			return nil
		}}
	estimate := &cobra.Command{Use: "estimate <id>", Short: "Forecast time and account share before starting", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := e.ctx()
			defer cancel()
			res, err := e.client().Estimate(ctx, req(&pb.ItemRef{Project: e.proj(ctx), Id: args[0]}))
			if err != nil {
				return err
			}
			m := res.Msg
			e.print(m, func(w io.Writer) {
				fmt.Fprintf(w, "%s: about %.1f hours", m.ItemId, m.Hours)
				if m.Tokens > 0 {
					fmt.Fprintf(w, ", %d tokens", m.Tokens)
				}
				if m.WeekPct > 0 {
					fmt.Fprintf(w, ", %.1f%% of the week of %s", m.WeekPct, m.Account)
				}
				fmt.Fprintf(w, " (based on %d items)\n", m.BasedOnItems)
			})
			return nil
		}}
	var why string
	redo := &cobra.Command{Use: "redo <id> <step>", Short: "Redo an item from any step of its flow", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := e.ctx()
			defer cancel()
			res, err := e.client().Redo(ctx, req(&pb.RedoRequest{Project: e.proj(ctx), Id: args[0], Step: args[1], Why: why}))
			if err != nil {
				return err
			}
			e.print(res.Msg, func(w io.Writer) { fmt.Fprintln(w, "Redoing "+itemLine(res.Msg.Item)) })
			return nil
		}}
	redo.Flags().StringVar(&why, "why", "", "reason")
	c.AddCommand(add, list, show, edit, closeC, timeline, estimate, redo)
	return c
}

func (e *env) queueCmd() *cobra.Command {
	c := &cobra.Command{Use: "queue", Short: "The merge queue", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := e.ctx()
			defer cancel()
			res, err := e.client().Queue(ctx, req(&pb.ProjectRef{Project: e.proj(ctx)}))
			if err != nil {
				return err
			}
			e.print(res.Msg, func(w io.Writer) {
				if len(res.Msg.Items) == 0 {
					fmt.Fprintln(w, "The merge queue is empty.")
				}
				for _, it := range res.Msg.Items {
					fmt.Fprintln(w, itemLine(it))
				}
			})
			return nil
		}}
	c.AddCommand(&cobra.Command{Use: "run", Short: "Integrate queued items held by this machine now", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := e.ctx()
			defer cancel()
			res, err := e.client().ProcessQueue(ctx, req(&pb.ProjectRef{Project: e.proj(ctx)}))
			if err != nil {
				return err
			}
			e.print(res.Msg, func(w io.Writer) { fmt.Fprintf(w, "%d items still in the queue.\n", len(res.Msg.Items)) })
			return nil
		}})
	return c
}

func (e *env) proposalsCmd() *cobra.Command {
	var all bool
	c := &cobra.Command{Use: "proposals", Short: "Proposals grouped by area", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := e.ctx()
			defer cancel()
			res, err := e.client().ListProposals(ctx, req(&pb.ListProposalsRequest{Project: e.proj(ctx), All: all}))
			if err != nil {
				return err
			}
			e.print(res.Msg, func(w io.Writer) {
				if len(res.Msg.Groups) == 0 {
					fmt.Fprintln(w, "No open proposals.")
				}
				for _, g := range res.Msg.Groups {
					fmt.Fprintf(w, "A-%s (%d)\n", g.Area, len(g.Proposals))
					for _, p := range g.Proposals {
						fmt.Fprintf(w, "  %-6s %-6s %s — %s (by %s, %d comments)\n", p.Id, p.Kind, p.Title, p.Impact, p.By, len(p.Comments))
					}
				}
			})
			return nil
		}}
	c.Flags().BoolVar(&all, "all", false, "include decided proposals")
	decide := func(use, decision, short string) *cobra.Command {
		var reason string
		cc := &cobra.Command{Use: use + " <P-id>", Short: short, Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				ctx, cancel := e.ctx()
				defer cancel()
				res, err := e.client().DecideProposal(ctx, req(&pb.DecideProposalRequest{Project: e.proj(ctx), Id: args[0], Decision: decision, Reason: reason}))
				if err != nil {
					return err
				}
				e.print(res.Msg, func(w io.Writer) {
					p := res.Msg.Proposal
					fmt.Fprintf(w, "%s is %s", p.Id, p.Status)
					if p.ItemId != "" {
						fmt.Fprintf(w, "; it became %s", p.ItemId)
					}
					fmt.Fprintln(w)
				})
				return nil
			}}
		cc.Flags().StringVar(&reason, "reason", "", "reason")
		return cc
	}
	c.AddCommand(decide("approve", "approve", "Approve the proposal (it becomes an item)"), decide("reject", "reject", "Reject the proposal"), decide("later", "later", "Decide about the proposal later"))
	return c
}

func (e *env) proposeCmd() *cobra.Command {
	var p pb.Proposal
	var prio int32
	c := &cobra.Command{Use: "propose", Short: "Propose an item in the fixed shape (limit per agent per day)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p.Priority = prio
			ctx, cancel := e.ctx()
			defer cancel()
			res, err := e.client().Propose(ctx, req(&pb.ProposeRequest{Project: e.proj(ctx), Proposal: &p}))
			if err != nil {
				return err
			}
			e.print(res.Msg, func(w io.Writer) {
				if res.Msg.Duplicate != "" {
					fmt.Fprintf(w, "Similar to %s: added as a comment there.\n", res.Msg.Duplicate)
					return
				}
				fmt.Fprintf(w, "Proposal %s recorded.\n", res.Msg.Proposal.Id)
			})
			return nil
		}}
	f := c.Flags()
	f.StringVar(&p.Title, "title", "", "title")
	f.StringVar(&p.Problem, "problem", "", "the problem")
	f.StringVar(&p.Evidence, "evidence", "", "evidence: print, log, line")
	f.StringVar(&p.Impact, "impact", "", "impact")
	f.StringVar(&p.Size, "size", "", "size")
	f.StringVar(&p.Area, "area", "", "area")
	f.StringVar(&p.GoalLink, "goal-link", "", "relation to the goal")
	f.StringVar(&p.Kind, "kind", "improvement", "feature, improvement, bug, self_improvement")
	f.Int32Var(&prio, "priority", 2, "0 to 3")
	return c
}

func (e *env) tweakCmd() *cobra.Command {
	c := &cobra.Command{Use: "tweak", Short: "Small fixes that ride with the next item of their area"}
	c.AddCommand(&cobra.Command{Use: "add <area> <text>", Short: "Add a tweak", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := e.ctx()
			defer cancel()
			res, err := e.client().AddTweak(ctx, req(&pb.AddTweakRequest{Project: e.proj(ctx), Area: args[0], Text: args[1]}))
			if err != nil {
				return err
			}
			e.print(res.Msg, func(w io.Writer) {
				fmt.Fprintf(w, "Tweak added to area %s.\n", res.Msg.Tweak.Area)
				if res.Msg.Item != nil {
					fmt.Fprintf(w, "Enough tweaks accumulated: they became %s.\n", res.Msg.Item.Id)
				}
			})
			return nil
		}})
	return c
}

func (e *env) agentsCmd() *cobra.Command {
	return &cobra.Command{Use: "agents", Short: "Agents with role, account, model and current step", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := e.ctx()
			defer cancel()
			res, err := e.client().Status(ctx, req(&pb.ProjectRef{Project: e.proj(ctx)}))
			if err != nil {
				return err
			}
			e.print(&pb.StatusResponse{Agents: res.Msg.Agents}, func(w io.Writer) {
				if len(res.Msg.Agents) == 0 {
					fmt.Fprintln(w, "No agents ran recently.")
				}
				for _, a := range res.Msg.Agents {
					fmt.Fprintf(w, "%-28s %-11s %-10s %-8s %-14s %-24s %s\n", a.Agent, a.Role, a.State, a.ItemId, a.Step, a.Model, when(a.At))
				}
			})
			return nil
		}}
}

func (e *env) metricsCmd() *cobra.Command {
	var window string
	c := &cobra.Command{Use: "metrics", Short: "Time, tokens and rework by step, item and role", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := e.ctx()
			defer cancel()
			res, err := e.client().Metrics(ctx, req(&pb.MetricsRequest{Project: e.proj(ctx), Window: window}))
			if err != nil {
				return err
			}
			m := res.Msg
			e.print(m, func(w io.Writer) {
				fmt.Fprintf(w, "Window %s: %.1f items/day, rework %.1f rounds/item, cache hit %.0f%%\n", m.Window, m.IntegratedPerDay, m.ReworkAvg, m.CacheHitRate*100)
				fmt.Fprintf(w, "Proposals %d made, %d approved · violations %d · flaky in quarantine %d · longest wait for the owner %s\n", m.ProposalsMade, m.ProposalsApproved, m.Violations, m.FlakyQuarantined, m.WaitingOwnerMax)
				for k, v := range m.TokensByModel {
					fmt.Fprintf(w, "  tokens %-28s %d\n", k, v)
				}
				for k, v := range m.ReworkCauses {
					fmt.Fprintf(w, "  rework from %-20s %d\n", k, v)
				}
				for _, a := range m.Alerts {
					fmt.Fprintln(w, "! "+a)
				}
			})
			return nil
		}}
	c.Flags().StringVar(&window, "window", "7d", "window: 24h, 7d, 30d")
	return c
}

func (e *env) digestCmd() *cobra.Command {
	return &cobra.Command{Use: "digest", Short: "The daily summary", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := e.ctx()
			defer cancel()
			res, err := e.client().Digest(ctx, req(&pb.ProjectRef{Project: e.proj(ctx)}))
			if err != nil {
				return err
			}
			e.print(res.Msg, func(w io.Writer) { fmt.Fprint(w, res.Msg.Text) })
			return nil
		}}
}

func (e *env) eventsCmd() *cobra.Command {
	var types string
	var follow bool
	var after int64
	c := &cobra.Command{Use: "events", Short: "Show or follow events (CloudEvents)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := e.ctx()
			defer cancel()
			cl := e.client()
			if !follow {
				res, err := cl.ListEvents(ctx, req(&pb.EventsRequest{Project: e.proj(ctx), Types: splitList(types), AfterSeq: after, Limit: 200}))
				if err != nil {
					return err
				}
				e.print(res.Msg, func(w io.Writer) {
					for _, ev := range res.Msg.Events {
						fmt.Fprintf(w, "%6d %s %-26s %-8s %s\n", ev.Seq, ev.Time[:19], ev.Type, ev.Subject, ev.Actor)
					}
				})
				return nil
			}
			u := cl.BaseURL + "/v1/events?project=" + e.proj(ctx) + "&types=" + types
			if after > 0 {
				u += fmt.Sprintf("&after_seq=%d", after)
			}
			r, err := http.NewRequest("GET", u, nil)
			if err != nil {
				return err
			}
			r.Header.Set("Authorization", "Bearer "+cl.Token)
			hc := *cl.HTTP
			hc.Timeout = 0
			resp, err := hc.Do(r)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			if resp.StatusCode != 200 {
				b, _ := io.ReadAll(resp.Body)
				return fmt.Errorf("events: %s", strings.TrimSpace(string(b)))
			}
			sc := bufio.NewScanner(resp.Body)
			sc.Buffer(make([]byte, 1<<20), 8<<20)
			for sc.Scan() {
				line := sc.Text()
				if d, ok := strings.CutPrefix(line, "data: "); ok {
					fmt.Fprintln(e.out, d)
				}
			}
			return sc.Err()
		}}
	c.Flags().StringVar(&types, "types", "", "comma-separated types or prefixes (item.*)")
	c.Flags().BoolVarP(&follow, "follow", "f", false, "follow live")
	c.Flags().Int64Var(&after, "after", 0, "after this sequence number")
	return c
}
