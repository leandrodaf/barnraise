package app

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// What only this machine can see — its worktrees and the processes that run
// in them — is read here on a timer and told to the others as live state.
// Nothing of it is read when someone asks: an answer comes from memory.
//
// The cost is bounded on purpose:
//   - processes: one small read per process of the machine, at most every
//     procEvery, and less often when a read turns out slow. The working
//     directory, which says whose a process is, is read once per process.
//   - worktrees: a directory listing and two small files per worktree.
//   - sizes: one worktree at a time, in the background, yielding as it walks,
//     and again only for a worktree that was used since it was measured.
//
// Every map here is keyed by something that exists now (a live process, a
// directory on disk) and loses the key when that goes away.
var (
	reportBusyEvery = 30 * time.Second // a project with sessions or processes
	reportIdleEvery = 2 * time.Minute  // nothing runs: only listings change
	procEvery       = 15 * time.Second
	procEveryMax    = 2 * time.Minute
	procSlow        = 50 * time.Millisecond // a read above this doubles the interval
	sizeEvery       = 30 * time.Minute
	procTop         = 6
	// recheckScans: a process that was not ours is asked for its directory
	// again after this many reads (it may have moved into a worktree).
	recheckScans = 20
)

type procEntry struct {
	start   int64
	cpu     time.Duration
	owner   string // worktree directory; "" = not attributed
	checked int    // scan in which its directory was last read
	name    string
	command string
	kind    string
	seen    int
}

type procCost struct {
	cpu  time.Duration
	peak int64
}

type sizeEntry struct {
	bytes, files int64
	at           time.Time
	used         bool // in use or with processes since it was measured
	setup        bool
}

// localView is this machine's own inventory.
type localView struct {
	mu       sync.Mutex
	procs    map[int]*procEntry
	buf      []ports.Proc
	scan     int
	lastScan time.Time
	every    time.Duration
	took     time.Duration
	noProcs  bool                           // the platform does not list processes
	byDir    map[string]*domain.ProcSummary // latest sample, by worktree directory
	cost     map[string]*procCost           // by worktree directory, while it exists
	sizes    map[string]*sizeEntry
	reported map[string]time.Time // project -> last report
	active   map[string]bool      // project -> had sessions or processes at the last report
	running  atomic.Bool
	sizing   atomic.Bool
}

func (l *localView) init() {
	if l.procs == nil {
		l.procs, l.byDir, l.cost = map[int]*procEntry{}, map[string]*domain.ProcSummary{}, map[string]*procCost{}
		l.sizes, l.reported, l.active = map[string]*sizeEntry{}, map[string]time.Time{}, map[string]bool{}
		l.every = procEvery
	}
}

// busyItems lists the worktree names of a project with a session running here.
func (r *Runtime) busyItems(project string) map[string]bool {
	busy := map[string]bool{}
	r.mu.Lock()
	for _, ls := range r.running {
		if ls.project == project {
			busy[ls.itemID] = true
		}
	}
	r.mu.Unlock()
	return busy
}

// reportLocal tells the other machines what this one keeps for the project,
// when it is time. It returns at once: the reading happens in the background
// and one reading runs at a time.
func (s *Service) reportLocal(p *Project, busy map[string]bool) {
	l := &s.local
	now := time.Now()
	l.mu.Lock()
	l.init()
	every := reportIdleEvery
	if len(busy) > 0 || l.active[p.Name] {
		every = reportBusyEvery
	}
	due := now.Sub(l.reported[p.Name]) >= every
	if due {
		l.reported[p.Name] = now
	}
	l.mu.Unlock()
	if !due {
		return
	}
	if !l.running.CompareAndSwap(false, true) {
		// Another project's reading is going on: this one is taken at the next tick.
		l.mu.Lock()
		delete(l.reported, p.Name)
		l.mu.Unlock()
		return
	}
	go func() {
		defer l.running.Store(false)
		rep := s.LocalReport(p, busy)
		s.emitEphemeral(p, domain.EvMachineWorktrees, s.d.Machine, "barnraise", domain.Payload{Worktrees: rep})
	}()
}

// LocalReport reads what this machine keeps for a project right now.
func (s *Service) LocalReport(p *Project, busy map[string]bool) *domain.WorktreeReport {
	l := &s.local
	l.mu.Lock()
	l.init()
	l.mu.Unlock()
	rep := &domain.WorktreeReport{Machine: s.d.Machine, Project: p.Name, At: s.d.Clock.Now().UTC()}
	rep.Worktrees = s.listWorktrees(p, busy)
	s.sampleProcs()
	if s.d.Host != nil {
		rep.DiskFreeBytes, rep.DiskTotalBytes = s.d.Host.Disk(s.d.DataDir)
	}
	var measure []string
	l.mu.Lock()
	rep.ProcsSupported = s.d.Host != nil && !l.noProcs
	rep.SampledAt, rep.SampleMS = l.lastScan.UTC(), float64(l.took.Microseconds())/1000
	active := len(busy) > 0
	for i := range rep.Worktrees {
		w := &rep.Worktrees[i]
		if sum := l.byDir[w.Path]; sum != nil {
			c := *sum
			w.Procs = &c
			active = true
		}
		if c := l.cost[w.Path]; c != nil && (c.cpu > 0 || c.peak > 0) {
			if w.Procs == nil {
				w.Procs = &domain.ProcSummary{}
			}
			w.Procs.CPUSeconds, w.Procs.PeakRSSBytes = c.cpu.Seconds(), c.peak
		}
		if w.Kind == domain.WorktreeRoot {
			continue // a person's checkout: its size is not ours to walk
		}
		se := l.sizes[w.Path]
		if se == nil {
			se = &sizeEntry{setup: w.SetupDone}
			l.sizes[w.Path] = se
			measure = append(measure, w.Path)
		} else {
			if w.InUse || (w.Procs != nil && w.Procs.Count > 0) {
				se.used = true
			}
			if se.setup != w.SetupDone || (se.used && time.Since(se.at) > sizeEvery) {
				se.setup = w.SetupDone
				measure = append(measure, w.Path)
			}
		}
		w.SizeBytes, w.Files, w.SizedAt = se.bytes, se.files, se.at
		rep.SizeBytes += se.bytes
	}
	l.active[p.Name] = active
	l.mu.Unlock()
	s.forgetGone(p, rep.Worktrees)
	if len(measure) > 0 {
		s.measureSizes(measure)
	}
	return rep
}

// forgetGone drops what is remembered about directories of the project that
// are no longer on disk.
func (s *Service) forgetGone(p *Project, list []domain.WorktreeInfo) {
	l := &s.local
	have := make(map[string]bool, len(list))
	for _, w := range list {
		have[w.Path] = true
	}
	under := []string{s.WorktreeDir(p.Name, "") + string(filepath.Separator), filepath.Join(s.d.DataDir, "repro", p.Name) + string(filepath.Separator)}
	mine := func(dir string) bool {
		for _, u := range under {
			if strings.HasPrefix(dir, u) {
				return true
			}
		}
		return false
	}
	l.mu.Lock()
	for dir := range l.sizes {
		if mine(dir) && !have[dir] {
			delete(l.sizes, dir)
		}
	}
	for dir := range l.cost {
		if mine(dir) && !have[dir] {
			delete(l.cost, dir)
		}
	}
	l.mu.Unlock()
}

// listWorktrees lists the project's directories on this machine: a listing
// and a few small reads, no process started.
func (s *Service) listWorktrees(p *Project, busy map[string]bool) []domain.WorktreeInfo {
	var out []domain.WorktreeInfo
	add := func(kind, name, itemID, dir string) {
		fi, err := os.Lstat(dir)
		if err != nil || !fi.IsDir() {
			return
		}
		w := domain.WorktreeInfo{Machine: s.d.Machine, Project: p.Name, Name: name, Kind: kind, ItemID: itemID, Path: dir, ModifiedAt: fi.ModTime().UTC(), InUse: itemID != "" && busy[itemID]}
		if kind != domain.WorktreeTrash {
			w.Branch, w.Head = gitHead(dir)
			_, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(setupMarker)))
			w.SetupDone = err == nil
		}
		out = append(out, w)
	}
	if root := p.Cfg().Root; root != "" {
		add(domain.WorktreeRoot, filepath.Base(root), "", root)
	}
	entries, _ := os.ReadDir(s.WorktreeDir(p.Name, ""))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		kind, itemID := worktreeKind(name)
		add(kind, name, itemID, s.WorktreeDir(p.Name, name))
	}
	repro := filepath.Join(s.d.DataDir, "repro", p.Name)
	entries, _ = os.ReadDir(repro)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		kind := domain.WorktreeRepro
		if ports.IsTrash(e.Name()) {
			kind = domain.WorktreeTrash
		}
		add(kind, e.Name(), "", filepath.Join(repro, e.Name()))
	}
	return out
}

// worktreeKind reads what a directory under worktrees/<project> is from its name.
func worktreeKind(name string) (kind, itemID string) {
	switch {
	case ports.IsTrash(name):
		return domain.WorktreeTrash, ""
	case strings.HasPrefix(name, "_refine-"):
		return domain.WorktreeRefine, strings.TrimPrefix(name, "_refine-")
	case strings.HasPrefix(name, "_"):
		return domain.WorktreeScratch, ""
	case strings.HasSuffix(name, "~b"):
		return domain.WorktreeAlt, strings.TrimSuffix(name, "~b")
	}
	if _, _, err := domain.ParseID(name); err != nil {
		return domain.WorktreeScratch, ""
	}
	return domain.WorktreeItem, name
}

// gitHead reads the branch and the commit checked out in dir from git's own
// files, without starting git. A layout it does not know gives empty values.
func gitHead(dir string) (branch, head string) {
	gitDir := filepath.Join(dir, ".git")
	if data, err := os.ReadFile(gitDir); err == nil {
		// A linked worktree: ".git" is a file that points at its git directory.
		rest, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:")
		if !ok {
			return "", ""
		}
		gitDir = strings.TrimSpace(rest)
		if !filepath.IsAbs(gitDir) {
			gitDir = filepath.Join(dir, gitDir)
		}
	}
	data, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return "", ""
	}
	ref, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "ref: ")
	if !ok {
		return "", strings.TrimSpace(string(data)) // detached
	}
	branch = strings.TrimPrefix(ref, "refs/heads/")
	common := gitDir
	if data, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
		common = strings.TrimSpace(string(data))
		if !filepath.IsAbs(common) {
			common = filepath.Join(gitDir, common)
		}
	}
	if data, err := os.ReadFile(filepath.Join(common, filepath.FromSlash(ref))); err == nil {
		return branch, strings.TrimSpace(string(data))
	}
	if data, err := os.ReadFile(filepath.Join(common, "packed-refs")); err == nil {
		for line := range strings.SplitSeq(string(data), "\n") {
			if sha, name, ok := strings.Cut(line, " "); ok && name == ref {
				return branch, sha
			}
		}
	}
	return branch, ""
}

// ownerDir returns the worktree directory that holds dir ("" when none
// does): <data>/worktrees/<project>/<name> or <data>/repro/<project>/<name>.
func (s *Service) ownerDir(dir string) string {
	for _, base := range []string{"worktrees", "repro"} {
		root := filepath.Join(s.d.DataDir, base) + string(filepath.Separator)
		rest, ok := strings.CutPrefix(dir, root)
		if !ok {
			continue
		}
		parts := strings.SplitN(rest, string(filepath.Separator), 3)
		if len(parts) < 2 || parts[1] == "" {
			return ""
		}
		return root + parts[0] + string(filepath.Separator) + parts[1]
	}
	return ""
}

// sampleProcs reads the machine's processes once, when the last read is old
// enough, and measures the ones that run for a worktree. A process belongs
// to the worktree its working directory is in, and so does everything it
// starts, wherever that goes (a browser a test opened).
func (s *Service) sampleProcs() {
	h := s.d.Host
	if h == nil {
		return
	}
	l := &s.local
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if l.noProcs || (!l.lastScan.IsZero() && now.Sub(l.lastScan) < l.every) {
		return
	}
	list, err := h.Processes(l.buf)
	l.buf = list[:0]
	if err != nil {
		l.noProcs = true // not on this platform: never asked again
		return
	}
	elapsed := now.Sub(l.lastScan)
	first := l.lastScan.IsZero()
	l.scan++
	byPID := make(map[int]*ports.Proc, len(list))
	for i := range list {
		pr := &list[i]
		byPID[pr.PID] = pr
		e := l.procs[pr.PID]
		if e == nil || e.start != pr.Start {
			e = &procEntry{start: pr.Start, name: pr.Name}
			if first || pr.StartedAt.Before(l.lastScan) {
				e.cpu = pr.CPU // older than the window: what it used before is not counted
			}
			l.procs[pr.PID] = e
		}
		e.seen = l.scan
	}
	for pid, e := range l.procs {
		if e.seen != l.scan {
			delete(l.procs, pid) // it ended
		}
	}
	// Whose is each process: its own directory first, then its parent's.
	var resolve func(pid, depth int) string
	resolve = func(pid, depth int) string {
		e := l.procs[pid]
		if e == nil || depth > 64 {
			return ""
		}
		if e.owner != "" {
			return e.owner
		}
		if e.checked == 0 || l.scan-e.checked >= recheckScans {
			e.checked = l.scan
			if cwd := h.Cwd(pid); cwd != "" {
				e.owner = s.ownerDir(cwd)
			}
		}
		if e.owner == "" {
			if pr := byPID[pid]; pr != nil && pr.PPID > 1 && pr.PPID != pid {
				e.owner = resolve(pr.PPID, depth+1)
			}
		}
		return e.owner
	}
	sums := map[string]*domain.ProcSummary{}
	for i := range list {
		pr := &list[i]
		owner := resolve(pr.PID, 0)
		if owner == "" {
			continue
		}
		e := l.procs[pr.PID]
		if e.command == "" {
			e.command = shortCommand(h.Cmdline(pr.PID), pr.Name)
			e.kind = procKind(pr.Name, e.command)
		}
		delta := max(pr.CPU-e.cpu, 0)
		e.cpu = pr.CPU
		info := domain.ProcInfo{PID: pr.PID, Name: pr.Name, Command: e.command, Kind: e.kind, RSSBytes: pr.RSS, CPUSeconds: pr.CPU.Seconds(), StartedAt: pr.StartedAt.UTC()}
		if !first && elapsed > 0 {
			info.CPUPct = float64(delta) / float64(elapsed) * 100
		}
		sum := sums[owner]
		if sum == nil {
			sum = &domain.ProcSummary{ByKind: map[string]int{}}
			sums[owner] = sum
		}
		sum.Count++
		sum.CPUPct += info.CPUPct
		sum.RSSBytes += info.RSSBytes
		sum.ByKind[info.Kind]++
		sum.Top = append(sum.Top, info)
		c := l.cost[owner]
		if c == nil {
			c = &procCost{}
			l.cost[owner] = c
		}
		if !first {
			c.cpu += delta
		}
	}
	for owner, sum := range sums {
		sort.Slice(sum.Top, func(i, j int) bool {
			a, b := sum.Top[i], sum.Top[j]
			if a.CPUPct != b.CPUPct {
				return a.CPUPct > b.CPUPct
			}
			if a.RSSBytes != b.RSSBytes {
				return a.RSSBytes > b.RSSBytes
			}
			return a.PID < b.PID
		})
		if len(sum.Top) > procTop {
			sum.Top = append([]domain.ProcInfo(nil), sum.Top[:procTop]...)
		}
		l.cost[owner].peak = max(l.cost[owner].peak, sum.RSSBytes)
	}
	l.byDir = sums
	l.lastScan = now
	l.took = time.Since(now)
	// A machine with many processes pays more for each read: read less often.
	switch {
	case l.took > procSlow && l.every < procEveryMax:
		l.every = min(l.every*2, procEveryMax)
	case l.took < procSlow/4 && l.every > procEvery:
		l.every = max(l.every/2, procEvery)
	}
}

// procMark starts measuring a session: it returns the processor time already
// counted for the worktree and starts a new memory peak.
func (s *Service) procMark(dir string) time.Duration {
	l := &s.local
	l.mu.Lock()
	defer l.mu.Unlock()
	l.init()
	c := l.cost[dir]
	if c == nil {
		return 0
	}
	c.peak = 0
	return c.cpu
}

// procSince is what the worktree's processes used since procMark.
func (s *Service) procSince(dir string, mark time.Duration) (cpuSeconds float64, peakRSS int64) {
	l := &s.local
	l.mu.Lock()
	defer l.mu.Unlock()
	c := l.cost[dir]
	if c == nil {
		return 0, 0
	}
	return max(c.cpu-mark, 0).Seconds(), c.peak
}

// shortCommand is the program and its first plain arguments. Options,
// anything with "=" and inline scripts are left out: that is where secrets
// travel.
func shortCommand(args []string, name string) string {
	if len(args) == 0 || args[0] == "" {
		return name
	}
	out := []string{filepath.Base(args[0])}
	for _, a := range args[1:] {
		if len(out) == 3 {
			break
		}
		if a == "" || strings.HasPrefix(a, "-") || strings.ContainsAny(a, "=@ \t\n") {
			continue // an option, an assignment, an address or a whole script
		}
		if strings.ContainsRune(a, filepath.Separator) {
			a = filepath.Base(a)
		}
		out = append(out, truncate(a, 40))
	}
	return strings.Join(out, " ")
}

var procKinds = []struct {
	kind  string
	words []string
}{
	{"agent", []string{"claude", "codex", "gemini"}},
	{"browser", []string{"chrome", "chromium", "firefox", "webkit", "headless_shell", "msedge", "safari"}},
	{"emulator", []string{"qemu", "emulator", "adb", "simulator", "crosvm"}},
	{"test", []string{"jest", "vitest", "pytest", "mocha", "playwright", "cypress", "maestro", "karma", "rspec", "phpunit", ".test", "go test"}},
	{"build", []string{"gradle", "kotlin", "javac", "tsc", "webpack", "esbuild", "rollup", "vite", "metro", "cargo", "rustc", "clang", "gcc", "cc1", "make", "ninja", "cmake", "xcodebuild", "compile", "link", "go build"}},
	{"container", []string{"docker", "podman", "containerd", "runc", "conmon"}},
	{"git", []string{"git"}},
	{"shell", []string{"bash", "zsh", "fish", "dash", "sh"}},
	{"runtime", []string{"node", "bun", "deno", "python", "java", "ruby", "php", "dotnet", "go", "npm", "pnpm", "yarn", "npx"}},
}

// procKind says what a process is for, from its name and command.
func procKind(name, command string) string {
	n, c := strings.ToLower(name), strings.ToLower(command)
	exact := func(w string) bool { return n == w || strings.HasPrefix(c, w+" ") || c == w }
	for _, k := range procKinds {
		for _, w := range k.words {
			switch k.kind {
			case "shell", "git", "runtime", "build":
				// Short words: only as the program itself, or the test binary suffix.
				if exact(w) || (len(w) > 4 && strings.Contains(c, w)) {
					return k.kind
				}
			default:
				if strings.Contains(n, w) || strings.Contains(c, w) {
					return k.kind
				}
			}
		}
	}
	return "other"
}

// measureSizes walks the given worktrees one after the other, in the
// background. One walk runs at a time; what does not fit now is asked again
// by the next report.
func (s *Service) measureSizes(dirs []string) {
	l := &s.local
	if !l.sizing.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer l.sizing.Store(false)
		for _, dir := range dirs {
			bytes, files, ok := dirSize(context.Background(), dir)
			l.mu.Lock()
			if se := l.sizes[dir]; se != nil && ok {
				se.bytes, se.files, se.at, se.used = bytes, files, time.Now().UTC(), false
			}
			l.mu.Unlock()
		}
	}()
}

// dirSize adds up the files below dir. It rests a moment every few thousand
// entries, so a tree of dependencies does not hold a processor or the disk.
func dirSize(ctx context.Context, dir string) (bytes, files int64, ok bool) {
	n := 0
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if n++; n%4096 == 0 {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			time.Sleep(sizeRest)
		}
		if d.Type().IsRegular() {
			if fi, err := d.Info(); err == nil {
				bytes += fi.Size()
				files++
			}
		}
		return nil
	})
	return bytes, files, err == nil
}

// sizeRest is the pause of a size walk every 4096 entries.
var sizeRest = 3 * time.Millisecond
