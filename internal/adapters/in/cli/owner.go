package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/types/known/structpb"

	pb "github.com/leandrodaf/barnraise/gen/barnraise/v1"
)

// approveCmd: never approve without saying what (plan, delivery, proposal).
func (e *env) approveCmd(approve bool) *cobra.Command {
	use, short := "approve", "Approve the plan, the delivery or the proposal"
	if !approve {
		use, short = "reject", "Send the plan or the delivery back, or reject the proposal"
	}
	var note string
	var plan, delivery bool
	c := &cobra.Command{Use: use + " <I-id|P-id>", Short: short, Long: short + ".\nSay what: --plan or --delivery for items; proposals need nothing more.", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			what := ""
			switch {
			case strings.HasPrefix(strings.ToUpper(args[0]), "P-"):
				what = "proposal"
			case plan && delivery:
				return fmt.Errorf("choose one: --plan or --delivery")
			case plan:
				what = "plan"
			case delivery:
				what = "delivery"
			default:
				return fmt.Errorf("say what you %s: --plan or --delivery", use)
			}
			ctx, cancel := e.ctx()
			defer cancel()
			_, err := e.client().Approve(ctx, req(&pb.ApproveRequest{Project: e.proj(ctx), Target: args[0], What: what, Approve: approve, Note: note}))
			if err != nil {
				return err
			}
			verb := map[bool]string{true: "Approved", false: "Sent back"}[approve]
			if !approve && what == "proposal" {
				verb = "Rejected"
			}
			fmt.Fprintf(e.out, "%s the %s of %s.\n", verb, what, strings.ToUpper(args[0]))
			return nil
		}}
	c.Flags().BoolVar(&plan, "plan", false, "the plan")
	c.Flags().BoolVar(&delivery, "delivery", false, "the delivery")
	c.Flags().StringVar(&note, "note", "", "note for the agent")
	return c
}

func (e *env) answerCmd() *cobra.Command {
	var note string
	c := &cobra.Command{Use: "answer <Q-id> <option>", Short: "Answer an owner question (option label or number)", Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := e.ctx()
			defer cancel()
			res, err := e.client().Answer(ctx, req(&pb.AnswerRequest{Project: e.proj(ctx), Id: args[0], Answer: strings.Join(args[1:], " "), Note: note}))
			if err != nil {
				return err
			}
			e.print(res.Msg, func(w io.Writer) { fmt.Fprintf(w, "%s answered: %s\n", res.Msg.Question.Id, res.Msg.Question.Answer) })
			return nil
		}}
	c.Flags().StringVar(&note, "note", "", "context for the agent")
	return c
}

func (e *env) questionsCmd() *cobra.Command {
	return &cobra.Command{Use: "questions", Short: "Questions waiting for the owner", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := e.ctx()
			defer cancel()
			res, err := e.client().ListQuestions(ctx, req(&pb.ProjectRef{Project: e.proj(ctx)}))
			if err != nil {
				return err
			}
			e.print(res.Msg, func(w io.Writer) {
				if len(res.Msg.Questions) == 0 {
					fmt.Fprintln(w, "Nothing waits for you.")
				}
				for _, q := range res.Msg.Questions {
					fmt.Fprintf(w, "%s %s\n  %s\n  %s\n", q.Id, q.ItemId, q.Context, q.Decision)
					for i, o := range q.Options {
						rec := ""
						if o.Recommended {
							rec = " (recommended)"
						}
						fmt.Fprintf(w, "    %d. %s%s\n", i+1, o.Label, rec)
					}
					fmt.Fprintf(w, "  If nobody answers: %s\n", q.IfNoAnswer)
				}
			})
			return nil
		}}
}

func (e *env) goalCmd() *cobra.Command {
	c := &cobra.Command{Use: "goal", Short: "The long-term goal with a measurable end"}
	var doneWhen, inventory string
	set := &cobra.Command{Use: "set <name>", Short: "Set the goal; --inventory lists its finite work once", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := e.ctx()
			defer cancel()
			res, err := e.client().SetGoal(ctx, req(&pb.SetGoalRequest{Project: e.proj(ctx), Goal: &pb.Goal{Name: args[0], DoneWhen: doneWhen, Inventory: inventory}}))
			if err != nil {
				return err
			}
			e.print(res.Msg, func(w io.Writer) {
				fmt.Fprintf(w, "Goal %s set: %s. Done when: %s.\n", res.Msg.Goal.Id, res.Msg.Goal.Name, res.Msg.Goal.DoneWhen)
				if n := len(res.Msg.Inventory); n > 0 {
					fmt.Fprintf(w, "Inventory: %d items created (%s … %s).\n", n, res.Msg.Inventory[0].Id, res.Msg.Inventory[n-1].Id)
				}
			})
			return nil
		}}
	set.Flags().StringVar(&doneWhen, "done-when", "", "measurable end (required)")
	set.Flags().StringVar(&inventory, "inventory", "", `"screens <dir>", "files <path>" or "lines <file>"`)
	_ = set.MarkFlagRequired("done-when")
	c.AddCommand(set)
	return c
}

func (e *env) focusCmd() *cobra.Command {
	var until string
	var share int32
	var items []string
	var preview, clear bool
	c := &cobra.Command{Use: "focus [area]", Short: "Set a temporary focus; shows the effect first", Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := e.ctx()
			defer cancel()
			cl := e.client()
			if clear {
				if _, err := cl.ClearFocus(ctx, req(&pb.ProjectRef{Project: e.proj(ctx)})); err != nil {
					return err
				}
				fmt.Fprintln(e.out, "Focus cleared: capacity goes back to the goal.")
				return nil
			}
			f := &pb.Focus{Items: items, Share: share}
			if len(args) == 1 {
				f.Area = args[0]
				f.Name = args[0]
			}
			res, err := cl.SetFocus(ctx, req(&pb.FocusRequest{Project: e.proj(ctx), Focus: f, Until: until, Preview: preview}))
			if err != nil {
				return err
			}
			eff := res.Msg.Effect
			e.print(res.Msg, func(w io.Writer) {
				u := until
				if u == "" {
					u = "until done"
				}
				verb := "Focus set"
				if preview {
					verb = "Focus would be"
				}
				fmt.Fprintf(w, "%s: %q, %s. %d items in the focus. %d in progress elsewhere finish their current step and wait. %d%% of capacity goes to the focus.\n",
					verb, def(f.Name, strings.Join(items, ",")), u, eff.GetFocusItems(), len(eff.GetParkedElsewhere()), eff.GetShare())
			})
			return nil
		}}
	c.Flags().StringVar(&until, "until", "", "a date (2026-12-01), a duration (3d) or done")
	c.Flags().Int32Var(&share, "share", 80, "share of capacity")
	c.Flags().StringSliceVar(&items, "items", nil, "items in the focus")
	c.Flags().BoolVar(&preview, "preview", false, "only show the effect")
	c.Flags().BoolVar(&clear, "clear", false, "end the focus")
	return c
}

func def(v, d string) string {
	if strings.TrimSpace(v) == "" {
		return d
	}
	return v
}

// eachProject runs fn for the current project, or for every project of the
// daemon with all, and reports the ones done.
func (e *env) eachProject(ctx context.Context, all bool, fn func(project string) error) ([]string, error) {
	if !all {
		name := e.proj(ctx)
		return []string{name}, fn(name)
	}
	res, err := e.client().ListProjects(ctx, req(&pb.Empty{}))
	if err != nil {
		return nil, err
	}
	var done []string
	for _, name := range res.Msg.Projects {
		if err := fn(name); err != nil {
			return done, fmt.Errorf("%s: %w", name, err)
		}
		done = append(done, name)
	}
	return done, nil
}

func (e *env) pauseCmd() *cobra.Command {
	var all bool
	c := &cobra.Command{Use: "pause [why]", Short: "Emergency button: stop every agent and start nothing", Args: cobra.ArbitraryArgs,
		Long: "Pauses a project: its sessions stop on every machine and nothing starts until `barnraise resume`.\n" +
			"--all pauses every project of this daemon.\n\n" +
			"To keep one machine from working, whatever the project, use `barnraise standby on`.",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := e.ctx()
			defer cancel()
			done, err := e.eachProject(ctx, all, func(project string) error {
				_, err := e.client().Pause(ctx, req(&pb.PauseRequest{Project: project, Why: strings.Join(args, " ")}))
				return err
			})
			if all && len(done) > 0 {
				fmt.Fprintf(e.out, "Paused: %s.\n", strings.Join(done, ", "))
			}
			if err != nil {
				return err
			}
			fmt.Fprintln(e.out, "Paused. Running sessions were stopped; `barnraise resume` continues.")
			return nil
		}}
	c.Flags().BoolVar(&all, "all", false, "pause every project of this daemon")
	return c
}

func (e *env) resumeCmd() *cobra.Command {
	var all bool
	c := &cobra.Command{Use: "resume", Short: "Lift the pause", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := e.ctx()
			defer cancel()
			done, err := e.eachProject(ctx, all, func(project string) error {
				_, err := e.client().Resume(ctx, req(&pb.ProjectRef{Project: project}))
				return err
			})
			if all && len(done) > 0 {
				fmt.Fprintf(e.out, "Resumed: %s.\n", strings.Join(done, ", "))
			}
			if err != nil {
				return err
			}
			fmt.Fprintln(e.out, "Resumed.")
			if ps, err := e.client().ListProjects(ctx, req(&pb.Empty{})); err == nil && ps.Msg.Standby {
				fmt.Fprintln(e.out, "This machine is still in standby: nothing starts here until `barnraise standby off`.")
			}
			return nil
		}}
	c.Flags().BoolVar(&all, "all", false, "resume every project of this daemon")
	return c
}

func (e *env) takeoverCmd() *cobra.Command {
	var noEditor bool
	c := &cobra.Command{Use: "takeover <item>", Short: "Pause the agent and open the item's worktree in your editor", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := e.ctx()
			defer cancel()
			res, err := e.client().Takeover(ctx, req(&pb.ItemRef{Project: e.proj(ctx), Id: args[0]}))
			if err != nil {
				return err
			}
			fmt.Fprintf(e.out, "%s is yours. Worktree: %s (plan.md and criteria.json inside).\nWhen done: barnraise handback %s --note \"what you did\"\n", res.Msg.Item.Id, res.Msg.Worktree, res.Msg.Item.Id)
			if ed := os.Getenv("VISUAL"); ed != "" && !noEditor {
				_ = exec.Command(ed, res.Msg.Worktree).Start()
			} else if ed := os.Getenv("EDITOR"); ed != "" && !noEditor {
				c := exec.Command(ed, res.Msg.Worktree)
				c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
				_ = c.Run()
			}
			return nil
		}}
	c.Flags().BoolVar(&noEditor, "no-editor", false, "do not open an editor")
	return c
}

func (e *env) handbackCmd() *cobra.Command {
	var note string
	c := &cobra.Command{Use: "handback <item>", Short: "Give a taken-over item back to the flow", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := e.ctx()
			defer cancel()
			res, err := e.client().Handback(ctx, req(&pb.HandbackRequest{Project: e.proj(ctx), Id: args[0], Note: note}))
			if err != nil {
				return err
			}
			fmt.Fprintln(e.out, "Handed back: "+itemLine(res.Msg.Item))
			return nil
		}}
	c.Flags().StringVar(&note, "note", "", "what you changed")
	return c
}

// chatCmd: configuring is talking. Plain sentences map to actions, with a
// confirmation before anything changes.
func (e *env) chatCmd() *cobra.Command {
	return &cobra.Command{Use: "chat", Short: "Talk to Barnraise: focus, pause, approve, status… in plain words", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			sc := bufio.NewScanner(e.in)
			fmt.Fprintln(e.out, "Barnraise: What do you need? (\"focus on payments until done\", \"pause\", \"status\", \"approve the plan of I-42\", \"quit\")")
			for {
				fmt.Fprint(e.out, "You: ")
				if !sc.Scan() {
					return nil
				}
				line := strings.TrimSpace(sc.Text())
				if line == "" {
					continue
				}
				if l := strings.ToLower(line); l == "quit" || l == "exit" || l == "sair" {
					return nil
				}
				act, args, explain := interpret(line)
				if act == "" {
					fmt.Fprintln(e.out, "Barnraise: I did not understand. Try: focus <area> [until <date>], pause, resume, status, approve the plan|delivery of I-<n>, answer Q-<n> <option>.")
					continue
				}
				if act != "status" {
					fmt.Fprintf(e.out, "Barnraise: %s Confirm? [y/N] ", explain)
					if !sc.Scan() || !strings.HasPrefix(strings.ToLower(strings.TrimSpace(sc.Text())), "y") && !strings.HasPrefix(strings.ToLower(strings.TrimSpace(sc.Text())), "s") {
						fmt.Fprintln(e.out, "Barnraise: Nothing changed.")
						continue
					}
				}
				root := e.rootCmd()
				root.SetArgs(append([]string{act}, args...))
				root.SetOut(e.out)
				if err := root.Execute(); err != nil {
					fmt.Fprintln(e.out, "Barnraise:", err)
				}
			}
		}}
}

// interpret maps an English or Portuguese sentence to a command.
func interpret(s string) (string, []string, string) {
	l := strings.ToLower(s)
	f := strings.Fields(l)
	has := func(ws ...string) bool {
		for _, w := range ws {
			if strings.Contains(l, w) {
				return true
			}
		}
		return false
	}
	findID := func(prefix string) string {
		for w := range strings.FieldsSeq(strings.ToUpper(s)) {
			w = strings.Trim(w, ".,;:!?")
			if strings.HasPrefix(w, prefix) {
				return w
			}
		}
		return ""
	}
	switch {
	case has("status", "como está", "como esta", "progresso", "progress"):
		return "status", nil, ""
	case has("resume", "retoma", "continua", "despausa"):
		return "resume", nil, "Resume every agent."
	case has("pause", "pausa", "para tudo", "stop everything", "emergência", "emergency"):
		return "pause", []string{"from chat"}, "Pause everything: running sessions stop and nothing starts."
	case has("approve", "aprova"):
		id := findID("I-")
		if p := findID("P-"); p != "" {
			return "approve", []string{p}, "Approve the proposal " + p + "."
		}
		if id == "" {
			return "", nil, ""
		}
		what := "--delivery"
		if has("plan", "plano") {
			what = "--plan"
		}
		return "approve", []string{id, what}, "Approve the " + strings.TrimPrefix(what, "--") + " of " + id + "."
	case has("focus", "foca", "foco"):
		var area, until string
		for i, w := range f {
			if (w == "on" || w == "na" || w == "no" || w == "em") && i+1 < len(f) && area == "" {
				j := i + 1
				if f[j] == "tela" || f[j] == "the" || f[j] == "screen" || f[j] == "área" || f[j] == "area" {
					j++
				}
				if j+1 < len(f) && f[j] == "de" {
					j++
				}
				if j < len(f) {
					area = strings.Trim(f[j], ".,")
				}
			}
			if (w == "until" || w == "até") && i+1 < len(f) {
				until = strings.Trim(strings.Join(f[i+1:], " "), ".")
			}
		}
		if area == "" {
			return "", nil, ""
		}
		args := []string{area}
		if until != "" && !strings.Contains(until, "pronta") && !strings.Contains(until, "done") && !strings.Contains(until, "terminar") && !strings.Contains(until, "ficar") {
			args = append(args, "--until", until)
		}
		return "focus", args, fmt.Sprintf("Focus on %s, %s; 80%% of capacity goes to it.", area, def(until, "until done"))
	case has("answer", "responde"):
		q := findID("Q-")
		if q == "" {
			return "", nil, ""
		}
		rest := strings.TrimSpace(s[strings.Index(strings.ToUpper(s), q)+len(q):])
		return "answer", []string{q, rest}, "Answer " + q + ": " + rest + "."
	}
	return "", nil, ""
}

// ---------- knowledge ----------

func (e *env) contextCmd() *cobra.Command {
	var budget int32
	c := &cobra.Command{Use: "context <item>", Short: "What an agent needs for an item, in a fixed order, cut to a token budget", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := e.ctx()
			defer cancel()
			res, err := e.client().Context(ctx, req(&pb.ContextRequest{Project: e.proj(ctx), Id: args[0], Budget: budget}))
			if err != nil {
				return err
			}
			e.print(res.Msg, func(w io.Writer) {
				fmt.Fprint(w, res.Msg.Text)
				fmt.Fprintf(w, "(%d of %d tokens)\n", res.Msg.Tokens, res.Msg.Budget)
			})
			return nil
		}}
	c.Flags().Int32Var(&budget, "budget", 0, "token budget (default from barnraise.yaml)")
	return c
}

func (e *env) findCmd() *cobra.Command {
	var kind, area, status, under string
	var limit int32
	var grep, regex bool
	c := &cobra.Command{Use: "find <text>", Short: "Layered search: ids, paths and symbols, filters, full text", Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := e.ctx()
			defer cancel()
			query := strings.Join(args, " ")
			switch {
			case regex:
				kind = "regex"
			case grep:
				kind = "grep"
			}
			if under != "" && (grep || regex) {
				query = "file:" + under + " " + query
			}
			res, err := e.client().Find(ctx, req(&pb.FindRequest{Project: e.proj(ctx), Query: query, Kind: kind, Area: area, Status: status, Limit: limit}))
			if err != nil {
				return err
			}
			e.print(res.Msg, func(w io.Writer) {
				if len(res.Msg.Hits) == 0 {
					fmt.Fprintln(w, "Nothing found.")
				}
				for _, h := range res.Msg.Hits {
					fmt.Fprintf(w, "%-28s %-10s %s — %s (~%d tokens, %s)\n", h.Id, h.Kind, h.Title, h.Summary, h.Tokens, h.Layer)
				}
			})
			return nil
		}}
	c.Flags().StringVar(&kind, "kind", "", "item, proposal, decision, standard, memory, symbol, file")
	c.Flags().StringVar(&area, "area", "", "area")
	c.Flags().StringVar(&status, "status", "", "status; all includes superseded and expired")
	c.Flags().Int32Var(&limit, "limit", 20, "results")
	c.Flags().BoolVar(&grep, "grep", false, "exact substring in the files' text (trigram index): path:line hits")
	c.Flags().BoolVar(&regex, "regex", false, "regular expression (RE2) in the files' text (trigram index)")
	c.Flags().StringVar(&under, "path", "", "with --grep or --regex: only files under this path")
	return c
}

func (e *env) knowCmd(name, short string) *cobra.Command {
	return &cobra.Command{Use: name + " <target>", Short: short, Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := e.ctx()
			defer cancel()
			cl := e.client()
			r := req(&pb.KnowledgeRequest{Project: e.proj(ctx), Target: args[0]})
			var res *pb.KnowledgeResponse
			switch name {
			case "get":
				out, err := cl.Get(ctx, r)
				if err != nil {
					return err
				}
				res = out.Msg
			case "related":
				out, err := cl.Related(ctx, r)
				if err != nil {
					return err
				}
				res = out.Msg
			case "why":
				out, err := cl.Why(ctx, r)
				if err != nil {
					return err
				}
				res = out.Msg
			case "history":
				out, err := cl.History(ctx, r)
				if err != nil {
					return err
				}
				res = out.Msg
			case "rules":
				out, err := cl.Rules(ctx, r)
				if err != nil {
					return err
				}
				res = out.Msg
			}
			e.print(res, func(w io.Writer) { fmt.Fprintln(w, res.Text) })
			return nil
		}}
}

func (e *env) revalidateCmd() *cobra.Command {
	var files, items, decisions []string
	var cause string
	var sweep bool
	c := &cobra.Command{Use: "revalidate", Short: "Find the items a change affects and triage them (living plan)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := e.ctx()
			defer cancel()
			cl := e.client()
			var res *pb.RevalidateResponse
			if sweep {
				out, err := cl.Sweep(ctx, req(&pb.ProjectRef{Project: e.proj(ctx)}))
				if err != nil {
					return err
				}
				res = out.Msg
			} else {
				ch, err := structpb.NewStruct(map[string]any{"kind": "manual", "cause": def(cause, "manual revalidation"), "files": toAny(files), "items": toAny(items), "decisions": toAny(decisions)})
				if err != nil {
					return err
				}
				out, err := cl.Revalidate(ctx, req(&pb.RevalidateRequest{Project: e.proj(ctx), Change: ch}))
				if err != nil {
					return err
				}
				res = out.Msg
			}
			e.print(res, nil)
			return nil
		}}
	c.Flags().StringSliceVar(&files, "files", nil, "changed files")
	c.Flags().StringSliceVar(&items, "items", nil, "changed items (dependencies)")
	c.Flags().StringSliceVar(&decisions, "decisions", nil, "changed decisions")
	c.Flags().StringVar(&cause, "cause", "", "what changed")
	c.Flags().BoolVar(&sweep, "sweep", false, "nightly sweep of items not validated for too long")
	return c
}

func toAny(s []string) []any {
	out := make([]any, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}
