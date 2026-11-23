package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	pb "github.com/leandrodaf/barnraise/gen/barnraise/v1"
	"github.com/leandrodaf/barnraise/internal/adapters/in/hooks"
	"github.com/leandrodaf/barnraise/internal/adapters/in/mcp"
	"github.com/leandrodaf/barnraise/internal/adapters/out/outfilter"
	"github.com/leandrodaf/barnraise/internal/paths"
)

func (e *env) nextCmd() *cobra.Command {
	return &cobra.Command{Use: "next", Short: "Take the next item (atomic reservation) or show the current one", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := e.ctx()
			defer cancel()
			res, err := e.client().NextItem(ctx, req(&pb.NextItemRequest{Project: e.proj(ctx)}))
			if err != nil {
				return err
			}
			e.print(res.Msg, func(w io.Writer) {
				if res.Msg.None {
					fmt.Fprintln(w, res.Msg.Note)
					return
				}
				step := ""
				if res.Msg.Step != nil {
					step = res.Msg.Step.Fields["name"].GetStringValue()
				}
				fmt.Fprintf(w, "%s at step %s\n", itemLine(res.Msg.Item), step)
			})
			return nil
		}}
}

func (e *env) deliverCmd() *cobra.Command {
	var status, summary, reason, item, step string
	var crit, files, concerns []string
	c := &cobra.Command{Use: "deliver", Short: "End a step with a delivery: DONE, DONE_WITH_CONCERNS, NEEDS_CONTEXT or BLOCKED", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d := &pb.Delivery{ItemId: def(item, os.Getenv("BARNRAISE_ITEM")), Step: def(step, os.Getenv("BARNRAISE_STEP")), Status: strings.ToUpper(status),
				Summary: summary, Files: files, Concerns: concerns, Reason: reason}
			for _, cm := range crit {
				id, ev, ok := strings.Cut(cm, "=")
				met := true
				if before, ok0 := strings.CutSuffix(id, "!"); ok0 {
					id, met = before, false
				}
				if !ok && met {
					return fmt.Errorf("criterion %q: use C1=\"evidence\" (or C1!=\"why not\" for not met)", cm)
				}
				d.Criteria = append(d.Criteria, &pb.CriterionMark{Id: strings.TrimSpace(id), Met: met, Evidence: strings.TrimSpace(ev)})
			}
			ctx, cancel := e.ctx()
			defer cancel()
			res, err := e.client().Deliver(ctx, req(&pb.DeliverRequest{Project: e.proj(ctx), Delivery: d}))
			if err != nil {
				return err
			}
			e.print(res.Msg, func(w io.Writer) {
				fmt.Fprintf(w, "Delivered. %s is now %s at step %s. End your session now.\n", res.Msg.Item.Id, res.Msg.Item.Status, res.Msg.Item.Step)
			})
			return nil
		}}
	f := c.Flags()
	f.StringVar(&status, "status", "DONE", "DONE, DONE_WITH_CONCERNS, NEEDS_CONTEXT, BLOCKED")
	f.StringVar(&summary, "summary", "", "what was done (required)")
	f.StringArrayVar(&crit, "criteria", nil, `mark a criterion: C1="evidence" (repeat)`)
	f.StringSliceVar(&files, "files", nil, "files touched")
	f.StringArrayVar(&concerns, "concern", nil, "a concern (repeat)")
	f.StringVar(&reason, "reason", "", "for NEEDS_CONTEXT and BLOCKED: the exact question or blocker")
	f.StringVar(&item, "item", "", "item (default: the session's)")
	f.StringVar(&step, "step", "", "step (default: the session's)")
	return c
}

func (e *env) reviewCmd() *cobra.Command {
	var cv, kv, decision, summary, item, step string
	var findings []string
	c := &cobra.Command{Use: "review", Short: "Submit a review with two verdicts and a decision", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			r := &pb.Review{ItemId: def(item, os.Getenv("BARNRAISE_ITEM")), Step: def(step, os.Getenv("BARNRAISE_STEP")), CriteriaVerdict: strings.ToLower(cv),
				CodeVerdict: strings.ToLower(kv), Decision: strings.ToUpper(decision), Summary: summary}
			for _, fd := range findings {
				parts := strings.SplitN(fd, "|", 3)
				f := &pb.Finding{}
				switch len(parts) {
				case 3:
					loc, sev, text := parts[0], parts[1], parts[2]
					file, line, _ := strings.Cut(loc, ":")
					n, _ := strconv.Atoi(line)
					f.File, f.Line, f.Severity, f.Text = strings.TrimSpace(file), int32(n), strings.TrimSpace(sev), strings.TrimSpace(text)
				default:
					f.Text = fd
				}
				r.Findings = append(r.Findings, f)
			}
			ctx, cancel := e.ctx()
			defer cancel()
			res, err := e.client().SubmitReview(ctx, req(&pb.ReviewRequest{Project: e.proj(ctx), Review: r}))
			if err != nil {
				return err
			}
			e.print(res.Msg, func(w io.Writer) {
				fmt.Fprintf(w, "Review recorded. %s is now %s at step %s. End your session now.\n", res.Msg.Item.Id, res.Msg.Item.Status, res.Msg.Item.Step)
			})
			return nil
		}}
	f := c.Flags()
	f.StringVar(&cv, "criteria-verdict", "", "pass or fail: does it meet the criteria?")
	f.StringVar(&kv, "code-verdict", "", "pass or fail: is the code good?")
	f.StringVar(&decision, "decision", "", "APPROVE, REWORK or REJECT")
	f.StringVar(&summary, "summary", "", "summary")
	f.StringArrayVar(&findings, "finding", nil, `"file:line|severity|text" (repeat)`)
	f.StringVar(&item, "item", "", "item (default: the session's)")
	f.StringVar(&step, "step", "", "step (default: the session's)")
	return c
}

func (e *env) askCmd() *cobra.Command {
	var context, decision, ifNo, item string
	var options []string
	var recommended int
	c := &cobra.Command{Use: "ask", Short: "Ask the owner: context, decision, 2-4 options with one recommended", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := &pb.Question{ItemId: def(item, os.Getenv("BARNRAISE_ITEM")), Context: context, Decision: decision, IfNoAnswer: ifNo}
			for i, o := range options {
				q.Options = append(q.Options, &pb.Option{Label: o, Recommended: i+1 == recommended})
			}
			ctx, cancel := e.ctx()
			defer cancel()
			res, err := e.client().AskOwner(ctx, req(&pb.AskOwnerRequest{Project: e.proj(ctx), Question: q}))
			if err != nil {
				return err
			}
			e.print(res.Msg, func(w io.Writer) {
				fmt.Fprintf(w, "Asked as %s. The item waits for the owner; take other work.\n", res.Msg.Question.Id)
			})
			return nil
		}}
	f := c.Flags()
	f.StringVar(&context, "context", "", "one sentence of context")
	f.StringVar(&decision, "decision", "", "the decision asked")
	f.StringArrayVar(&options, "option", nil, "an option (2 to 4)")
	f.IntVar(&recommended, "recommended", 1, "number of the recommended option")
	f.StringVar(&ifNo, "if-no-answer", "", "what happens if nobody answers")
	f.StringVar(&item, "item", "", "item")
	return c
}

func (e *env) messageCmd() *cobra.Command {
	var item string
	c := &cobra.Command{Use: "message <to> <text>", Short: "Send a short message tied to an item (to: agent, role:<role>, item:<id>)", Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := e.ctx()
			defer cancel()
			res, err := e.client().SendMessage(ctx, req(&pb.SendMessageRequest{Project: e.proj(ctx), Message: &pb.Message{To: args[0], Text: strings.Join(args[1:], " "), ItemId: def(item, os.Getenv("BARNRAISE_ITEM"))}}))
			if err != nil {
				return err
			}
			e.print(res.Msg, func(w io.Writer) { fmt.Fprintln(w, "Sent.") })
			return nil
		}}
	c.Flags().StringVar(&item, "item", "", "item")
	return c
}

func (e *env) inboxCmd() *cobra.Command {
	var agent, role, item string
	var mark bool
	c := &cobra.Command{Use: "inbox", Short: "Messages waiting for an agent", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := e.ctx()
			defer cancel()
			res, err := e.client().Inbox(ctx, req(&pb.InboxRequest{Project: e.proj(ctx), Agent: def(agent, os.Getenv("BARNRAISE_AGENT")), Role: def(role, os.Getenv("BARNRAISE_ROLE")), ItemId: def(item, os.Getenv("BARNRAISE_ITEM")), Mark: mark}))
			if err != nil {
				return err
			}
			e.print(res.Msg, func(w io.Writer) {
				for _, m := range res.Msg.Messages {
					fmt.Fprintf(w, "%s from %s: %s\n", m.ItemId, m.From, m.Text)
				}
			})
			return nil
		}}
	c.Flags().StringVar(&agent, "agent", "", "agent")
	c.Flags().StringVar(&role, "role", "", "role")
	c.Flags().StringVar(&item, "item", "", "item")
	c.Flags().BoolVar(&mark, "mark", false, "mark as delivered")
	return c
}

func (e *env) journalCmd() *cobra.Command {
	return &cobra.Command{Use: "journal <text>", Short: "Write the end-of-step journal entry", Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := e.ctx()
			defer cancel()
			_, err := e.client().Journal(ctx, req(&pb.JournalRequest{Project: e.proj(ctx), Text: strings.Join(args, " ")}))
			return err
		}}
}

// ---------- hooks ----------

func (e *env) hookCmd() *cobra.Command {
	c := &cobra.Command{Use: "hook", Short: "Provider hooks installed in agent sessions"}
	c.AddCommand(&cobra.Command{Use: "pre-tool", Short: "PreToolUse: block what is never the agent's, locally, without network", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			in, err := hooks.ReadInput(e.in)
			if err != nil {
				return nil // never break the session on a bad payload
			}
			pol, err := hooks.LoadPolicy(os.Getenv("BARNRAISE_POLICY"))
			if err != nil {
				return nil // not a Barnraise session
			}
			bin, _ := os.Executable()
			res := hooks.PreTool(in, pol, hooks.ExistsOnMain(pol.Worktree, pol.MainBranch), bin)
			_ = hooks.Write(e.out, res.Output)
			if !res.Decision.Allow {
				// Tell the daemon, briefly; the decision is already out.
				ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
				defer cancel()
				excerpt := res.Call.Command
				if excerpt == "" {
					excerpt = strings.Join(res.Call.Paths, " ")
				}
				_, _ = e.client().ReportViolation(ctx, req(&pb.PolicyViolationRequest{Project: os.Getenv("BARNRAISE_PROJECT"), ItemId: os.Getenv("BARNRAISE_ITEM"),
					Agent: os.Getenv("BARNRAISE_AGENT"), Tool: in.ToolName, Rule: res.Decision.Rule, Reason: res.Decision.Reason, Excerpt: excerpt}))
			}
			return nil
		}})
	c.AddCommand(&cobra.Command{Use: "prime", Short: "SessionStart/PreCompact: reinject the item, plan.md, journal and inbox", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			in, _ := hooks.ReadInput(e.in)
			item := os.Getenv("BARNRAISE_ITEM")
			if item == "" || in.HookEventName == "PreCompact" {
				if in.HookEventName == "PreCompact" {
					fmt.Fprintln(e.errOut, "Barnraise: update plan.md now; it is the item's memory after compaction.")
				}
				return nil
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cl := e.client()
			var b strings.Builder
			if in.Source == "compact" || in.Source == "clear" || in.Source == "resume" {
				res, err := cl.Context(ctx, req(&pb.ContextRequest{Project: os.Getenv("BARNRAISE_PROJECT"), Id: item}))
				if err == nil {
					b.WriteString("# Barnraise: your item after a context reset\n" + res.Msg.Text)
				}
			}
			mb, err := cl.Inbox(ctx, req(&pb.InboxRequest{Project: os.Getenv("BARNRAISE_PROJECT"), Mark: true}))
			if err == nil && len(mb.Msg.Messages) > 0 {
				b.WriteString("\n# Messages from Barnraise\n")
				for _, m := range mb.Msg.Messages {
					fmt.Fprintf(&b, "- %s: %s\n", m.From, m.Text)
				}
			}
			return hooks.Write(e.out, hooks.AdditionalContext("SessionStart", b.String()))
		}})
	mail := func(event string) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, _ []string) error {
			_, _ = hooks.ReadInput(e.in)
			if os.Getenv("BARNRAISE_TOKEN") == "" {
				return nil
			}
			if event == "PostToolUse" && !throttle("mail", 20*time.Second) {
				return nil
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			mb, err := e.client().Inbox(ctx, req(&pb.InboxRequest{Project: os.Getenv("BARNRAISE_PROJECT"), Mark: true}))
			if err != nil || len(mb.Msg.Messages) == 0 {
				return nil
			}
			var b strings.Builder
			b.WriteString("Messages from Barnraise (delivered between tool calls):\n")
			for _, m := range mb.Msg.Messages {
				fmt.Fprintf(&b, "- %s: %s\n", m.From, m.Text)
			}
			return hooks.Write(e.out, hooks.AdditionalContext(event, b.String()))
		}
	}
	c.AddCommand(&cobra.Command{Use: "mail", Short: "UserPromptSubmit: deliver the inbox (replaces typing into terminals)", Args: cobra.NoArgs, RunE: mail("UserPromptSubmit")})
	c.AddCommand(&cobra.Command{Use: "post-tool", Short: "PostToolUse: deliver urgent messages between tool calls", Args: cobra.NoArgs, RunE: mail("PostToolUse")})
	c.AddCommand(&cobra.Command{Use: "stop", Short: "Stop: keep the agent going until it delivers", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			in, _ := hooks.ReadInput(e.in)
			item, step := os.Getenv("BARNRAISE_ITEM"), os.Getenv("BARNRAISE_STEP")
			if in.StopHookActive || item == "" || step == "" {
				return nil
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			res, err := e.client().GetItem(ctx, req(&pb.ItemRef{Project: os.Getenv("BARNRAISE_PROJECT"), Id: item}))
			if err != nil {
				return nil
			}
			if res.Msg.Item.Step == step && res.Msg.Item.Status == "in_progress" {
				return hooks.Write(e.out, hooks.BlockStop("You have not ended step "+step+" of "+item+". Finish it and end with exactly one `deliver` (or `review` for review steps). If you cannot, deliver BLOCKED or NEEDS_CONTEXT with the reason."))
			}
			return nil
		}})
	return c
}

// throttle returns true at most once per interval for a key in this session.
func throttle(key string, every time.Duration) bool {
	dir := filepath.Join(os.Getenv("BARNRAISE_WORKTREE"), ".barnraise-session")
	if os.Getenv("BARNRAISE_WORKTREE") == "" {
		dir = os.TempDir()
	}
	f := filepath.Join(dir, "."+key+".stamp")
	if st, err := os.Stat(f); err == nil && time.Since(st.ModTime()) < every {
		return false
	}
	_ = os.WriteFile(f, nil, 0o600)
	return true
}

func (e *env) statuslineCmd() *cobra.Command {
	return &cobra.Command{Use: "statusline", Short: "Status line that records each account's real limits", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			data, _ := io.ReadAll(io.LimitReader(e.in, 1<<20))
			var s hooks.StatusInput
			_ = json.Unmarshal(data, &s)
			five, week, top, fr, wr, ok := s.Limits()
			acc := os.Getenv("BARNRAISE_ACCOUNT")
			if ok && acc != "" && throttle("usage", time.Minute) {
				u := &pb.Usage{FiveHourPct: five, WeeklyPct: week, WeeklyTopPct: top, ContextPct: s.ContextWindow.UsedPercentage, Source: "statusline",
					FiveHourResets: normTime(fr), WeeklyResets: normTime(wr)}
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				_, _ = e.client().ReportUsage(ctx, req(&pb.ReportUsageRequest{Project: os.Getenv("BARNRAISE_PROJECT"), Account: acc, Usage: u}))
			}
			line := "barnraise"
			if it := os.Getenv("BARNRAISE_ITEM"); it != "" {
				line += " · " + it + " " + os.Getenv("BARNRAISE_STEP")
			}
			if s.Model.DisplayName != "" {
				line += " · " + s.Model.DisplayName
			}
			if ok {
				line += fmt.Sprintf(" · 5h %.0f%% · week %.0f%%", five, week)
			}
			if s.ContextWindow.UsedPercentage > 0 {
				line += fmt.Sprintf(" · ctx %.0f%%", s.ContextWindow.UsedPercentage)
			}
			fmt.Fprintln(e.out, line)
			return nil
		}}
}

// normTime accepts RFC3339 or unix seconds.
func normTime(v string) string {
	if v == "" || v == "<nil>" {
		return ""
	}
	if f, err := strconv.ParseFloat(v, 64); err == nil {
		return time.Unix(int64(f), 0).UTC().Format(time.RFC3339)
	}
	if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	return ""
}

func (e *env) filterCmd() *cobra.Command {
	return &cobra.Command{Use: "filter -- <command…>", Short: "Run a noisy command and print only the errors or the tail, with the full log path", Args: cobra.MinimumNArgs(1),
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 && args[0] == "--" {
				args = args[1:]
			}
			if len(args) == 0 {
				return fmt.Errorf("usage: barnraise filter -- <command>")
			}
			logDir := filepath.Join(paths.DataDir(), "logs")
			if wt := os.Getenv("BARNRAISE_WORKTREE"); wt != "" {
				logDir = filepath.Join(wt, ".barnraise-session", "logs")
			}
			_ = os.MkdirAll(logDir, 0o700)
			if len(args) == 1 && strings.Contains(args[0], " ") {
				args = []string{"sh", "-c", args[0]}
			}
			summary, code, _, err := outfilter.Run(context.Background(), args, logDir)
			if err != nil {
				return err
			}
			fmt.Fprintln(e.out, summary)
			if code != 0 {
				return exitCode(code)
			}
			return nil
		}}
}

func (e *env) mcpCmd() *cobra.Command {
	return &cobra.Command{Use: "mcp", Short: "MCP server (stdio) with typed tools, permission by role", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return mcp.Serve(context.Background(), os.Stdin, os.Stdout, e.client(), mcp.Options{
				Project: def(e.project, os.Getenv("BARNRAISE_PROJECT")), Role: os.Getenv("BARNRAISE_ROLE"), Item: os.Getenv("BARNRAISE_ITEM"), Version: e.hooks.Version})
		}}
}
