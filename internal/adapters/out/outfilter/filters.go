package outfilter

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
)

// Rules of the build tools, linters and installers added after the first
// eight: errors and summaries are kept, progress is counted.
func init() {
	rules[ToolCargo] = toolRules{
		fail: []rule{
			{re: regexp.MustCompile(`^error(\[E\d+\])?: `), after: 14},
			{re: regexp.MustCompile(`^test \S+ \.\.\. FAILED$`)},
			{re: regexp.MustCompile(`^---- \S+ stdout ----$`), after: 10},
			{re: regexp.MustCompile(`^thread '.*' panicked at`), after: 3},
			{re: regexp.MustCompile(`^failures:$`), after: 20, cont: indented},
		},
		success: []rule{
			{re: regexp.MustCompile(`^test result: `)},
			{re: regexp.MustCompile(`^\s*Finished `)},
			{re: regexp.MustCompile(`^warning: .* generated \d+ warnings?`)},
		},
		collapses: []collapse{
			{regexp.MustCompile(`^\s*(Compiling|Checking|Downloaded|Downloading|Fresh|Blocking|Updating|Locking|Adding|Installing|Running) `), "build progress lines"},
			{regexp.MustCompile(`^test \S+ \.\.\. ok$`), "tests ok"},
		},
		dedupe: true,
	}
	rules[ToolMaven] = toolRules{
		fail: []rule{
			{re: regexp.MustCompile(`<<< (FAILURE|ERROR)!`), after: 4, cont: indented},
			{re: regexp.MustCompile(`^\[ERROR\] `)},
			{re: regexp.MustCompile(`^Tests run: .*(Failures: [1-9]|Errors: [1-9])`)},
		},
		success: []rule{
			{re: regexp.MustCompile(`^\[INFO\] BUILD (SUCCESS|FAILURE)`)},
			{re: regexp.MustCompile(`^\[(INFO|ERROR|WARNING)\] Tests run: \d+, Failures: \d+, Errors: \d+, Skipped: \d+$`)},
			{re: regexp.MustCompile(`^\[INFO\] Total time:`)},
		},
		collapses: []collapse{
			{regexp.MustCompile(`^(\[INFO\] )?(Download(ing|ed) from|Progress \()`), "download lines"},
			{regexp.MustCompile(`^\[WARNING\] `), "warning lines"},
		},
		skip:   regexp.MustCompile(`^\[ERROR\] (-> \[Help|Re-run Maven|For more information|To see the full stack|\[Help \d|$)|^\[ERROR\]\s*$`),
		dedupe: true,
	}
	rules[ToolMake] = toolRules{
		fail: []rule{
			{re: regexp.MustCompile(`make(\[\d+\])?: \*\*\* `)},
			{re: regexp.MustCompile(`^\S+:\d+(:\d+)?: (fatal )?error`), after: 3, cont: indented},
			{re: regexp.MustCompile(`^\s*--- FAIL: `), after: 10, cont: indented},
			{re: regexp.MustCompile(`^FAIL\s+\S+`)},
			{re: regexp.MustCompile(`^panic: `), after: 12, cont: anyLine},
			{re: regexp.MustCompile(`^\S+\.go:\d+(:\d+)?: `)},
			{re: regexp.MustCompile(`(?i)^(error|fatal)[:\s]`)},
		},
		collapses: []collapse{{regexp.MustCompile(`^ok\s+\S+`), "packages ok"}},
		dedupe:    true,
	}
	rules[ToolTSC] = toolRules{
		fail: []rule{
			{re: regexp.MustCompile(`^\S.*\(\d+,\d+\): error TS\d+: `), after: 3, cont: indented},
			{re: regexp.MustCompile(`^\S.*:\d+:\d+ - error TS\d+: `)},
			{re: regexp.MustCompile(`^error TS\d+: `)},
		},
		success: []rule{{re: regexp.MustCompile(`^Found \d+ errors?`)}},
		dedupe:  true,
	}
	rules[ToolLint] = toolRules{
		fail: []rule{
			{re: regexp.MustCompile(`^\S+\.go:\d+(:\d+)?: `)},
			{re: regexp.MustCompile(`^(level=error|ERRO|Error:) `)},
		},
		success: []rule{
			{re: regexp.MustCompile(`^\d+ issues?[.:]`)},
			{re: regexp.MustCompile(`^\* \S+: \d+$`)},
		},
		collapses: []collapse{{regexp.MustCompile(`^level=(info|warning) |^(INFO|WARN) `), "log lines"}},
		dedupe:    true,
	}
	rules[ToolInstall] = toolRules{
		fail: []rule{
			{re: regexp.MustCompile(`^npm (ERR!|error) `)},
			{re: regexp.MustCompile(`^\s*ERR_PNPM_|ERESOLVE`)},
			{re: regexp.MustCompile(`^error `)},
			{re: regexp.MustCompile(`^ERROR: `)},
			{re: regexp.MustCompile(`(?i)^\s*(fatal )?error:`)},
		},
		success: []rule{
			{re: regexp.MustCompile(`^(added|removed|changed) \d+ packages?|^up to date`)},
			{re: regexp.MustCompile(`vulnerabilit(y|ies)`)},
			{re: regexp.MustCompile(`^Packages: [+-]|^Done in |^Successfully installed |^Already up[- ]to[- ]date|^Lockfile is up to date|^Bundle complete|^Installed \d+ packages`)},
		},
		collapses: []collapse{
			{regexp.MustCompile(`(?i)deprecated`), "deprecation warnings"},
			{regexp.MustCompile(`^(Collecting|Downloading|Using cached|Requirement already satisfied|Installing collected|Fetching|Using|Resolving|Progress: resolved)\b`), "progress lines"},
			{regexp.MustCompile(`(?i)^(npm )?warn(ing)?\b`), "other warnings"},
		},
		dedupe: true,
	}
	rules[ToolDockerBuild] = toolRules{
		fail: []rule{
			{re: regexp.MustCompile(`^#\d+ .*ERROR`), after: 6},
			{re: regexp.MustCompile(`^#\d+ \d+\.\d+ .*(?i:error|err!|fatal|failed)`)},
			{re: regexp.MustCompile(`^ERROR:? |(?i)failed to solve|did not complete successfully|returned a non-zero code|^Error response from daemon`)},
			{re: regexp.MustCompile(`^\s*>>> `)},
		},
		success: []rule{
			{re: regexp.MustCompile(`naming to |^Successfully (built|tagged) |writing image sha256`)},
		},
		collapses: []collapse{{regexp.MustCompile(`^#\d+ |^Step \d+/\d+ |^ ---> `), "build step lines"}},
		dedupe:    true,
	}
}

// filterFunc is a filter that needs the structure of the output.
type filterFunc func(lines []string, exit int) (body, summary []string)

var funcs = map[string]filterFunc{
	ToolGitStatus: gitStatus,
	ToolGitDiff:   gitDiff,
	ToolGitLog:    gitLog,
	ToolGitSync:   gitSync,
	ToolESLint:    eslint,
	ToolFind:      findPaths,
	ToolGrep:      grepMatches,
	ToolKubectl:   kubectl,
	ToolLogs:      logLines,
}

// listCap shows the first n entries of a list and counts the rest.
func listCap(entries []string, n int) []string {
	if len(entries) <= n {
		return entries
	}
	return append(append([]string(nil), entries[:n]...), fmt.Sprintf("  … %d more", len(entries)-n))
}

var shortStatus = regexp.MustCompile(`^([ MADRCU?!T]{2}) (.+)$`)

// gitStatus groups the files of `git status` by state and caps each group.
func gitStatus(lines []string, exit int) ([]string, []string) {
	if exit != 0 {
		return nil, nil
	}
	var head []string
	groups := map[string][]string{}
	var order []string
	section := ""
	addTo := func(g, entry string) {
		if _, ok := groups[g]; !ok {
			order = append(order, g)
		}
		groups[g] = append(groups[g], entry)
	}
	for _, l := range lines {
		t := strings.TrimSpace(l)
		switch {
		case t == "":
		case strings.HasPrefix(t, "(use ") || strings.HasPrefix(t, "(commit or discard"):
		case t == "Changes to be committed:":
			section = "staged"
		case t == "Changes not staged for commit:":
			section = "not staged"
		case t == "Untracked files:":
			section = "untracked"
		case t == "Unmerged paths:":
			section = "conflicts"
		case section != "" && (strings.HasPrefix(l, "\t") || strings.HasPrefix(l, "  ")):
			addTo(section, t)
		case section == "" && shortStatus.MatchString(l):
			m := shortStatus.FindStringSubmatch(l)
			addTo(strings.TrimSpace(m[1]), m[2])
		default:
			section = ""
			head = append(head, t)
		}
	}
	body := head
	for _, g := range order {
		entries := groups[g]
		if g == "untracked" || g == "??" {
			entries = foldDirs(entries, 15)
		}
		body = append(body, fmt.Sprintf("%s (%d):", g, len(groups[g])))
		for _, e := range listCap(entries, 15) {
			if !strings.HasPrefix(e, "  ") {
				e = "  " + e
			}
			body = append(body, e)
		}
	}
	return body, nil
}

// foldDirs replaces long lists of paths by counts per top folder.
func foldDirs(paths []string, n int) []string {
	if len(paths) <= n {
		return paths
	}
	count := map[string]int{}
	var order []string
	for _, p := range paths {
		top := p
		if i := strings.IndexByte(strings.TrimSuffix(p, "/"), '/'); i >= 0 {
			top = p[:i+1]
		}
		if count[top] == 0 {
			order = append(order, top)
		}
		count[top]++
	}
	out := make([]string, 0, len(order))
	for _, t := range order {
		if count[t] > 1 {
			out = append(out, fmt.Sprintf("%s (%d files)", t, count[t]))
		} else {
			out = append(out, t)
		}
	}
	return out
}

// Lock files and generated files are counted, never shown, in a diff.
var generatedFile = regexp.MustCompile(`(^|/)(package-lock\.json|yarn\.lock|pnpm-lock\.yaml|go\.sum|Cargo\.lock|poetry\.lock|Gemfile\.lock|composer\.lock|bun\.lockb)$|\.min\.(js|css)$|\.(pb|gen)\.go$|_generated\.|\.snap$`)

type diffFile struct {
	name         string
	meta         []string
	hunks        []string
	added, del   int
	binary, skip bool
}

// gitDiff keeps the commit header, a stat of every file and the first lines
// of each file's changes; lock and generated files only appear in the stat.
func gitDiff(lines []string, exit int) ([]string, []string) {
	if exit != 0 {
		return nil, nil
	}
	var header []string
	var files []*diffFile
	var cur *diffFile
	for _, l := range lines {
		switch {
		case strings.HasPrefix(l, "diff --git "):
			name := l[len("diff --git "):]
			if i := strings.Index(name, " b/"); i >= 0 {
				name = name[i+3:]
			}
			cur = &diffFile{name: name, skip: generatedFile.MatchString(name)}
			files = append(files, cur)
		case cur == nil:
			if len(header) < 15 {
				header = append(header, l)
			}
		case strings.HasPrefix(l, "index ") || strings.HasPrefix(l, "--- ") || strings.HasPrefix(l, "+++ "):
		case strings.HasPrefix(l, "new file") || strings.HasPrefix(l, "deleted file") || strings.HasPrefix(l, "rename ") || strings.HasPrefix(l, "similarity "):
			cur.meta = append(cur.meta, l)
		case strings.HasPrefix(l, "Binary files "):
			cur.binary = true
		default:
			if strings.HasPrefix(l, "+") {
				cur.added++
			} else if strings.HasPrefix(l, "-") {
				cur.del++
			}
			cur.hunks = append(cur.hunks, l)
		}
	}
	if len(files) == 0 {
		return nil, nil
	}
	added, del := 0, 0
	for _, f := range files {
		added += f.added
		del += f.del
	}
	body := append([]string(nil), header...)
	body = append(body, fmt.Sprintf("%d files changed, +%d -%d", len(files), added, del))
	for _, f := range files {
		note := fmt.Sprintf("+%d -%d", f.added, f.del)
		switch {
		case f.binary:
			note = "binary"
		case f.skip:
			note += ", not shown"
		}
		body = append(body, fmt.Sprintf("  %s %s", f.name, note))
	}
	budget, perFile := 160, 40
	for _, f := range files {
		if f.skip || f.binary || len(f.hunks) == 0 {
			continue
		}
		if budget <= 0 {
			body = append(body, "… the remaining files' changes are in the full output")
			break
		}
		body = append(body, "--- "+f.name)
		body = append(body, f.meta...)
		n := min(perFile, budget, len(f.hunks))
		body = append(body, f.hunks[:n]...)
		if n < len(f.hunks) {
			body = append(body, fmt.Sprintf("… %d more lines of %s", len(f.hunks)-n, f.name))
		}
		budget -= n
	}
	return body, nil
}

// bigBodies are tools allowed a larger body than MaxLines: MaxLines would
// cut a diff in the middle, so the diff keeps its own budget.
var bigBodies = map[string]int{ToolGitDiff: 220}

var commitLine = regexp.MustCompile(`^commit ([0-9a-f]{7,40})`)

// gitLog turns `git log` into one line per commit.
func gitLog(lines []string, exit int) ([]string, []string) {
	if exit != 0 {
		return nil, nil
	}
	type commit struct{ hash, author, date, subject string }
	var cs []*commit
	var cur *commit
	for _, l := range lines {
		if m := commitLine.FindStringSubmatch(l); m != nil {
			cur = &commit{hash: m[1][:min(10, len(m[1]))]}
			cs = append(cs, cur)
			continue
		}
		if cur == nil {
			continue
		}
		t := strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(l, "Author: "):
			a := strings.TrimSpace(strings.TrimPrefix(l, "Author: "))
			if i := strings.Index(a, " <"); i >= 0 {
				a = a[:i]
			}
			cur.author = a
		case strings.HasPrefix(l, "Date: "):
			cur.date = strings.TrimSpace(strings.TrimPrefix(l, "Date: "))
		case cur.subject == "" && t != "" && strings.HasPrefix(l, "    "):
			cur.subject = t
		}
	}
	if len(cs) == 0 {
		return nil, nil
	}
	var body []string
	for _, c := range cs {
		body = append(body, strings.TrimSpace(fmt.Sprintf("%s %s (%s, %s)", c.hash, c.subject, c.author, c.date)))
	}
	if len(body) > 40 {
		body = append(body[:40], fmt.Sprintf("… %d more commits", len(cs)-40))
	}
	return body, nil
}

var gitProgress = regexp.MustCompile(`^(remote: )?(Enumerating|Counting|Compressing|Writing|Receiving|Resolving|Total|Delta compression|Unpacking|Updating files|Checking out files|Filtering content)\b`)

// gitSync drops the transfer progress of push, pull, fetch and clone.
func gitSync(lines []string, _ int) ([]string, []string) {
	var body []string
	dropped := 0
	for _, l := range lines {
		if gitProgress.MatchString(l) || strings.TrimSpace(strings.TrimPrefix(l, "remote:")) == "" {
			dropped++
			continue
		}
		body = append(body, l)
	}
	if dropped > 0 {
		body = append(body, fmt.Sprintf("(%d progress lines dropped)", dropped))
	}
	return body, nil
}

var (
	eslintProblem = regexp.MustCompile(`^\s+\d+:\d+\s+(error|warning)\s+(.*?)(\s{2,}(\S+))?$`)
	eslintTotal   = regexp.MustCompile(`^✖ \d+ problems?|^\d+ problems?`)
)

// eslint keeps every error under its file and counts warnings by rule.
func eslint(lines []string, _ int) ([]string, []string) {
	var body, summary []string
	file := ""
	fileShown := false
	warnByRule := map[string]int{}
	warnings := 0
	for _, l := range lines {
		switch {
		case eslintTotal.MatchString(strings.TrimSpace(l)):
			summary = append(summary, strings.TrimSpace(l))
		case eslintProblem.MatchString(l):
			m := eslintProblem.FindStringSubmatch(l)
			if m[1] == "warning" {
				warnings++
				warnByRule[def(m[4], "other")]++
				continue
			}
			if !fileShown && file != "" {
				body = append(body, file)
				fileShown = true
			}
			body = append(body, l)
		case strings.TrimSpace(l) != "" && !strings.HasPrefix(l, " "):
			file, fileShown = strings.TrimSpace(l), false
		}
	}
	if warnings > 0 {
		var parts []string
		for _, r := range sortedByCount(warnByRule) {
			parts = append(parts, fmt.Sprintf("%s ×%d", r, warnByRule[r]))
		}
		summary = append([]string{fmt.Sprintf("%d warnings: %s", warnings, strings.Join(listCap(parts, 8), ", "))}, summary...)
	}
	return body, summary
}

func def(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

func sortedByCount(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if m[keys[i]] != m[keys[j]] {
			return m[keys[i]] > m[keys[j]]
		}
		return keys[i] < keys[j]
	})
	return keys
}

var permissionDenied = regexp.MustCompile(`(?i)permission denied|operation not permitted`)

// findPaths shows the first paths and how many fall in each folder.
func findPaths(lines []string, _ int) ([]string, []string) {
	var paths []string
	denied := 0
	for _, l := range lines {
		if permissionDenied.MatchString(l) {
			denied++
			continue
		}
		if strings.TrimSpace(l) != "" {
			paths = append(paths, l)
		}
	}
	count := map[string]int{}
	for _, p := range paths {
		d := path.Dir(strings.TrimPrefix(p, "./"))
		if parts := strings.SplitN(d, "/", 3); len(parts) > 2 {
			d = parts[0] + "/" + parts[1]
		}
		count[d]++
	}
	body := []string{fmt.Sprintf("%d paths", len(paths))}
	body = append(body, listCap(paths, 25)...)
	if len(paths) > 25 && len(count) > 1 {
		body = append(body, "by folder:")
		for _, d := range listCap(sortedByCount(count), 12) {
			if n, ok := count[d]; ok {
				body = append(body, fmt.Sprintf("  %s %d", d, n))
			} else {
				body = append(body, d)
			}
		}
	}
	var summary []string
	if denied > 0 {
		summary = append(summary, fmt.Sprintf("%d permission denied lines dropped", denied))
	}
	return body, summary
}

var grepLine = regexp.MustCompile(`^([^:\s][^:]*?)[:-](\d+[:-])?(.*)$`)

// grepMatches groups matches by file: the count and the first three lines.
func grepMatches(lines []string, _ int) ([]string, []string) {
	type file struct {
		name  string
		lines []string
		n     int
	}
	var files []*file
	byName := map[string]*file{}
	binary, loose := 0, 0
	for _, l := range lines {
		if strings.HasPrefix(l, "Binary file ") || strings.HasPrefix(l, "grep: ") && strings.Contains(l, "binary file matches") {
			binary++
			continue
		}
		m := grepLine.FindStringSubmatch(l)
		if m == nil || strings.Contains(m[1], " ") && !strings.Contains(m[1], "/") {
			loose++
			continue
		}
		f := byName[m[1]]
		if f == nil {
			f = &file{name: m[1]}
			byName[m[1]] = f
			files = append(files, f)
		}
		f.n++
		if len(f.lines) < 3 {
			text := strings.TrimSpace(m[3])
			if len(text) > 160 {
				text = text[:160] + "…"
			}
			f.lines = append(f.lines, "  "+strings.TrimRight(m[2], ":-")+": "+text)
		}
	}
	if len(files) == 0 || loose > len(lines)/2 {
		return nil, nil // one file's lines without names: keep the head
	}
	total := 0
	for _, f := range files {
		total += f.n
	}
	body := []string{fmt.Sprintf("%d matches in %d files", total, len(files))}
	// Every file gets its name and count while they fit; the first files
	// also get their first lines.
	for i, f := range files {
		if len(body) >= MaxLines-4 {
			body = append(body, fmt.Sprintf("… %d more files", len(files)-i))
			break
		}
		body = append(body, fmt.Sprintf("%s (%d)", f.name, f.n))
		if len(body)+len(f.lines) < MaxLines/2 {
			body = append(body, f.lines...)
		}
	}
	var summary []string
	if binary > 0 {
		summary = append(summary, fmt.Sprintf("%d binary files matched", binary))
	}
	return body, summary
}

var (
	kubeField    = regexp.MustCompile(`^\s*(Name|Namespace|Status|Reason|Message|Ready|Restart Count|State|Last State|Exit Code|Image|Node|Started|Finished|Type|Replicas):`)
	kubeProblem  = regexp.MustCompile(`(?i)error|forbidden|not found|denied|invalid|unable|failed|backoff|oomkilled|crashloop`)
	kubeUnchange = regexp.MustCompile(`\b(unchanged|configured|created)$`)
)

// kubectl keeps the status fields and the last events of describe, counts
// unchanged objects of apply, and keeps problem lines.
func kubectl(lines []string, exit int) ([]string, []string) {
	var body, summary []string
	events := -1
	counts := map[string]int{}
	for i, l := range lines {
		switch {
		case strings.HasPrefix(l, "Events:"):
			events = i
		case events >= 0:
		case kubeField.MatchString(l):
			body = append(body, l)
		case kubeUnchange.MatchString(l):
			m := kubeUnchange.FindStringSubmatch(l)
			counts[m[1]]++
			if m[1] != "unchanged" {
				body = append(body, l)
			}
		case kubeProblem.MatchString(l):
			body = append(body, l)
		}
	}
	if events >= 0 {
		ev := lines[events:]
		body = append(body, tail(ev, 15)...)
	}
	for _, k := range sortedByCount(counts) {
		summary = append(summary, fmt.Sprintf("%d %s", counts[k], k))
	}
	if exit != 0 && len(body) == 0 {
		return nil, nil
	}
	return dedupeAll(body), summary
}

var logProblem = regexp.MustCompile(`(?i)\b(error|err|fatal|panic|exception|fail(ed|ure)?|traceback|warn(ing)?)\b`)

// logLines keeps the problem lines (deduplicated) and the last lines.
func logLines(lines []string, _ int) ([]string, []string) {
	var problems []string
	for _, l := range lines {
		if logProblem.MatchString(l) {
			problems = append(problems, l)
		}
	}
	problems = dedupeAll(problems)
	if len(problems) > 20 {
		problems = append(problems[:20], fmt.Sprintf("… %d more problem lines", len(problems)-20))
	}
	body := problems
	if len(body) > 0 {
		body = append(body, "last lines:")
	}
	return append(body, tail(collapseRuns(lines), 20)...), nil
}
