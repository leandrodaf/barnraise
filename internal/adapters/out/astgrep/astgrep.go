// Package astgrep is the built-in ast-grep check kind (runs_on:
// builtin:ast-grep): it runs `ast-grep scan` with the project's structural
// rules (sgconfig.yml) on the files a change touches and maps each match to
// the check's result. It is opt-in: a project declares the check, and the
// machine needs ast-grep installed.
package astgrep

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// Binary is the program the check runs.
const Binary = "ast-grep"

// Options configures the check.
type Options struct {
	Exec     ports.Exec
	LookPath func(string) (string, error) // default exec.LookPath
	Machine  string                       // named when ast-grep is missing
}

// Runner runs ast-grep checks.
type Runner struct{ o Options }

// New returns a runner.
func New(o Options) *Runner {
	if o.LookPath == nil {
		o.LookPath = exec.LookPath
	}
	return &Runner{o: o}
}

// Match is one line of `ast-grep scan --json=stream`.
type Match struct {
	File     string `json:"file"`
	RuleID   string `json:"ruleId"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
	Note     string `json:"note"`
	Lines    string `json:"lines"`
	Range    struct {
		Start struct {
			Line   int `json:"line"`
			Column int `json:"column"`
		} `json:"start"`
	} `json:"range"`
}

// maxListed bounds the matches written in the summary; the log has all.
const maxListed = 30

// Check runs the rules. A rule of severity error fails the check; the
// others are listed. When ast-grep is missing or cannot read the rules,
// the result is red with MainRed set: the machine or the configuration is
// wrong, not the change, so the item waits for the owner.
func (r *Runner) Check(ctx context.Context, spec ports.CheckSpec) (domain.CheckResult, []byte, error) {
	res := domain.CheckResult{}
	bin, err := r.o.LookPath(Binary)
	if err != nil {
		where := "this machine"
		if r.o.Machine != "" {
			where = r.o.Machine
		}
		res.MainRed, res.CannotRun = true, true
		res.Summary = fmt.Sprintf("check %s needs ast-grep, which is not installed on %s: install it (https://ast-grep.github.io) or remove the check", spec.Def.Name, where)
		return res, nil, nil
	}
	dir := filepath.Join(spec.Dir, filepath.FromSlash(spec.Def.Dir))
	args := []string{"scan", "--json=stream"}
	args = append(args, strings.Fields(spec.Def.Run)...)
	if len(spec.Touched) > 0 {
		files := r.inScope(dir, spec.Def.Dir, spec.Touched)
		if len(files) == 0 {
			res.Passed = true
			res.Summary = "no file of the change for ast-grep to scan"
			return res, nil, nil
		}
		args = append(args, files...)
	}
	timeout := spec.Def.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	out, err := r.o.Exec.Run(ctx, ports.Cmd{Name: bin, Args: args, Dir: dir, Env: os.Environ(), Timeout: timeout})
	res.Duration = out.Duration
	if err != nil {
		if ctx.Err() != nil {
			return res, nil, ctx.Err()
		}
		res.MainRed, res.CannotRun = true, true
		res.Summary = "ast-grep could not run: " + err.Error()
		return res, nil, nil
	}
	matches, bad := parse(out.Stdout)
	log := append(append([]byte(nil), out.Stdout...), out.Stderr...)
	if out.ExitCode != 0 && out.ExitCode != 1 || out.ExitCode == 1 && len(matches) == 0 || bad {
		// Not a verdict on the code: no sgconfig.yml, a rule that does not
		// parse, a crash.
		res.MainRed, res.CannotRun = true, true
		res.Summary = fmt.Sprintf("ast-grep could not scan (exit %d): %s", out.ExitCode, firstLine(out.Stderr, out.Stdout))
		return res, log, nil
	}
	res.Passed, res.Summary = verdict(matches)
	return res, log, nil
}

// inScope keeps the touched files (repository paths) that exist under the
// check's folder, relative to it.
func (r *Runner) inScope(dir, sub string, touched []string) []string {
	prefix := strings.Trim(filepath.ToSlash(sub), "/")
	var out []string
	for _, f := range touched {
		f = filepath.ToSlash(f)
		if prefix != "" {
			rest, ok := strings.CutPrefix(f, prefix+"/")
			if !ok {
				continue
			}
			f = rest
		}
		if st, err := os.Stat(filepath.Join(dir, filepath.FromSlash(f))); err == nil && !st.IsDir() {
			out = append(out, f)
		}
	}
	sort.Strings(out)
	return out
}

func parse(stdout []byte) ([]Match, bool) {
	var out []Match
	sc := bufio.NewScanner(bytes.NewReader(stdout))
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var m Match
		if err := json.Unmarshal(line, &m); err != nil {
			return out, true
		}
		out = append(out, m)
	}
	return out, false
}

func verdict(ms []Match) (bool, string) {
	if len(ms) == 0 {
		return true, "no match of the structural rules (ast-grep)"
	}
	sort.SliceStable(ms, func(i, j int) bool {
		if a, b := rank(ms[i].Severity), rank(ms[j].Severity); a != b {
			return a > b
		}
		if ms[i].File != ms[j].File {
			return ms[i].File < ms[j].File
		}
		return ms[i].Range.Start.Line < ms[j].Range.Start.Line
	})
	errs := 0
	count := map[string]int{}
	for _, m := range ms {
		sev := strings.ToLower(m.Severity)
		if sev == "" {
			sev = "error"
		}
		if sev == "error" {
			errs++
		}
		count[sev]++
	}
	var b strings.Builder
	if errs > 0 {
		fmt.Fprintf(&b, "%d structural rule %s at severity error (ast-grep):\n", errs, plural(errs, "violation", "violations"))
	} else {
		fmt.Fprintf(&b, "no structural rule at severity error is broken (ast-grep); %d other %s:\n", len(ms), plural(len(ms), "match", "matches"))
	}
	for i, m := range ms {
		if i == maxListed {
			fmt.Fprintf(&b, "… and %d more in the log\n", len(ms)-maxListed)
			break
		}
		msg := strings.TrimSpace(m.Message)
		if msg == "" {
			msg = strings.TrimSpace(m.Lines)
		}
		sev := strings.ToLower(m.Severity)
		if sev == "" {
			sev = "error"
		}
		// ast-grep counts lines and columns from 0.
		fmt.Fprintf(&b, "- %s:%d:%d %s [%s]: %s\n", m.File, m.Range.Start.Line+1, m.Range.Start.Column+1, m.RuleID, sev, msg)
	}
	return errs == 0, strings.TrimSpace(b.String())
}

func rank(sev string) int {
	switch strings.ToLower(sev) {
	case "error", "":
		return 3
	case "warning":
		return 2
	case "info":
		return 1
	}
	return 0
}

func firstLine(bs ...[]byte) string {
	for _, b := range bs {
		s := strings.TrimSpace(string(b))
		if s == "" {
			continue
		}
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[:i]
		}
		if len(s) > 300 {
			s = s[:300]
		}
		return s
	}
	return "no output"
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
