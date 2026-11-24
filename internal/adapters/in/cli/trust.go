package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	natsx "github.com/leandrodaf/barnraise/internal/adapters/out/nats"
	"github.com/leandrodaf/barnraise/internal/adapters/out/sqlite"
	"github.com/leandrodaf/barnraise/internal/app"
	"github.com/leandrodaf/barnraise/internal/daemoncfg"
	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/paths"
)

// apiJSON calls a plain JSON endpoint of the daemon with the CLI's token.
func (e *env) apiJSON(ctx context.Context, method, path string, q url.Values, out any) error {
	cl := e.client()
	u := cl.BaseURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	r, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return err
	}
	r.Header.Set("Authorization", "Bearer "+cl.Token)
	resp, err := cl.HTTP.Do(r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode != http.StatusOK {
		var m map[string]string
		if json.Unmarshal(body, &m) == nil && m["error"] != "" {
			return fmt.Errorf("%s", m["error"])
		}
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return json.Unmarshal(body, out)
}

func (e *env) eventsVerifyCmd() *cobra.Command {
	var from int64
	var offline bool
	var db string
	c := &cobra.Command{Use: "verify", Short: "Walk the hash chain of the event log and report the first break", Args: cobra.NoArgs,
		Long: "Every stored event carries the hash of the one before it. verify recomputes the chain (and checks the last\n" +
			"signed anchor) and names the first event that was edited, removed or inserted outside Barnraise.\n" +
			"It asks the daemon; with --offline (or when the daemon is not running) it reads the state file itself.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := e.ctx()
			defer cancel()
			var rep domain.ChainReport
			var err error
			if !offline {
				q := url.Values{}
				if from > 0 {
					q.Set("from", strconv.FormatInt(from, 10))
				}
				err = e.apiJSON(ctx, http.MethodGet, "/v1/events/verify", q, &rep)
				if err != nil && !daemonDown(err) {
					return err
				}
				offline = err != nil
			}
			if offline {
				if rep, err = verifyOffline(ctx, db, from); err != nil {
					return err
				}
			}
			if e.json {
				e.printAny(rep)
			} else {
				printChain(e.out, rep)
			}
			if !rep.OK() {
				return exitCode(2)
			}
			return nil
		}}
	c.Flags().Int64Var(&from, "from", 0, "start at this sequence number (default: the first event)")
	c.Flags().BoolVar(&offline, "offline", false, "read the state file directly instead of asking the daemon")
	c.Flags().StringVar(&db, "db", "", "state file for --offline (default: barnraise.db in the data folder)")
	return c
}

func daemonDown(err error) bool {
	s := err.Error()
	return strings.Contains(s, "no such file") || strings.Contains(s, "connection refused")
}

func verifyOffline(ctx context.Context, db string, from int64) (domain.ChainReport, error) {
	if db == "" {
		db = filepath.Join(paths.DataDir(), "barnraise.db")
	}
	st, err := sqlite.OpenReadOnly(db)
	if err != nil {
		return domain.ChainReport{}, err
	}
	defer st.Close()
	rep, err := st.VerifyChain(ctx, from)
	if err != nil {
		return rep, err
	}
	pub := ""
	if df, err := daemoncfg.Load(); err == nil {
		pub = df.NKeyPublic
	}
	rep.Anchor, err = st.CheckAnchor(ctx, natsx.AnchorVerifier(pub))
	return rep, err
}

func short(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

func printChain(w io.Writer, rep domain.ChainReport) {
	if rep.Break == nil {
		fmt.Fprintf(w, "✔ event chain intact: %d events checked (seq %d..%d), head %s\n", rep.Checked, rep.From, rep.To, short(rep.Head))
	} else {
		note, _ := app.ChainProblem(domain.ChainReport{Break: rep.Break})
		fmt.Fprintf(w, "✘ event chain broken at seq %d (%s): %s\n", rep.Break.Seq, rep.Break.Reason, note)
		fmt.Fprintf(w, "  checked %d events (seq %d..%d)\n", rep.Checked, rep.From, rep.To)
	}
	if rep.Genesis > 0 {
		fmt.Fprintf(w, "  events up to seq %d existed before the chain: the migration hashed them as they were then\n", rep.Genesis)
	}
	switch a := rep.Anchor; {
	case a == nil:
		fmt.Fprintln(w, "  no signed anchor yet (the daemon writes one at start and every hour)")
	case a.OK:
		fmt.Fprintf(w, "✔ signed anchor at seq %d holds\n", a.Seq)
	default:
		fmt.Fprintf(w, "✘ signed anchor at seq %d: %s\n", a.Seq, a.Reason)
	}
}

func (e *env) trustCmd() *cobra.Command {
	var status bool
	c := &cobra.Command{Use: "trust [item]", Short: "Re-trust the files that steer agents after checking a change",
		Long: "Before each session Barnraise checks that barnraise.yaml and .barnraise/ in the project root, the instruction\n" +
			"files of the item's worktree (CLAUDE.md, AGENTS.md, GEMINI.md, .mcp.json, .claude/, .codex/, .gemini/) and\n" +
			"the hook binary were not changed outside the owner's hands. When they were, sessions are refused.\n\n" +
			"  barnraise trust           trust the project root as it is now\n" +
			"  barnraise trust I-42      accept the instruction files of I-42's worktree as they are now\n" +
			"  barnraise trust --status  show what would refuse a session, without changing anything",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := e.ctx()
			defer cancel()
			q := url.Values{}
			if p := e.proj(ctx); p != "" {
				q.Set("project", p)
			}
			if len(args) == 1 {
				q.Set("item", args[0])
			}
			method := http.MethodPost
			if status {
				method = http.MethodGet
			}
			var rep app.TrustReport
			if err := e.apiJSON(ctx, method, "/v1/trust", q, &rep); err != nil {
				return err
			}
			if e.json {
				e.printAny(rep)
			} else {
				printTrust(e.out, rep, !status)
			}
			if status && len(rep.Problems) > 0 {
				return exitCode(2)
			}
			return nil
		}}
	c.Flags().BoolVar(&status, "status", false, "only report; trust nothing")
	return c
}

func printTrust(w io.Writer, rep app.TrustReport, wrote bool) {
	if wrote {
		what := "the project root (barnraise.yaml, .barnraise/)"
		if rep.Item != "" {
			what = "the instruction files of " + rep.Item
		}
		fmt.Fprintf(w, "✔ %s: %d files trusted as they are now\n", what, len(rep.Recorded))
		for _, p := range sortedPaths(rep.Recorded) {
			fmt.Fprintf(w, "  %s  %s\n", short(rep.Recorded[p]), p)
		}
	}
	if len(rep.Problems) == 0 {
		fmt.Fprintln(w, "✔ nothing would refuse a session of "+rep.Project)
		return
	}
	for _, pr := range rep.Problems {
		fmt.Fprintf(w, "✘ %s: %s\n", pr.Scope, domain.TrustPaths(pr.Changes, 0))
		fmt.Fprintf(w, "  fix: %s\n", pr.Fix)
	}
}

func sortedPaths(m domain.Manifest) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
