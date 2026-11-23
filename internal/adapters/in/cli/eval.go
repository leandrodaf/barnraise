package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	pb "github.com/leandrodaf/barnraise/gen/barnraise/v1"
	"github.com/leandrodaf/barnraise/internal/domain"
)

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// EvalReport measures the knowledge pack against what integrated items
// really touched, and compares items in an experiment with the rest.
type EvalReport struct {
	Items        int                     `json:"items"`
	RecallAvg    float64                 `json:"recall_avg"`
	TokensAvg    float64                 `json:"pack_tokens_avg"`
	PerItem      []EvalItem              `json:"per_item"`
	Experiments  map[string]EvalCohort   `json:"experiments,omitempty"`
	Decision     string                  `json:"decision"`
}

// EvalItem is one old item replayed as if it were new.
type EvalItem struct {
	ID      string  `json:"id"`
	Files   int     `json:"files"`
	Found   int     `json:"found"`
	Recall  float64 `json:"recall"`
	Tokens  int32   `json:"tokens"`
	Reworks int     `json:"reworks"`
}

// EvalCohort compares rework between items inside and outside an experiment.
type EvalCohort struct {
	Items      int     `json:"items"`
	ReworkAvg  float64 `json:"rework_avg"`
	ControlAvg float64 `json:"control_rework_avg"`
	Verdict    string  `json:"verdict"`
}

func (e *env) evalCmd() *cobra.Command {
	var limit int
	var budget int32
	c := &cobra.Command{Use: "eval", Short: "Replay integrated items: does the context pack hold what the PR really used?", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := e.ctx()
			defer cancel()
			cl := e.client()
			project := e.proj(ctx)
			list, err := cl.ListItems(ctx, req(&pb.ListItemsRequest{Project: project, Status: "integrated"}))
			if err != nil {
				return err
			}
			items := list.Msg.Items
			sort.Slice(items, func(i, j int) bool { return items[i].IntegratedAt > items[j].IntegratedAt })
			if limit > 0 && len(items) > limit {
				items = items[:limit]
			}
			rep := EvalReport{Experiments: map[string]EvalCohort{}}
			var recallSum, tokSum float64
			for _, it := range items {
				if len(it.FilesTouched) == 0 {
					continue
				}
				res, err := cl.Context(ctx, req(&pb.ContextRequest{Project: project, Id: it.Id, Budget: budget}))
				if err != nil {
					continue
				}
				// The pack built as if the item were new: only what anchors and the index give.
				text := strings.ToLower(res.Msg.Text)
				found := 0
				for _, f := range it.FilesTouched {
					if strings.Contains(text, strings.ToLower(f)) {
						found++
					}
				}
				r := float64(found) / float64(len(it.FilesTouched))
				rep.PerItem = append(rep.PerItem, EvalItem{ID: it.Id, Files: len(it.FilesTouched), Found: found, Recall: r, Tokens: res.Msg.Tokens, Reworks: len(it.Rework)})
				recallSum += r
				tokSum += float64(res.Msg.Tokens)
			}
			rep.Items = len(rep.PerItem)
			if rep.Items > 0 {
				rep.RecallAvg = recallSum / float64(rep.Items)
				rep.TokensAvg = tokSum / float64(rep.Items)
			}
			// Experiments: items that ran with BARNRAISE_EXPERIMENT are marked in their session events.
			evs, err := cl.ListEvents(ctx, req(&pb.EventsRequest{Project: project, Types: []string{domain.EvAgentSession}, Limit: 1000}))
			if err == nil {
				exp := map[string]map[string]bool{}
				for _, ev := range evs.Msg.Events {
					sess := ev.Data.GetFields()["session"].GetStructValue()
					if sess == nil {
						continue
					}
					if d := sess.Fields["detail"].GetStringValue(); d != "" && sess.Fields["state"].GetStringValue() == "starting" {
						if exp[d] == nil {
							exp[d] = map[string]bool{}
						}
						exp[d][sess.Fields["item_id"].GetStringValue()] = true
					}
				}
				for name, ids := range exp {
					var in, out, nIn, nOut float64
					for _, p := range rep.PerItem {
						if ids[p.ID] {
							in += float64(p.Reworks)
							nIn++
						} else {
							out += float64(p.Reworks)
							nOut++
						}
					}
					c := EvalCohort{Items: int(nIn)}
					if nIn > 0 {
						c.ReworkAvg = in / nIn
					}
					if nOut > 0 {
						c.ControlAvg = out / nOut
					}
					switch {
					case nIn < 3:
						c.Verdict = "not enough items yet"
					case c.ReworkAvg > c.ControlAvg*1.1:
						c.Verdict = "worse: roll it back (remove it from experiments)"
					default:
						c.Verdict = "not worse: can go to the whole fleet"
					}
					rep.Experiments[name] = c
				}
			}
			switch {
			case rep.Items == 0:
				rep.Decision = "no integrated items with touched files yet"
			case rep.RecallAvg >= 0.8:
				rep.Decision = "graph and text find what items use; keep vectors off"
			default:
				rep.Decision = "recall below 80%: improve anchors, area summaries or standards before trying vectors"
			}
			if e.json {
				e.printAny(rep)
				return nil
			}
			w := e.out
			fmt.Fprintf(w, "%d items replayed · recall %.0f%% · pack %.0f tokens on average\n", rep.Items, rep.RecallAvg*100, rep.TokensAvg)
			for _, p := range rep.PerItem {
				fmt.Fprintf(w, "  %-7s %d/%d files (%.0f%%) · %d tokens · %d rework rounds\n", p.ID, p.Found, p.Files, p.Recall*100, p.Tokens, p.Reworks)
			}
			for n, c := range rep.Experiments {
				fmt.Fprintf(w, "Experiment %s: %d items, rework %.1f vs %.1f — %s\n", n, c.Items, c.ReworkAvg, c.ControlAvg, c.Verdict)
			}
			fmt.Fprintln(w, "Decision: "+rep.Decision)
			_ = io.Discard
			return nil
		}}
	c.Flags().IntVar(&limit, "limit", 20, "how many recent integrated items")
	c.Flags().Int32Var(&budget, "budget", 0, "token budget of the pack")
	return c
}
