package app

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
	"github.com/leandrodaf/barnraise/internal/testkit"
)

// fakeHost is a machine whose processes the test decides.
type fakeHost struct {
	mu       sync.Mutex
	procs    []ports.Proc
	cwd      map[int]string
	args     map[int][]string
	cwdReads map[int]int
	lists    int
	err      error
}

func (h *fakeHost) Processes(buf []ports.Proc) ([]ports.Proc, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lists++
	if h.err != nil {
		return buf[:0], h.err
	}
	return append(buf[:0], h.procs...), nil
}

func (h *fakeHost) Cwd(pid int) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cwdReads == nil {
		h.cwdReads = map[int]int{}
	}
	h.cwdReads[pid]++
	return h.cwd[pid]
}

func (h *fakeHost) Cmdline(pid int) []string { return h.args[pid] }

func (h *fakeHost) Disk(string) (free, total int64) { return 40 << 30, 100 << 30 }

func (h *fakeHost) set(procs ...ports.Proc) {
	h.mu.Lock()
	h.procs = procs
	h.mu.Unlock()
}

// sampleAfter reads the processes as if d had passed since the last read.
func (e *env) sampleAfter(d time.Duration) {
	e.t.Helper()
	l := &e.svc.local
	l.mu.Lock()
	l.init()
	l.every = 0
	if !l.lastScan.IsZero() {
		l.lastScan = time.Now().Add(-d)
	}
	l.mu.Unlock()
	e.svc.sampleProcs()
}

func near(a, b float64) bool { return math.Abs(a-b) <= math.Max(0.02*math.Abs(b), 0.01) }

// A process belongs to the worktree it stands in, and so does everything it
// starts: the browser a test opened stands somewhere else.
func TestProcessesAreMeasuredByWorktree(t *testing.T) {
	e := newEnv(t)
	wt := e.svc.WorktreeDir("demo", "I-1")
	other := e.svc.WorktreeDir("demo", "I-2")
	started := time.Now().Add(-time.Hour)
	h := &fakeHost{
		cwd: map[int]string{100: filepath.Join(wt, "web"), 101: "/", 102: "/tmp", 200: "/home/ana", 300: other},
		args: map[int][]string{
			100: {"/usr/bin/claude", "--model", "big", "-p"},
			101: {"/opt/chrome/chrome", "--headless", "--remote-debugging-port=9222", "--user-data-dir=/tmp/x"},
			102: {"node", "/repo/node_modules/.bin/jest", "--token=SECRET", "cards.test.ts"},
			300: {"/usr/bin/gradle", "assembleDebug"},
		},
	}
	e.svc.d.Host = h
	mk := func(pid, ppid int, name string, cpu time.Duration, rss int64) ports.Proc {
		return ports.Proc{PID: pid, PPID: ppid, Name: name, Start: int64(pid), CPU: cpu, RSS: rss, StartedAt: started}
	}
	h.set(mk(100, 1, "claude", 5*time.Second, 300<<20), mk(101, 102, "chrome", 2*time.Second, 500<<20), mk(102, 100, "node", time.Second, 100<<20),
		mk(200, 1, "editor", time.Minute, 1<<30), mk(300, 1, "java", 10*time.Second, 2<<30))
	e.sampleAfter(0)
	sum := e.svc.local.byDir[wt]
	if sum == nil || sum.Count != 3 || sum.RSSBytes != 900<<20 || sum.CPUPct != 0 {
		t.Fatalf("the first read counts and weighs, without a rate yet: %+v", sum)
	}
	if sum.ByKind["agent"] != 1 || sum.ByKind["browser"] != 1 || sum.ByKind["test"] != 1 {
		t.Fatalf("kinds %v", sum.ByKind)
	}
	if got := e.svc.local.byDir[other]; got == nil || got.Count != 1 || got.ByKind["build"] != 1 {
		t.Fatalf("each worktree has its own processes: %+v", got)
	}
	for _, p := range sum.Top {
		if strings.Contains(p.Command, "SECRET") || strings.Contains(p.Command, "9222") || strings.Contains(p.Command, "--") {
			t.Fatalf("options never leave the machine: %q", p.Command)
		}
	}
	mark := e.svc.procMark(wt)

	// Ten seconds later: the agent used 2 s of processor, the test 5 s, the browser 1 s.
	h.set(mk(100, 1, "claude", 7*time.Second, 310<<20), mk(101, 102, "chrome", 3*time.Second, 700<<20), mk(102, 100, "node", 6*time.Second, 100<<20),
		mk(200, 1, "editor", 2*time.Minute, 1<<30), mk(300, 1, "java", 10*time.Second, 2<<30))
	e.sampleAfter(10 * time.Second)
	sum = e.svc.local.byDir[wt]
	if !near(sum.CPUPct, 80) || sum.RSSBytes != 1110<<20 {
		t.Fatalf("8 s of processor in 10 s is 80%%: %+v", sum)
	}
	if top := sum.Top[0]; top.PID != 102 || !near(top.CPUPct, 50) || top.Command != "node jest cards.test.ts" || top.Kind != "test" {
		t.Fatalf("the heaviest comes first: %+v", sum.Top)
	}
	cpu, peak := e.svc.procSince(wt, mark)
	if !near(cpu, 8) || peak != 1110<<20 {
		t.Fatalf("a session is charged what ran since it started: %v s, %d bytes", cpu, peak)
	}
	if got := e.svc.local.byDir[other]; !near(got.CPUPct, 0) {
		t.Fatalf("an idle process uses nothing: %+v", got)
	}
	if h.cwdReads[100] != 1 || h.cwdReads[300] != 1 {
		t.Fatalf("the directory of a process is read once: %v", h.cwdReads)
	}

	// The agent ends. What it started still belongs to the worktree; the
	// number 200 is now another program, in the worktree.
	h.cwd[200] = wt
	h.args[200] = []string{"bash"}
	reused := mk(200, 1, "bash", 0, 4<<20)
	reused.Start, reused.StartedAt = 999, time.Now()
	h.set(mk(101, 102, "chrome", 3*time.Second, 700<<20), mk(102, 1, "node", 6*time.Second, 100<<20), reused)
	e.sampleAfter(10 * time.Second)
	sum = e.svc.local.byDir[wt]
	if sum.Count != 3 || sum.ByKind["shell"] != 1 || sum.ByKind["agent"] != 0 {
		t.Fatalf("orphans stay and a reused number is a new process: %+v", sum)
	}
	if e.svc.local.byDir[other] != nil {
		t.Fatalf("nothing runs there any more: %+v", e.svc.local.byDir[other])
	}
	if n := len(e.svc.local.procs); n != 3 {
		t.Fatalf("processes that ended are forgotten: %d kept", n)
	}
	if cpu, _ := e.svc.procSince(wt, mark); !near(cpu, 8) {
		t.Fatalf("what ended keeps being counted for the worktree: %v", cpu)
	}
}

func TestProcessesAreReadOnATimer(t *testing.T) {
	e := newEnv(t)
	h := &fakeHost{}
	e.svc.d.Host = h
	e.svc.local.init()
	e.svc.sampleProcs()
	e.svc.sampleProcs()
	e.svc.sampleProcs()
	if h.lists != 1 {
		t.Fatalf("asked %d times within the interval, want 1", h.lists)
	}
	// A slow read makes the next ones rarer; a fast one brings the pace back.
	l := &e.svc.local
	old := procSlow
	t.Cleanup(func() { procSlow = old })
	procSlow = 0
	l.lastScan = time.Now().Add(-time.Hour)
	e.svc.sampleProcs()
	if l.every != 2*procEvery {
		t.Fatalf("interval after a slow read = %s", l.every)
	}
	procSlow = time.Hour
	l.lastScan = time.Now().Add(-time.Hour)
	e.svc.sampleProcs()
	if l.every != procEvery {
		t.Fatalf("interval after a fast read = %s", l.every)
	}

	// A platform that cannot list processes is asked once.
	e2 := newEnv(t)
	h2 := &fakeHost{err: ports.ErrNoProcesses}
	e2.svc.d.Host = h2
	rep := e2.svc.LocalReport(e2.p, nil)
	_ = e2.svc.LocalReport(e2.p, nil)
	if h2.lists != 1 || rep.ProcsSupported {
		t.Fatalf("asked %d times, supported=%v", h2.lists, rep.ProcsSupported)
	}
	if rep.DiskFreeBytes != 40<<30 || rep.DiskTotalBytes != 100<<30 {
		t.Fatalf("disk %+v", rep)
	}
}

func TestLocalReportListsWorktrees(t *testing.T) {
	e := newEnv(t, withGit())
	old := sizeRest
	t.Cleanup(func() { sizeRest = old })
	sizeRest = 0
	it := e.ready("simple", "cards", "cards")
	e.start(it.ID)
	dir := e.workspace(it.ID, map[string]string{"big.bin": strings.Repeat("x", 5000)})
	refine := e.scratch("_refine-I-77")
	scratch := e.scratch("_main-abc")
	if err := os.MkdirAll(e.svc.WorktreeDir("demo", ports.TrashPrefix+"I-9-zz"), 0o755); err != nil {
		t.Fatal(err)
	}
	head, err := e.p.Ports().Git.Head(e.ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	find := func(rep *domain.WorktreeReport, name string) *domain.WorktreeInfo {
		for i := range rep.Worktrees {
			if rep.Worktrees[i].Name == name {
				return &rep.Worktrees[i]
			}
		}
		return nil
	}
	rep := e.svc.LocalReport(e.p, map[string]bool{it.ID: true})
	if rep.Machine != "m1" || rep.Project != "demo" || rep.ProcsSupported {
		t.Fatalf("report %+v", rep)
	}
	w := find(rep, it.ID)
	if w == nil || w.Kind != domain.WorktreeItem || w.ItemID != it.ID || w.Path != dir || !w.InUse || w.Machine != "m1" {
		t.Fatalf("item worktree %+v", w)
	}
	if w.Branch != e.item(it.ID).Branch || w.Head != head {
		t.Fatalf("branch %q head %q, want %q %q", w.Branch, w.Head, e.item(it.ID).Branch, head)
	}
	if r := find(rep, "_refine-I-77"); r == nil || r.Kind != domain.WorktreeRefine || r.ItemID != "I-77" || r.Branch != "" || r.Head == "" || r.Path != refine {
		t.Fatalf("refine checkout (detached) %+v", r)
	}
	if s := find(rep, "_main-abc"); s == nil || s.Kind != domain.WorktreeScratch || s.Path != scratch {
		t.Fatalf("scratch %+v", s)
	}
	if tr := find(rep, ports.TrashPrefix+"I-9-zz"); tr == nil || tr.Kind != domain.WorktreeTrash {
		t.Fatalf("trash %+v", tr)
	}
	root := find(rep, filepath.Base(e.root))
	if root == nil || root.Kind != domain.WorktreeRoot || root.Head == "" {
		t.Fatalf("the project's own checkout %+v", root)
	}
	// Sizes come from the background, a report later.
	eventually(t, "the worktree to be measured", func() bool {
		w := find(e.svc.LocalReport(e.p, nil), it.ID)
		return w != nil && w.SizeBytes >= 5000 && w.Files > 0 && !w.SizedAt.IsZero()
	})
	rep = e.svc.LocalReport(e.p, nil)
	if rep.SizeBytes < 5000 || find(rep, filepath.Base(e.root)).SizeBytes != 0 {
		t.Fatalf("the total adds the worktrees and leaves the root out: %+v", rep)
	}
	// A measured worktree nobody used is not walked again.
	before := find(rep, it.ID).SizedAt
	testkit.WriteFile(t, dir, "more.bin", strings.Repeat("y", 9000))
	time.Sleep(20 * time.Millisecond)
	if again := find(e.svc.LocalReport(e.p, nil), it.ID); !again.SizedAt.Equal(before) {
		t.Fatalf("an idle worktree was measured again")
	}
	// What is gone from the disk is forgotten.
	e.svc.procMark(dir)
	e.svc.local.mu.Lock()
	e.svc.local.cost[dir] = &procCost{cpu: time.Second}
	e.svc.local.mu.Unlock()
	if err := ports.RemoveTree(dir); err != nil {
		t.Fatal(err)
	}
	rep = e.svc.LocalReport(e.p, nil)
	e.svc.local.mu.Lock()
	_, sized := e.svc.local.sizes[dir]
	_, cost := e.svc.local.cost[dir]
	e.svc.local.mu.Unlock()
	if find(rep, it.ID) != nil || sized || cost {
		t.Fatalf("a removed worktree is still remembered: sized=%v cost=%v", sized, cost)
	}
}

func TestReportLocalReachesOtherMachines(t *testing.T) {
	e := newEnv(t, withGit())
	it := e.ready("simple", "cards", "cards")
	e.start(it.ID)
	e.workspace(it.ID, nil)
	ch, cancel := e.svc.Hub().Subscribe("demo", []string{domain.EvMachineWorktrees}, 8)
	defer cancel()
	e.svc.reportLocal(e.p, nil)
	e.svc.reportLocal(e.p, nil) // not due again: nothing is read twice
	var ev domain.Event
	select {
	case ev = <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("no report was published")
	}
	select {
	case extra := <-ch:
		t.Fatalf("a second report within the interval: %+v", extra)
	case <-time.After(50 * time.Millisecond):
	}
	if len(e.store.Types("m1")) != 0 {
		t.Fatalf("the report is live state, never stored: %v", e.store.Types("m1"))
	}
	// Another machine learns it from the event alone.
	other := newEnv(t, withMachine("m2"))
	if err := other.svc.Ingest(other.ctx, ev); err != nil {
		t.Fatal(err)
	}
	got := other.p.Snapshot().Worktrees["m1"]
	if got == nil || len(got.Worktrees) < 2 {
		t.Fatalf("m2 does not know m1's worktrees: %+v", got)
	}
	if len(other.store.Events()) != 0 {
		t.Fatalf("m2 stored live state")
	}
}

func TestShortCommandAndKinds(t *testing.T) {
	for _, c := range []struct {
		args       []string
		name, want string
		kind       string
	}{
		{[]string{"/usr/bin/node", "/w/node_modules/.bin/vitest", "run", "--api-key=abc"}, "node", "node vitest run", "test"},
		{[]string{"go", "test", "./..."}, "go", "go test ...", "test"},
		{[]string{"/tmp/go-build1/b001/app.test", "-test.v"}, "app.test", "app.test", "test"},
		{[]string{"/usr/lib/chromium/chromium", "--type=renderer"}, "chromium", "chromium", "browser"},
		{[]string{"qemu-system-x86_64", "-avd", "Pixel"}, "qemu-system-x86", "qemu-system-x86_64 Pixel", "emulator"},
		{[]string{"/bin/sh", "-c", "curl -H 'Authorization: Bearer abc' host"}, "sh", "sh", "shell"},
		{[]string{"git", "push", "origin", "user@host:x"}, "git", "git push origin", "git"},
		{[]string{"docker", "run", "--rm", "img"}, "docker", "docker run img", "container"},
		{[]string{"python3", "server.py"}, "python3", "python3 server.py", "runtime"},
		{[]string{"/usr/bin/tsc", "--noEmit"}, "tsc", "tsc", "build"},
		{nil, "kworker", "kworker", "other"},
		{[]string{"/opt/thing"}, "thing", "thing", "other"},
	} {
		got := shortCommand(c.args, c.name)
		if got != c.want {
			t.Errorf("shortCommand(%v) = %q, want %q", c.args, got, c.want)
		}
		if k := procKind(c.name, got); k != c.kind {
			t.Errorf("procKind(%q, %q) = %q, want %q", c.name, got, k, c.kind)
		}
	}
}

func TestOwnerDirAndWorktreeKind(t *testing.T) {
	e := newEnv(t)
	data := e.svc.d.DataDir
	for dir, want := range map[string]string{
		filepath.Join(data, "worktrees", "demo", "I-1", "web", "src"): filepath.Join(data, "worktrees", "demo", "I-1"),
		filepath.Join(data, "worktrees", "demo", "I-1"):               filepath.Join(data, "worktrees", "demo", "I-1"),
		filepath.Join(data, "repro", "demo", "r1", "x"):               filepath.Join(data, "repro", "demo", "r1"),
		filepath.Join(data, "worktrees", "demo"):                      "",
		filepath.Join(data, "worktrees"):                              "",
		filepath.Join(data, "logs", "x"):                              "",
		"/home/ana/project":                                           "",
	} {
		if got := e.svc.ownerDir(dir); got != want {
			t.Errorf("ownerDir(%s) = %q, want %q", dir, got, want)
		}
	}
	for name, want := range map[string][2]string{
		"I-12": {domain.WorktreeItem, "I-12"}, "I-12~b": {domain.WorktreeAlt, "I-12"}, "_refine-I-3": {domain.WorktreeRefine, "I-3"},
		"_remote-x": {domain.WorktreeScratch, ""}, ".trash-I-1-abc": {domain.WorktreeTrash, ""}, "notes": {domain.WorktreeScratch, ""},
	} {
		if kind, id := worktreeKind(name); kind != want[0] || id != want[1] {
			t.Errorf("worktreeKind(%s) = %s %s, want %v", name, kind, id, want)
		}
	}
	if b, h := gitHead(t.TempDir()); b != "" || h != "" {
		t.Errorf("a folder that is not a checkout has no head: %q %q", b, h)
	}
}
