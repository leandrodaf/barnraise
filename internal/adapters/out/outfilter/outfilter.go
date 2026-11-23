// Package outfilter turns long build and test output into a short summary
// for an agent: the errors or the tail, plus the path of the full log. It
// backs `barnraise filter -- <cmd>`, which a PreToolUse hook wraps around
// noisy commands (Gradle, test runners, Maestro).
package outfilter

import (
	"bytes"
	"fmt"
	"path/filepath"
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
)

const (
	// MaxLines bounds the summary body.
	MaxLines = 50
	// TailLines is what the generic filter keeps.
	TailLines  = 40
	maxLineLen = 300
)

// Filter summarizes output for a tool.
func Filter(tool string, output []byte, exitCode int) string {
	return FilterWithLog(tool, output, exitCode, "")
}

// FilterWithLog summarizes output and ends with "full log: <path>" when
// logPath is set.
func FilterWithLog(tool string, output []byte, exitCode int, logPath string) string {
	lines := splitLines(output)
	tool = normalizeTool(tool)
	status := "passed"
	if exitCode != 0 {
		status = fmt.Sprintf("failed (exit %d)", exitCode)
	}
	var body, summary []string
	if r, ok := rules[tool]; ok {
		body, summary = r.apply(lines, exitCode)
	}
	if len(body) == 0 && (exitCode != 0 || len(summary) == 0) {
		n := TailLines
		if exitCode == 0 {
			n = 5
		}
		body = tail(lines, n)
		summary = nil
	}
	body = capLines(body, MaxLines-len(summary))
	body = append(body, summary...)

	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s\n", tool, status)
	for _, l := range body {
		b.WriteString(l)
		b.WriteByte('\n')
	}
	if logPath != "" {
		fmt.Fprintf(&b, "full log: %s\n", logPath)
	}
	return strings.TrimRight(b.String(), "\n")
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
	return ToolGeneric
}

// Detect guesses the tool from a shell command.
func Detect(command string) string {
	f := strings.Fields(command)
	for i, w := range f {
		base := filepath.Base(strings.Trim(w, `"'`))
		next := ""
		if i+1 < len(f) {
			next = f[i+1]
		}
		switch base {
		case "gradlew", "gradle", "gradlew.bat":
			return ToolGradle
		case "go":
			if next == "test" || next == "vet" || next == "build" {
				return ToolGo
			}
		case "jest":
			return ToolJest
		case "vitest":
			return ToolVitest
		case "pytest", "py.test":
			return ToolPytest
		case "maestro":
			return ToolMaestro
		case "python", "python3":
			if next == "-m" && i+2 < len(f) && f[i+2] == "pytest" {
				return ToolPytest
			}
		}
	}
	for i, w := range f {
		base := filepath.Base(w)
		if (base == "npm" || base == "yarn" || base == "pnpm" || base == "bun" || base == "npx") && i+1 < len(f) {
			rest := strings.Join(f[i+1:], " ")
			switch {
			case strings.Contains(rest, "vitest"):
				return ToolVitest
			case strings.Contains(rest, "jest"):
				return ToolJest
			case strings.Contains(rest, "test"):
				return ToolJS
			}
		}
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

type toolRules struct {
	fail    []rule // used when exit != 0
	success []rule // used when exit == 0 (and as summary lines on failure)
	// collapse counts lines matching the pattern instead of listing them all.
	collapse      *regexp.Regexp
	collapseLabel string
}

// apply returns the failure lines and the summary lines.
func (t toolRules) apply(lines []string, exit int) (fail, summary []string) {
	var picked []string
	seen := map[int]bool{}
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
	if exit != 0 {
		run(t.fail)
	}
	fail, picked = picked, nil
	collapsed := 0
	if t.collapse != nil {
		for i, l := range lines {
			if t.collapse.MatchString(l) && !seen[i] {
				collapsed++
				seen[i] = true
			}
		}
	}
	run(t.success)
	if collapsed > 0 {
		picked = append(picked, fmt.Sprintf("%d %s", collapsed, t.collapseLabel))
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
		collapse:      regexp.MustCompile(`^ok\s+\S+`),
		collapseLabel: "packages ok",
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
