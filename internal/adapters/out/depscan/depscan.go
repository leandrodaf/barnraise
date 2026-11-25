// Package depscan is the built-in dependencies check: when a change adds
// or updates dependencies, it looks them up in the OSV database of known
// vulnerabilities and fails on the new ones at or above the project's
// severity (ADR-0034). It runs osv-scanner when it is installed and
// otherwise reads the lockfiles itself and asks the OSV API. The daemon
// runs it with its own network, never an agent.
package depscan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// Options configures the scanner.
type Options struct {
	// Exec runs git and osv-scanner.
	Exec ports.Exec
	// HTTP asks the OSV API (default: 30s timeout).
	HTTP *http.Client
	// LookPath finds osv-scanner (default exec.LookPath).
	LookPath func(string) (string, error)
	// MainRefs are the refs tried for main when a check comes without its
	// base commit, best first.
	MainRefs []string
}

// Scanner runs the dependencies check.
type Scanner struct{ o Options }

// New returns a scanner.
func New(o Options) *Scanner {
	if o.HTTP == nil {
		o.HTTP = &http.Client{Timeout: 30 * time.Second}
	}
	if o.LookPath == nil {
		o.LookPath = exec.LookPath
	}
	return &Scanner{o: o}
}

// Finding is one vulnerability (with its aliases) of one package.
type Finding struct {
	File     string   `json:"file"`
	Package  Package  `json:"package"`
	IDs      []string `json:"ids"` // the advisory and its aliases, the main id first
	Severity string   `json:"severity,omitempty"`
	Score    float64  `json:"score,omitempty"`
	Summary  string   `json:"summary,omitempty"`
	Fixed    []string `json:"fixed,omitempty"`
}

func (f Finding) vulnKey() string { return f.Package.nameKey() }

// Report is what one run found.
type Report struct {
	Scanner   string    // osv-scanner or the OSV API
	Files     []string  // the lockfiles compared
	Checked   int       // packages added or updated
	New       []Finding // vulnerabilities the change brings in
	Ignored   []Finding // …that the owner accepted (dependencies.ignore)
	Failing   []Finding // …at or above fail_at
	BaseKnown bool
}

// Check runs the check for a change. A failure to reach OSV or to run
// osv-scanner is not the item's fault: the result is red with MainRed set,
// so the item waits for the owner instead of going back to an agent.
func (s *Scanner) Check(ctx context.Context, spec ports.CheckSpec, pol domain.DependencyPolicy) (domain.CheckResult, error) {
	res := domain.CheckResult{}
	start := time.Now()
	rep, err := s.Scan(ctx, spec, pol)
	res.Duration = time.Since(start)
	if err != nil {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		res.MainRed, res.CannotRun = true, true
		res.Summary = "could not check the dependencies (this is not the change's fault): " + err.Error()
		return res, nil
	}
	res.Passed = len(rep.Failing) == 0
	res.Summary = rep.Text(pol)
	return res, nil
}

// Scan compares the lockfiles a change touches with their state at the
// change's base and returns the vulnerabilities it brings in.
func (s *Scanner) Scan(ctx context.Context, spec ports.CheckSpec, pol domain.DependencyPolicy) (Report, error) {
	dir := spec.Dir
	base := spec.Base
	if base == "" {
		base = s.mergeBase(ctx, dir)
	}
	rep := Report{BaseKnown: base != ""}
	touched := spec.Touched
	if len(touched) == 0 && base != "" {
		touched = s.diff(ctx, dir, base)
	}
	targets := s.targets(ctx, dir, base, touched)
	rep.Files = targets
	if len(targets) == 0 {
		return rep, nil
	}
	scanner := pol.Scanner
	bin := ""
	if scanner == "" || scanner == "auto" || scanner == "osv-scanner" {
		if p, err := s.o.LookPath("osv-scanner"); err == nil {
			bin = p
		} else if scanner == "osv-scanner" {
			return rep, errors.New("dependencies.scanner is osv-scanner, but osv-scanner is not installed on this machine")
		}
	}
	var findings []Finding
	var err error
	if bin != "" {
		rep.Scanner = "osv-scanner"
		findings, rep.Checked, err = s.withScanner(ctx, bin, dir, base, targets, spec.Def.Timeout)
	} else {
		rep.Scanner = "the OSV API"
		api := pol.API
		if api == "" {
			api = DefaultAPI
		}
		c := &osvClient{base: strings.TrimRight(api, "/"), http: s.o.HTTP, cache: map[string]Vuln{}}
		findings, rep.Checked, err = s.native(ctx, c, dir, base, targets)
	}
	if err != nil {
		return rep, err
	}
	for _, f := range findings {
		switch {
		case pol.Ignored(f.IDs...):
			rep.Ignored = append(rep.Ignored, f)
		case pol.Fails(f.Severity):
			rep.New = append(rep.New, f)
			rep.Failing = append(rep.Failing, f)
		default:
			rep.New = append(rep.New, f)
		}
	}
	return rep, nil
}

// targets are the lockfiles to compare for the touched files.
func (s *Scanner) targets(ctx context.Context, dir, base string, touched []string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(f string) {
		if !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	exists := func(f string) bool {
		if st, err := os.Stat(filepath.Join(dir, filepath.FromSlash(f))); err == nil && !st.IsDir() {
			return true
		}
		return false
	}
	for _, f := range touched {
		f = filepath.ToSlash(f)
		if !domain.IsDependencyManifest(f) {
			continue
		}
		name := path.Base(f)
		if parserFor(f) != nil {
			if exists(f) {
				add(f)
			}
			continue
		}
		locks, ok := lockfilesOf[name]
		if !ok {
			continue
		}
		for d := path.Dir(f); ; d = path.Dir(d) {
			found := false
			for _, l := range locks {
				cand := path.Join(d, l)
				if exists(cand) {
					add(cand)
					found = true
				}
			}
			if found || d == "." || d == "/" {
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// native reads the lockfiles itself and asks the OSV API.
func (s *Scanner) native(ctx context.Context, c *osvClient, dir, base string, targets []string) ([]Finding, int, error) {
	var headPkgs, basePkgs []Package
	fileOf := map[string]string{} // head package key -> lockfile
	for _, t := range targets {
		parse := parserFor(t)
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(t)))
		if err != nil {
			continue
		}
		head := parse(data)
		var old []Package
		if base != "" {
			if b, ok := s.show(ctx, dir, base, t); ok {
				old = parse(b)
			}
		}
		had := map[string]bool{}
		oldByName := map[string][]Package{}
		for _, p := range old {
			had[p.key()] = true
			oldByName[p.nameKey()] = append(oldByName[p.nameKey()], p)
		}
		for _, p := range head {
			if had[p.key()] {
				continue
			}
			if _, dup := fileOf[p.key()]; dup {
				continue
			}
			fileOf[p.key()] = t
			headPkgs = append(headPkgs, p)
			basePkgs = append(basePkgs, oldByName[p.nameKey()]...)
		}
	}
	if len(headPkgs) == 0 {
		return nil, 0, nil
	}
	headIDs, err := c.affecting(ctx, headPkgs)
	if err != nil {
		return nil, len(headPkgs), err
	}
	// What the same packages already had before the change is not new: an
	// update that keeps a known hole open is reported by the check of the
	// change that opened it.
	known := map[string]bool{}
	if len(basePkgs) > 0 {
		baseIDs, err := c.affecting(ctx, sortPackages(basePkgs))
		if err != nil {
			return nil, len(headPkgs), err
		}
		for _, p := range basePkgs {
			for _, id := range baseIDs[p.key()] {
				v, err := c.vuln(ctx, id)
				if err != nil {
					return nil, len(headPkgs), err
				}
				for _, a := range append([]string{v.ID}, v.Aliases...) {
					known[p.nameKey()+"\x00"+a] = true
				}
			}
		}
	}
	var out []Finding
	for _, p := range headPkgs {
		ids := headIDs[p.key()]
		if len(ids) == 0 {
			continue
		}
		var vulns []Vuln
		for _, id := range ids {
			v, err := c.vuln(ctx, id)
			if err != nil {
				return nil, len(headPkgs), err
			}
			vulns = append(vulns, v)
		}
		for _, f := range group(fileOf[p.key()], p, vulns, nil) {
			if !f.knownIn(known) {
				out = append(out, f)
			}
		}
	}
	sortFindings(out)
	return out, len(headPkgs), nil
}

func (f Finding) knownIn(known map[string]bool) bool {
	for _, id := range f.IDs {
		if known[f.vulnKey()+"\x00"+id] {
			return true
		}
	}
	return false
}

// group merges the records of one package that are aliases of each other
// (a GO- entry and its GHSA) into one finding with the highest severity.
// scores are severities given by the scanner per group id, when known.
func group(file string, p Package, vulns []Vuln, scores map[string]float64) []Finding {
	parent := make([]int, len(vulns))
	for i := range parent {
		parent[i] = i
	}
	find := func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	owner := map[string]int{}
	for i, v := range vulns {
		for _, id := range append([]string{v.ID}, v.Aliases...) {
			if j, ok := owner[id]; ok {
				parent[find(i)] = find(j)
			} else {
				owner[id] = i
			}
		}
	}
	byRoot := map[int]*Finding{}
	var roots []int
	for i, v := range vulns {
		r := find(i)
		f := byRoot[r]
		if f == nil {
			f = &Finding{File: file, Package: p}
			byRoot[r] = f
			roots = append(roots, r)
		}
		for _, id := range append([]string{v.ID}, v.Aliases...) {
			if !domain.Contains(f.IDs, id) {
				f.IDs = append(f.IDs, id)
			}
		}
		sev, score := v.rating(p)
		if sc := scores[v.ID]; sc > score {
			score = sc
			if s := domain.SeverityOfScore(sc); domain.SeverityRank(s) > domain.SeverityRank(sev) {
				sev = s
			}
		}
		if domain.SeverityRank(sev) > domain.SeverityRank(f.Severity) {
			f.Severity = sev
		}
		f.Score = max(f.Score, score)
		if f.Summary == "" {
			f.Summary = strings.TrimSpace(v.Summary)
		}
		for _, x := range v.fixedIn(p) {
			if !domain.Contains(f.Fixed, x) {
				f.Fixed = append(f.Fixed, x)
			}
		}
	}
	out := make([]Finding, 0, len(roots))
	for _, r := range roots {
		f := *byRoot[r]
		sort.SliceStable(f.IDs, func(i, j int) bool { return idRank(f.IDs[i]) < idRank(f.IDs[j]) })
		out = append(out, f)
	}
	return out
}

// idRank puts the most useful id first: GHSA, then the ecosystem's own,
// then CVE.
func idRank(id string) int {
	switch {
	case strings.HasPrefix(id, "GHSA-"):
		return 0
	case strings.HasPrefix(id, "CVE-"):
		return 2
	}
	return 1
}

func sortFindings(fs []Finding) {
	sort.SliceStable(fs, func(i, j int) bool {
		a, b := fs[i], fs[j]
		if ra, rb := domain.SeverityRank(a.Severity), domain.SeverityRank(b.Severity); ra != rb {
			return ra > rb
		}
		if a.Package.Name != b.Package.Name {
			return a.Package.Name < b.Package.Name
		}
		return a.IDs[0] < b.IDs[0]
	})
}

// scannerOutput is the part of osv-scanner's JSON output the check reads.
type scannerOutput struct {
	Results []struct {
		Source struct {
			Path string `json:"path"`
		} `json:"source"`
		Packages []struct {
			Package struct {
				Name      string `json:"name"`
				Version   string `json:"version"`
				Ecosystem string `json:"ecosystem"`
			} `json:"package"`
			Vulnerabilities []Vuln `json:"vulnerabilities"`
			Groups          []struct {
				IDs         []string `json:"ids"`
				Aliases     []string `json:"aliases"`
				MaxSeverity string   `json:"max_severity"`
			} `json:"groups"`
		} `json:"packages"`
	} `json:"results"`
}

// withScanner runs osv-scanner on each lockfile as it is now and as it was
// at the base, and keeps what only the new one has.
func (s *Scanner) withScanner(ctx context.Context, bin, dir, base string, targets []string, timeout time.Duration) ([]Finding, int, error) {
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	tmp, err := os.MkdirTemp("", "barnraise-deps-")
	if err != nil {
		return nil, 0, err
	}
	defer os.RemoveAll(tmp)
	var out []Finding
	checked := 0
	for i, t := range targets {
		head, n, err := s.runScanner(ctx, bin, dir, filepath.Join(dir, filepath.FromSlash(t)), t, timeout)
		if err != nil {
			return nil, checked, err
		}
		checked += n
		known := map[string]bool{}
		if base != "" {
			if data, ok := s.show(ctx, dir, base, t); ok {
				d := filepath.Join(tmp, fmt.Sprint(i))
				if err := os.MkdirAll(d, 0o700); err != nil {
					return nil, checked, err
				}
				p := filepath.Join(d, path.Base(t))
				if err := os.WriteFile(p, data, 0o600); err != nil {
					return nil, checked, err
				}
				old, _, err := s.runScanner(ctx, bin, d, p, t, timeout)
				if err != nil {
					return nil, checked, err
				}
				for _, f := range old {
					for _, id := range f.IDs {
						known[f.vulnKey()+"\x00"+id] = true
					}
				}
			}
		}
		for _, f := range head {
			if !f.knownIn(known) {
				out = append(out, f)
			}
		}
	}
	sortFindings(out)
	return out, checked, nil
}

func (s *Scanner) runScanner(ctx context.Context, bin, dir, file, shown string, timeout time.Duration) ([]Finding, int, error) {
	r, err := s.o.Exec.Run(ctx, ports.Cmd{Name: bin, Args: []string{"--format=json", "--lockfile=" + file}, Dir: dir, Env: os.Environ(), Timeout: timeout})
	if err != nil {
		return nil, 0, fmt.Errorf("osv-scanner on %s: %w", shown, err)
	}
	switch r.ExitCode {
	case 0, 1: // no vulnerabilities, vulnerabilities found
	case 128: // no packages found
		return nil, 0, nil
	default:
		return nil, 0, fmt.Errorf("osv-scanner on %s exited with %d: %s", shown, r.ExitCode, firstLine(r.Stderr))
	}
	var so scannerOutput
	if err := json.Unmarshal(r.Stdout, &so); err != nil {
		return nil, 0, fmt.Errorf("osv-scanner on %s printed no readable JSON: %w", shown, err)
	}
	var out []Finding
	n := 0
	for _, res := range so.Results {
		for _, pk := range res.Packages {
			n++
			p := Package{Ecosystem: pk.Package.Ecosystem, Name: pk.Package.Name, Version: pk.Package.Version}
			scores := map[string]float64{}
			for _, g := range pk.Groups {
				for _, id := range g.IDs {
					scores[id] = parseScore(g.MaxSeverity)
				}
			}
			out = append(out, group(shown, p, pk.Vulnerabilities, scores)...)
		}
	}
	return out, n, nil
}

func firstLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}

// git runs git in dir.
func (s *Scanner) git(ctx context.Context, dir string, args ...string) ([]byte, bool) {
	r, err := s.o.Exec.Run(ctx, ports.Cmd{Name: "git", Args: args, Dir: dir, Timeout: time.Minute})
	if err != nil || r.ExitCode != 0 {
		return nil, false
	}
	return r.Stdout, true
}

func (s *Scanner) mergeBase(ctx context.Context, dir string) string {
	for _, ref := range s.o.MainRefs {
		if out, ok := s.git(ctx, dir, "merge-base", "HEAD", ref); ok {
			if b := strings.TrimSpace(string(out)); b != "" {
				return b
			}
		}
	}
	return ""
}

func (s *Scanner) diff(ctx context.Context, dir, base string) []string {
	out, ok := s.git(ctx, dir, "diff", "--name-only", base)
	if !ok {
		return nil
	}
	var files []string
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			files = append(files, l)
		}
	}
	return files
}

func (s *Scanner) show(ctx context.Context, dir, base, file string) ([]byte, bool) {
	return s.git(ctx, dir, "show", base+":"+file)
}

// Text is the summary for the agent and the timeline.
func (r Report) Text(pol domain.DependencyPolicy) string {
	failAt := pol.FailAt
	if failAt == "" {
		failAt = "high"
	}
	if len(r.Files) == 0 {
		return "no dependency lockfile changed"
	}
	files := strings.Join(r.Files, ", ")
	if len(r.New) == 0 && len(r.Ignored) == 0 {
		if r.Checked == 0 && r.Scanner != "osv-scanner" {
			return fmt.Sprintf("no dependency was added or updated in %s", files)
		}
		return fmt.Sprintf("no new known vulnerability in the dependencies of %s (%s)", files, r.Scanner)
	}
	var b strings.Builder
	if len(r.Failing) > 0 {
		fmt.Fprintf(&b, "%d known %s at or above %s in the dependencies this change adds or updates (%s):\n",
			len(r.Failing), plural(len(r.Failing), "vulnerability", "vulnerabilities"), failAt, r.Scanner)
		for _, f := range r.Failing {
			b.WriteString("- " + f.line() + "\n")
		}
		b.WriteString("Move each one to a version that fixes it (or drop the package). If the owner accepts the risk, the id goes in dependencies.ignore in barnraise.yaml.\n")
	} else {
		fmt.Fprintf(&b, "no new known vulnerability at or above %s in the dependencies of %s (%s)\n", failAt, files, r.Scanner)
	}
	var below []Finding
	for _, f := range r.New {
		if !pol.Fails(f.Severity) {
			below = append(below, f)
		}
	}
	if len(below) > 0 {
		fmt.Fprintf(&b, "Below %s, not failing:\n", failAt)
		for _, f := range below {
			b.WriteString("- " + f.line() + "\n")
		}
	}
	if len(r.Ignored) > 0 {
		b.WriteString("Accepted in dependencies.ignore: ")
		var ids []string
		for _, f := range r.Ignored {
			ids = append(ids, f.Package.Name+" "+f.IDs[0])
		}
		b.WriteString(strings.Join(ids, ", ") + "\n")
	}
	return strings.TrimSpace(b.String())
}

func (f Finding) line() string {
	sev := f.Severity
	if sev == "" {
		sev = "unrated"
	}
	if f.Score > 0 {
		sev += fmt.Sprintf(" %.1f", f.Score)
	}
	s := fmt.Sprintf("%s: %s %s: %s (%s)", f.File, f.Package.Name, f.Package.Version, strings.Join(f.IDs, ", "), sev)
	if f.Summary != "" {
		s += ": " + f.Summary
	}
	if len(f.Fixed) > 0 {
		s += ". Fixed in " + strings.Join(f.Fixed, ", ")
	}
	return s
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
