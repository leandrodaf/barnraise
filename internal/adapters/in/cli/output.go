package cli

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/leandrodaf/barnraise/internal/adapters/out/outfilter"
	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/paths"
)

// outputsDir is where filtered outputs are kept in full: the item's session
// folder inside an agent session, the data folder otherwise.
func outputsDir() string {
	if wt := os.Getenv("BARNRAISE_WORKTREE"); wt != "" {
		return filepath.Join(wt, ".barnraise-session", domain.OutputsDir)
	}
	return filepath.Join(paths.DataDir(), domain.OutputsDir)
}

func (e *env) filterCmd() *cobra.Command {
	return &cobra.Command{Use: "filter [--raw] -- <command…>", Short: "Run a noisy command and print only the errors and summaries, with a handle to the full output", Args: cobra.MinimumNArgs(1),
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			raw := false
			for len(args) > 0 && args[0] != "--" && strings.HasPrefix(args[0], "--") {
				switch args[0] {
				case "--raw":
					raw = true
				case "--help", "-h":
					return cmd.Help()
				default:
					return fmt.Errorf("unknown flag %s: usage: barnraise filter [--raw] -- <command>", args[0])
				}
				args = args[1:]
			}
			if len(args) > 0 && args[0] == "--" {
				args = args[1:]
			}
			if len(args) == 0 {
				return fmt.Errorf("usage: barnraise filter [--raw] -- <command>")
			}
			if len(args) == 1 && strings.Contains(args[0], " ") {
				args = []string{"sh", "-c", args[0]}
			}
			res, err := outfilter.RunCommand(context.Background(), args, outfilter.Options{
				Store: outfilter.StoreFromEnv(outputsDir()), Delegate: outfilter.DelegateFromEnv(),
				Run: os.Getenv(domain.EnvRun), Raw: raw, Stdin: os.Stdin,
			})
			if err != nil {
				return err
			}
			fmt.Fprintln(e.out, res.Text)
			if res.Exit != 0 {
				return exitCode(res.Exit)
			}
			return nil
		}}
}

func (e *env) outputCmd() *cobra.Command {
	var dir string
	c := &cobra.Command{Use: "output", Short: "Full text of the outputs `barnraise filter` shortened"}
	c.PersistentFlags().StringVar(&dir, "dir", "", "folder of kept outputs (default: this session's, or the data folder's)")
	store := func() outfilter.Store {
		if dir != "" {
			return outfilter.StoreFromEnv(dir)
		}
		return outfilter.StoreFromEnv(outputsDir())
	}
	var grep, lines string
	show := &cobra.Command{Use: "show <id>", Short: "Print a kept output, or only its matching lines (--grep) or a range (--lines a:b)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			from, to, err := parseRange(lines)
			if err != nil {
				return err
			}
			text, err := store().Show(args[0], grep, from, to)
			if err != nil {
				return err
			}
			fmt.Fprint(e.out, text)
			return nil
		}}
	show.Flags().StringVar(&grep, "grep", "", "only the lines matching this regular expression, with their numbers")
	show.Flags().StringVar(&lines, "lines", "", "only these lines: a:b, a: or :b (1-based)")
	var limit int
	list := &cobra.Command{Use: "list", Short: "Kept outputs, newest first", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			recs, err := store().List()
			if err != nil {
				return err
			}
			if limit > 0 && len(recs) > limit {
				recs = recs[:limit]
			}
			if e.json {
				e.printAny(recs)
				return nil
			}
			if len(recs) == 0 {
				fmt.Fprintln(e.out, "No kept outputs.")
				return nil
			}
			for _, r := range recs {
				fmt.Fprintf(e.out, "%s  %s  exit %d  %-13s %6d → %-5d tokens  %s\n", r.ID, r.At.Local().Format(time.DateTime), r.Exit, r.Tool, r.RawTokens, r.ShownTokens, r.Command)
			}
			return nil
		}}
	list.Flags().IntVar(&limit, "limit", 20, "most outputs to list (0: all)")
	stats := &cobra.Command{Use: "stats", Short: "Tokens the output filter kept out of the agents' context", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			recs, err := store().List()
			if err != nil {
				return err
			}
			st := outputStats(recs)
			if e.json {
				e.printAny(st)
				return nil
			}
			fmt.Fprintf(e.out, "%d outputs shortened: %d tokens → %d (%d saved, %.0f%%)\n", st.Runs, st.RawTokens, st.ShownTokens, st.Saved, st.SavedPct)
			for _, t := range st.ByTool {
				fmt.Fprintf(e.out, "  %-14s %4d runs  %8d saved\n", t.Tool, t.Runs, t.Saved)
			}
			return nil
		}}
	c.AddCommand(show, list, stats)
	return c
}

type toolStats struct {
	Tool  string `json:"tool"`
	Runs  int    `json:"runs"`
	Saved int64  `json:"saved_tokens"`
}

type filterStats struct {
	Runs        int         `json:"runs"`
	RawTokens   int64       `json:"raw_tokens"`
	ShownTokens int64       `json:"shown_tokens"`
	Saved       int64       `json:"saved_tokens"`
	SavedPct    float64     `json:"saved_pct"`
	ByTool      []toolStats `json:"by_tool"`
}

func outputStats(recs []domain.OutputRecord) filterStats {
	var st filterStats
	by := map[string]*toolStats{}
	for _, r := range recs {
		st.Runs++
		st.RawTokens += int64(r.RawTokens)
		st.ShownTokens += int64(r.ShownTokens)
		st.Saved += int64(r.Saved())
		t := by[r.Tool]
		if t == nil {
			t = &toolStats{Tool: r.Tool}
			by[r.Tool] = t
		}
		t.Runs++
		t.Saved += int64(r.Saved())
	}
	if st.RawTokens > 0 {
		st.SavedPct = 100 * float64(st.Saved) / float64(st.RawTokens)
	}
	for _, t := range by {
		st.ByTool = append(st.ByTool, *t)
	}
	slices.SortFunc(st.ByTool, func(a, b toolStats) int {
		if a.Saved != b.Saved {
			return cmp.Compare(b.Saved, a.Saved)
		}
		return strings.Compare(a.Tool, b.Tool)
	})
	return st
}

// parseRange reads a:b, a:, :b or a single line number.
func parseRange(s string) (int, int, error) {
	if s == "" {
		return 0, 0, nil
	}
	a, b, found := strings.Cut(s, ":")
	if !found {
		b = a
	}
	num := func(v string) (int, error) {
		if v == "" {
			return 0, nil
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return 0, fmt.Errorf("--lines %q: use a:b with line numbers from 1", s)
		}
		return n, nil
	}
	from, err := num(a)
	if err != nil {
		return 0, 0, err
	}
	to, err := num(b)
	if err != nil {
		return 0, 0, err
	}
	if from > 0 && to > 0 && to < from {
		return 0, 0, fmt.Errorf("--lines %q: the end is before the start", s)
	}
	return from, to, nil
}
