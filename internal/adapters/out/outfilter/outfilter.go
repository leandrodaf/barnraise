// Package outfilter turns long command output into a short summary for an
// agent: the errors, the summary lines and a handle to the full text. It
// backs `barnraise filter -- <cmd>`, which a PreToolUse hook wraps around
// noisy commands (builds, test runners, linters, git, listings, searches),
// and the check runner. Commands without a native filter may be delegated
// to rtk when it is installed (ADR-0036).
package outfilter

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
)

// Tools recognized by Filter and Detect.
const (
	ToolGradle  = "gradle"
	ToolGo      = "go"
	ToolJest    = "jest"
	ToolVitest  = "vitest"
	ToolJS      = "js" // npm/yarn/pnpm test: jest or vitest
	ToolPytest  = "pytest"
	ToolMaestro = "maestro"
	ToolGeneric = "generic"

	ToolGitStatus   = "git-status"
	ToolGitDiff     = "git-diff" // diff and show
	ToolGitLog      = "git-log"
	ToolGitSync     = "git-sync" // push, pull, fetch, clone
	ToolLint        = "golangci-lint"
	ToolCargo       = "cargo"
	ToolMaven       = "mvn"
	ToolMake        = "make"
	ToolTSC         = "tsc"
	ToolESLint      = "eslint"
	ToolList        = "list" // ls, tree, docker ps, kubectl get
	ToolFind        = "find"
	ToolGrep        = "grep" // grep, rg, ag, git grep
	ToolDockerBuild = "docker-build"
	ToolLogs        = "logs" // docker logs, kubectl logs, compose up
	ToolKubectl     = "kubectl"
	ToolInstall     = "install" // npm/pnpm/yarn/pip/bundle install
)

const (
	// MaxLines bounds the summary body.
	MaxLines = 50
	// TailLines is what the generic filter keeps.
	TailLines  = 40
	maxLineLen = 300
	// Transparent tools print short outputs unchanged.
	smallLines = 40
	smallBytes = 4 << 10
)

// transparent tools (git, listings, searches, logs) keep a short output as
// it is: the agent asked for that text, not for a verdict.
var transparent = map[string]bool{
	ToolGitStatus: true, ToolGitDiff: true, ToolGitLog: true, ToolGitSync: true, ToolList: true,
	ToolFind: true, ToolGrep: true, ToolLogs: true, ToolKubectl: true,
}

// headFirst tools keep the beginning when nothing better is known.
var headFirst = map[string]bool{ToolGitDiff: true, ToolGitLog: true, ToolList: true, ToolFind: true, ToolGrep: true}

// Filter summarizes output for a tool.
func Filter(tool string, output []byte, exitCode int) string {
	return FilterWithLog(tool, output, exitCode, "")
}

// FilterWithLog summarizes output and ends with "full log: <path>" when
// logPath is set.
func FilterWithLog(tool string, output []byte, exitCode int, logPath string) string {
	footer := ""
	if logPath != "" {
		footer = "full log: " + logPath
	}
	s, _ := Summarize(tool, output, exitCode, footer)
	return s
}

// Summarize returns what the agent sees and whether it is shorter than the
// output. A short output of a transparent tool comes back unchanged (only
// colors and progress redraws removed), without footer.
func Summarize(tool string, output []byte, exitCode int, footer string) (string, bool) {
	lines := splitLines(output)
	tool = normalizeTool(tool)
	if transparent[tool] && len(lines) <= smallLines && len(output) <= smallBytes {
		return strings.Join(lines, "\n"), false
	}
	status := "passed"
	if exitCode != 0 {
		status = fmt.Sprintf("failed (exit %d)", exitCode)
	}
	var body, summary []string
	if f, ok := funcs[tool]; ok {
		body, summary = f(lines, exitCode)
	} else if r, ok := rules[tool]; ok {
		body, summary = r.apply(lines, exitCode)
	}
	if len(body) == 0 && (exitCode != 0 || len(summary) == 0) {
		n := TailLines
		if exitCode == 0 && !transparent[tool] {
			n = 5
		}
		runs := collapseRuns(lines)
		if headFirst[tool] && exitCode == 0 {
			body = head(runs, n)
		} else {
			body = tail(runs, n)
		}
		summary = nil
	}
	limit := MaxLines
	if n := bigBodies[tool]; n > 0 {
		limit = n
	}
	body = capLines(collapseRuns(body), limit-len(summary))
	body = append(body, summary...)

	var b strings.Builder
	if transparent[tool] {
		fmt.Fprintf(&b, "%s: %d lines shortened", tool, len(lines))
		if exitCode != 0 {
			fmt.Fprintf(&b, ", exit %d", exitCode)
		}
		b.WriteByte('\n')
	} else {
		fmt.Fprintf(&b, "%s: %s\n", tool, status)
	}
	for _, l := range body {
		b.WriteString(l)
		b.WriteByte('\n')
	}
	if footer != "" {
		b.WriteString(footer + "\n")
	}
	return strings.TrimRight(b.String(), "\n"), true
}

func normalizeTool(t string) string {
	t = strings.ToLower(strings.TrimSpace(t))
	switch t {
	case "gradlew", "./gradlew", "gradle":
		return ToolGradle
	case "go", "gotest", "go test":
		return ToolGo
	case "jest", "vitest", "pytest", "maestro", "js":
		return t
	case "npm", "yarn", "pnpm", "bun":
		return ToolJS
	}
	if _, ok := funcs[t]; ok {
		return t
	}
	if _, ok := rules[t]; ok {
		return t
	}
	if t == ToolList || t == ToolLogs {
		return t
	}
	return ToolGeneric
}

// rule picks a matching line plus up to `after` following lines that
// satisfy `cont` (nil: any non-blank line).
type rule struct {
	re    *regexp.Regexp
	after int
	cont  *regexp.Regexp
	stop  *regexp.Regexp
}

// collapse counts lines matching the pattern instead of listing them all.
type collapse struct {
	re    *regexp.Regexp
	label string
}

type toolRules struct {
	fail    []rule // used when exit != 0
	success []rule // used when exit == 0 (and as summary lines on failure)
	// collapses are counted, never listed.
	collapses []collapse
	// skip are lines never picked (help noise around errors).
	skip *regexp.Regexp
	// dedupe merges identical picked lines wherever they are.
	dedupe bool
	// failAlways also picks failure lines when the exit code is 0
	// (linters and compilers that report problems and still succeed).
	failAlways bool
}

// apply returns the failure lines and the summary lines.
func (t toolRules) apply(lines []string, exit int) (fail, summary []string) {
	var picked []string
	seen := map[int]bool{}
	if t.skip != nil {
		for i, l := range lines {
			if t.skip.MatchString(l) {
				seen[i] = true
			}
		}
	}
	add := func(i int) {
		if !seen[i] {
			seen[i] = true
			if strings.TrimSpace(lines[i]) != "" {
				picked = append(picked, lines[i])
			}
		}
	}
	run := func(rs []rule) {
		for i := range lines {
			if seen[i] {
				continue
			}
			for _, r := range rs {
				if !r.re.MatchString(lines[i]) {
					continue
				}
				add(i)
				for j := i + 1; j < len(lines) && j <= i+r.after; j++ {
					l := lines[j]
					if r.stop != nil && r.stop.MatchString(l) {
						break
					}
					if r.cont != nil && !r.cont.MatchString(l) {
						break
					}
					if r.cont == nil && strings.TrimSpace(l) == "" {
						break
					}
					add(j)
				}
				break
			}
		}
	}
	if exit != 0 || t.failAlways {
		run(t.fail)
	}
	fail, picked = picked, nil
	if t.dedupe {
		fail = dedupeAll(fail)
	}
	counts := make([]int, len(t.collapses))
	for i, l := range lines {
		if seen[i] {
			continue
		}
		for k, c := range t.collapses {
			if c.re.MatchString(l) {
				counts[k]++
				seen[i] = true
				break
			}
		}
	}
	run(t.success)
	for k, n := range counts {
		if n > 0 {
			picked = append(picked, fmt.Sprintf("%d %s", n, t.collapses[k].label))
		}
	}
	if len(picked) > 10 {
		picked = picked[len(picked)-10:]
	}
	return fail, picked
}

var (
	indented = regexp.MustCompile(`^(\s{2,}|\t)\S`)
	anyLine  = regexp.MustCompile(``) // also blank lines
)

var rules = map[string]toolRules{
	ToolGradle: {
		fail: []rule{
			{re: regexp.MustCompile(`^FAILURE: `)},
			{re: regexp.MustCompile(`^\* What went wrong:`), after: 12, cont: anyLine, stop: regexp.MustCompile(`^\* (Try|Get more help|Exception is):`)},
			{re: regexp.MustCompile(`^e: `)},
			{re: regexp.MustCompile(`^w: .*(error|unresolved)`)},
			{re: regexp.MustCompile(`\.(java|kt):\d+: error: `), after: 2, cont: indented},
			{re: regexp.MustCompile(`^> Task \S+ FAILED`)},
			{re: regexp.MustCompile(`^\S.* > .* FAILED$`), after: 3, cont: indented},
			{re: regexp.MustCompile(`^\d+ tests? completed, \d+ failed`)},
			{re: regexp.MustCompile(`^Execution failed for `)},
		},
		success: []rule{
			{re: regexp.MustCompile(`^BUILD (SUCCESSFUL|FAILED) in `)},
		},
	},
	ToolGo: {
		fail: []rule{
			{re: regexp.MustCompile(`^\s*--- FAIL: `), after: 10, cont: indented},
			{re: regexp.MustCompile(`^panic: `), after: 12, cont: anyLine},
			{re: regexp.MustCompile(`^# \S+`)},
			{re: regexp.MustCompile(`^\S+\.go:\d+(:\d+)?: `)},
			{re: regexp.MustCompile(`^FAIL\s+\S+`)},
			{re: regexp.MustCompile(`^WARNING: DATA RACE`), after: 8, cont: anyLine},
		},
		success: []rule{
			{re: regexp.MustCompile(`^(ok|\?)\s+\S+.*\[(no test files|setup failed)\]`)},
		},
		collapses: []collapse{{regexp.MustCompile(`^ok\s+\S+`), "packages ok"}},
	},
	ToolJest:   jsRules,
	ToolVitest: jsRules,
	ToolJS:     jsRules,
	ToolPytest: {
		fail: []rule{
			{re: regexp.MustCompile(`^_{3,} .* _{3,}$`)},
			{re: regexp.MustCompile(`^E\s{2,}`)},
			{re: regexp.MustCompile(`^\S+\.py:\d+: \w+`)},
			{re: regexp.MustCompile(`^(FAILED|ERROR) `)},
		},
		success: []rule{
			{re: regexp.MustCompile(`^=+ .*\b(passed|failed|error|errors|skipped)\b.* in [\d.]+s.*=+$`)},
		},
	},
	ToolMaestro: {
		fail: []rule{
			{re: regexp.MustCompile(`(?i)\bFAILED\b|\[Failed\]|❌`), after: 3, cont: indented},
			{re: regexp.MustCompile(`(?i)assert(ion)?\b.*(false|failed|error)|Assertion is false`)},
			{re: regexp.MustCompile(`(?i)element not found|no visible element|timed out waiting`)},
			{re: regexp.MustCompile(`(?i)^\s*(Exception|Error):`)},
		},
		success: []rule{
			{re: regexp.MustCompile(`(?i)\b\d+/\d+ (flows? )?(passed|failed)|flows? (passed|failed)|^\s*\[Passed\]`)},
		},
	},
}

var jsRules = toolRules{
	fail: []rule{
		{re: regexp.MustCompile(`^\s*FAIL\s+\S+`)},
		{re: regexp.MustCompile(`^\s*● `), after: 10, cont: anyLine, stop: regexp.MustCompile(`^\s*(● |at |Test Suites:|FAIL|PASS)`)},
		{re: regexp.MustCompile(`^\s*(×|✗|✕)\s`)},
		{re: regexp.MustCompile(`(AssertionError|TypeError|ReferenceError|SyntaxError)`), after: 4, cont: indented},
		{re: regexp.MustCompile(`^\s*❯ \S+:\d+`)},
	},
	success: []rule{
		{re: regexp.MustCompile(`^\s*(Test Suites|Tests|Snapshots|Test Files|Duration):\s`)},
		{re: regexp.MustCompile(`^\s*(Tests|Test Files)\s+\d+ (passed|failed)`)},
	},
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07]*\x07`)

func splitLines(out []byte) []string {
	out = ansi.ReplaceAll(out, nil)
	out = bytes.ReplaceAll(out, []byte("\r\n"), []byte("\n"))
	raw := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	lines := make([]string, 0, len(raw))
	for _, l := range raw {
		// Progress bars rewrite the line with \r; keep the final state.
		if i := strings.LastIndexByte(l, '\r'); i >= 0 {
			l = l[i+1:]
		}
		if len(l) > maxLineLen {
			l = l[:maxLineLen] + "…"
		}
		lines = append(lines, l)
	}
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	return lines
}

func tail(lines []string, n int) []string {
	if len(lines) <= n {
		return lines
	}
	return append([]string{fmt.Sprintf("… %d earlier lines omitted", len(lines)-n)}, lines[len(lines)-n:]...)
}

func head(lines []string, n int) []string {
	if len(lines) <= n {
		return lines
	}
	return append(append([]string(nil), lines[:n]...), fmt.Sprintf("… %d more lines omitted", len(lines)-n))
}

func capLines(lines []string, n int) []string {
	if n < 2 {
		n = 2
	}
	if len(lines) <= n {
		return lines
	}
	out := append([]string(nil), lines[:n-1]...)
	return append(out, fmt.Sprintf("… %d more lines omitted", len(lines)-(n-1)))
}

// collapseRuns turns a run of identical lines into one line with a count.
func collapseRuns(lines []string) []string {
	out := make([]string, 0, len(lines))
	for i := 0; i < len(lines); {
		j := i + 1
		for j < len(lines) && lines[j] == lines[i] {
			j++
		}
		if n := j - i; n > 2 && strings.TrimSpace(lines[i]) != "" {
			out = append(out, fmt.Sprintf("%s (×%d)", lines[i], n))
		} else {
			out = append(out, lines[i:j]...)
		}
		i = j
	}
	return out
}

// dedupeAll keeps the first of identical lines anywhere, with a count.
func dedupeAll(lines []string) []string {
	count := map[string]int{}
	for _, l := range lines {
		count[l]++
	}
	out := make([]string, 0, len(count))
	done := map[string]bool{}
	for _, l := range lines {
		if done[l] {
			continue
		}
		done[l] = true
		if n := count[l]; n > 1 {
			out = append(out, fmt.Sprintf("%s (×%d)", l, n))
		} else {
			out = append(out, l)
		}
	}
	return out
}
