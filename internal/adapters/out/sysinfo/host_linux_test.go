//go:build linux

package sysinfo

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/ports"
)

func TestParseStat(t *testing.T) {
	boot := time.Unix(1_700_000_000, 0)
	// A name with spaces and parentheses, as Chrome and tmux make them.
	line := "4242 (Web Content (x) y) S 4000 4242 4242 0 -1 4194560 100 0 0 0 250 50 0 0 20 0 5 0 360000 1000000 2048 18446744073709551615 1 1 0 0 0 0 0 0 0 0 0 0 17 3 0 0 0 0 0\n"
	p, ok := parseStat([]byte(line), 4242, 4096, boot)
	if !ok || p.Name != "Web Content (x) y" || p.PPID != 4000 || p.CPU != 3*time.Second || p.RSS != 2048*4096 || p.Start != 360000 {
		t.Fatalf("parsed %+v ok=%v", p, ok)
	}
	if !p.StartedAt.Equal(boot.Add(time.Hour)) {
		t.Fatalf("started %s, want one hour after boot", p.StartedAt)
	}
	for _, bad := range []string{"", "1 (x", "1 (x) S 2 3"} {
		if _, ok := parseStat([]byte(bad), 1, 4096, boot); ok {
			t.Errorf("a cut line was accepted: %q", bad)
		}
	}
}

func TestHostReadsThisMachine(t *testing.T) {
	h := NewHost()
	list, err := h.Processes(nil)
	if err != nil {
		t.Fatal(err)
	}
	var me *ports.Proc
	for i := range list {
		if list[i].PID == os.Getpid() {
			me = &list[i]
		}
	}
	if me == nil || me.PPID != os.Getppid() || me.RSS <= 0 || me.Name == "" || me.StartedAt.IsZero() || time.Since(me.StartedAt) > time.Hour {
		t.Fatalf("this process, as the machine shows it: %+v", me)
	}
	wd, _ := os.Getwd()
	if got := h.Cwd(os.Getpid()); got != wd {
		t.Fatalf("cwd %q, want %q", got, wd)
	}
	if args := h.Cmdline(os.Getpid()); len(args) == 0 || filepath.Base(args[0]) != filepath.Base(os.Args[0]) {
		t.Fatalf("cmdline %v", args)
	}
	if h.Cwd(-1) != "" || h.Cmdline(-1) != nil {
		t.Fatal("a process that does not exist has no directory and no arguments")
	}
	// The buffer is reused: a second read allocates no new list.
	again, _ := h.Processes(list)
	if len(again) == 0 || &again[0] != &list[0] {
		t.Fatal("the buffer was not reused")
	}
	free, total := h.Disk(t.TempDir())
	if total <= 0 || free <= 0 || free > total {
		t.Fatalf("disk free %d of %d", free, total)
	}
	if f, tot := h.Disk("/does/not/exist"); f != 0 || tot != 0 {
		t.Fatal("a missing path has no disk")
	}
}

func TestHostWithAFakeProc(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		path := filepath.Join(root, "proc", rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("stat", "cpu 1 2 3\nbtime 1700000000\n")
	write("10/stat", "10 (jest) R 1 10 10 0 -1 0 0 0 0 0 100 0 0 0 20 0 1 0 100 0 10 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0\n")
	write("10/cmdline", "node\x00jest\x00--ci\x00")
	write("self/stat", "ignored")
	write("11/other", "a process that ended before its stat was read")
	h := &Host{Root: root}
	list, err := h.Processes(nil)
	if err != nil || len(list) != 1 || list[0].PID != 10 || list[0].CPU != time.Second || !list[0].StartedAt.Equal(time.Unix(1_700_000_001, 0)) {
		t.Fatalf("list %+v err %v", list, err)
	}
	if args := h.Cmdline(10); len(args) != 3 || args[1] != "jest" {
		t.Fatalf("cmdline %v", args)
	}
	if _, err := (&Host{Root: filepath.Join(root, "nope")}).Processes(nil); err == nil {
		t.Fatal("a machine without /proc lists nothing and says so")
	}
}
