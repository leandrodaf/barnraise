package microvm

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// fakeRunner writes a stand-in for smolvm: it records its arguments and
// runs what follows "--" on the machine itself, as a VM would inside.
func fakeRunner(t *testing.T) (path, record string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake runner is a shell script")
	}
	dir := t.TempDir()
	record = filepath.Join(dir, "args")
	path = filepath.Join(dir, "smolvm")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = --version ]; then echo 'smolvm 0.0-fake'; exit 0; fi\n" +
		"printf '%s\\n' \"$@\" > " + record + "\n" +
		"while [ $# -gt 0 ] && [ \"$1\" != -- ]; do shift; done\n" +
		"shift\n" +
		"exec \"$@\"\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path, record
}

func TestSupport(t *testing.T) {
	missing := New(Options{Runner: "no-such-runner-xyz", Image: "img"})
	if s := missing.Support(); s.Available || !strings.Contains(s.Reason, "not installed") || s.Backend != Backend {
		t.Fatalf("missing runner: %+v", s)
	}
	path, _ := fakeRunner(t)
	noKVM := New(Options{Runner: path, Image: "img", KVM: func() bool { return false }})
	if s := noKVM.Support(); s.Available || !strings.Contains(s.Reason, "/dev/kvm") {
		t.Fatalf("no kvm: %+v", s)
	}
	noImage := New(Options{Runner: path, KVM: func() bool { return true }})
	if s := noImage.Support(); s.Available || !strings.Contains(s.Reason, "image") {
		t.Fatalf("no image: %+v", s)
	}
	custom := New(Options{Runner: path, Template: []string{"{runner}", "--", "{command}"}, KVM: func() bool { return true }})
	if s := custom.Support(); !s.Available {
		t.Fatalf("a template without {image} needs no image: %+v", s)
	}
	ok := New(Options{Runner: path, Image: "img", KVM: func() bool { return true }})
	if s := ok.Support(); !s.Available || s.Reason != "smolvm 0.0-fake" {
		t.Fatalf("ok: %+v", s)
	}
	broken := New(Options{Runner: path, Image: "img", KVM: func() bool { return true },
		Version: func(context.Context, string) (string, error) { return "", errors.New("x") }})
	if s := broken.Support(); !s.Available || s.Reason != "" {
		t.Fatalf("no version is not fatal: %+v", s)
	}
}

func TestWrapExpandsTheTemplate(t *testing.T) {
	path, _ := fakeRunner(t)
	work := t.TempDir()
	extra := t.TempDir()
	c := New(Options{Runner: path, Image: "ghcr.io/me/toolchain:1", CPUs: 2, MemoryMB: 2048, Mounts: []string{extra + ":ro", "/no/such"},
		KVM: func() bool { return true }})
	conf := domain.Confinement{Profile: "check", Work: []string{work, work, "/missing"}, Network: domain.NetLocal, Env: []string{"A", "B"}}
	name, args, err := c.Wrap(conf, "sh", []string{"-c", "make test"})
	if err != nil || name != path {
		t.Fatal(name, err)
	}
	got := strings.Join(args, " ")
	for _, want := range []string{"machine run --image ghcr.io/me/toolchain:1 --cpus 2 --mem 2048",
		"--volume " + work + ":" + work + " --volume " + extra + ":" + extra + ":ro --env A --env B --",
		work + " sh -c make test"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %s", want, got)
		}
	}
	if strings.Contains(got, "--net") || strings.Contains(got, "/no/such") || strings.Count(got, "--volume") != 2 {
		t.Fatalf("a check has no network; missing paths are not mounted: %s", got)
	}
	conf.Network = domain.NetRegistries
	conf.Hosts = []string{"proxy.golang.org"}
	_, args, _ = c.Wrap(conf, "true", nil)
	if !strings.Contains(strings.Join(args, " "), "--allow-host proxy.golang.org") {
		t.Fatalf("registries: %v", args)
	}
	conf.Hosts = nil
	_, args, _ = c.Wrap(conf, "true", nil)
	if !strings.Contains(strings.Join(args, " "), "--net") {
		t.Fatalf("registries without hosts: %v", args)
	}
	conf.Network = domain.NetOpen
	_, args, _ = c.Wrap(conf, "true", nil)
	if !strings.Contains(strings.Join(args, " "), "--net") {
		t.Fatalf("open: %v", args)
	}
	conf.DenyCommands = []string{`\bgit\s+push\b`}
	if _, _, err := c.Wrap(conf, "sh", []string{"-c", "git push"}); err == nil {
		t.Fatal("command rules apply before the VM starts")
	}
	if _, _, err := c.Wrap(domain.Confinement{}, "true", nil); err == nil {
		t.Fatal("no work folder")
	}
	if _, _, err := New(Options{Runner: "no-such-runner-xyz"}).Wrap(conf, "true", nil); err == nil {
		t.Fatal("unavailable")
	}
	empty := New(Options{Runner: path, Template: []string{"{cpus}"}, KVM: func() bool { return true }})
	if _, _, err := empty.Wrap(domain.Confinement{Work: []string{work}}, "true", nil); err == nil {
		t.Fatal("empty template")
	}
}

// The wrapped command really runs through the runner, in the work folder.
func TestWrappedCommandRunsThroughTheRunner(t *testing.T) {
	path, record := fakeRunner(t)
	work := t.TempDir()
	c := New(Options{Runner: path, Image: "img", KVM: func() bool { return true }})
	name, args, err := c.Wrap(domain.Confinement{Work: []string{work}, Network: domain.NetLocal}, "sh", []string{"-c", "pwd; echo hi > out"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	real, _ := filepath.EvalSymlinks(work)
	if got := strings.TrimSpace(string(out)); got != work && got != real {
		t.Fatalf("runs in the worktree: %q", got)
	}
	if data, err := os.ReadFile(filepath.Join(work, "out")); err != nil || string(data) != "hi\n" {
		t.Fatalf("wrote %q %v", data, err)
	}
	rec, _ := os.ReadFile(record)
	if !strings.HasPrefix(string(rec), "machine\nrun\n--image\nimg\n") {
		t.Fatalf("runner saw: %s", rec)
	}
}

func TestExpand(t *testing.T) {
	got := Expand([]string{"{runner}", "--name=vm-{image}", "{cpus}", "x"}, map[string][]string{"{runner}": {"r"}, "{image}": {"i"}})
	if strings.Join(got, " ") != "r --name=vm-i x" {
		t.Fatal(got)
	}
	if !hasKVM() && runtime.GOOS != "linux" {
		t.Fatal("only Linux needs /dev/kvm")
	}
}
