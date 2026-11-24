package outfilter

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/adapters/out/execx"
	"github.com/leandrodaf/barnraise/internal/domain"
)

func TestStoreShowListAndPrune(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	st := Store{Dir: dir, Keep: time.Hour, KeepBytes: 1 << 20, Now: func() time.Time { return now }}
	res, err := RunCommand(context.Background(), []string{"sh", "-c", "seq 1 500"}, Options{Store: st})
	if err != nil || res.Record.ID == "" {
		t.Fatalf("%+v %v", res, err)
	}
	id := res.Record.ID
	all, err := st.Show(id, "", 0, 0)
	if err != nil || !strings.HasPrefix(all, "1\n2\n") || !strings.HasSuffix(all, "500\n") {
		t.Fatalf("%q %v", all[:20], err)
	}
	part, _ := st.Show(id, "", 10, 12)
	if part != "10\n11\n12\n" {
		t.Fatalf("%q", part)
	}
	hits, _ := st.Show(id, `^4\d\d$`, 0, 0)
	if !strings.HasPrefix(hits, "400: 400\n") || strings.Count(hits, "\n") != 100 {
		t.Fatalf("%q", hits)
	}
	if _, err := st.Show(id, "(", 0, 0); err == nil {
		t.Fatal("bad pattern")
	}
	for _, bad := range []string{"../etc/passwd", "o123", "oZZZZZZZZ"} {
		if _, err := st.Show(bad, "", 0, 0); err == nil {
			t.Fatalf("%s accepted", bad)
		}
	}
	if _, err := st.Show("o00000000", "", 0, 0); err == nil || !strings.Contains(err.Error(), "barnraise output list") {
		t.Fatal(err)
	}

	// A short output is printed as it is and not kept.
	res2, err := RunCommand(context.Background(), []string{"sh", "-c", "echo hello"}, Options{Store: st})
	if err != nil || res2.Shortened || res2.Record.ID != "" || res2.Text != "hello" {
		t.Fatalf("%+v", res2)
	}
	recs, _ := st.List()
	if len(recs) != 1 {
		t.Fatalf("%+v", recs)
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("temporary file left: %s", e.Name())
		}
	}

	// Two hours later the first output expires when the next one is kept.
	now = now.Add(2 * time.Hour)
	res3, _ := RunCommand(context.Background(), []string{"sh", "-c", "seq 1 300"}, Options{Store: st})
	recs, _ = st.List()
	if len(recs) != 1 || recs[0].ID != res3.Record.ID {
		t.Fatalf("%+v", recs)
	}
	if _, err := os.Stat(st.Path(id)); !os.IsNotExist(err) {
		t.Fatal("expired output still on disk")
	}

	// The size cap removes the oldest outputs, never the newest.
	small := Store{Dir: dir, KeepBytes: 100, Now: st.Now}
	r4, _ := RunCommand(context.Background(), []string{"sh", "-c", "seq 1 400"}, Options{Store: small})
	recs, _ = small.List()
	if len(recs) != 1 || recs[0].ID != r4.Record.ID {
		t.Fatalf("%+v", recs)
	}
}

func TestStoreFromEnv(t *testing.T) {
	t.Setenv(domain.EnvOutputKeep, "3h0m0s")
	t.Setenv(domain.EnvOutputKeepBytes, "1024")
	s := StoreFromEnv("/x")
	if s.Keep != 3*time.Hour || s.KeepBytes != 1024 || s.Dir != "/x" {
		t.Fatalf("%+v", s)
	}
	t.Setenv(domain.EnvOutputKeep, "nonsense")
	if StoreFromEnv("/x").Keep != 0 {
		t.Fatal("bad duration ignored")
	}
}

func TestBoundedKeptFile(t *testing.T) {
	dir := t.TempDir()
	f, err := os.CreateTemp(dir, "k")
	if err != nil {
		t.Fatal(err)
	}
	oldHead := keepHead
	keepHead = 4
	defer func() { keepHead = oldHead }()
	k := &keptOutput{file: f, tail: execx.NewTailBuffer(10)}
	_, _ = k.Write([]byte("HEAD0123456789"))
	_, _ = k.Write([]byte("abcdefghij"))
	if err := k.finish(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Clean(f.Name()))
	if !k.truncated || !strings.HasPrefix(string(data), "HEAD\n… 10 bytes omitted") || !strings.HasSuffix(string(data), "abcdefghij") {
		t.Fatalf("%q", data)
	}
}

// fakeRTK writes an rtk that refuses "unknowncmd" like a CLI parser and
// otherwise prints a marker and the first lines of the command's output.
func fakeRTK(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "rtk")
	script := `#!/bin/sh
if [ "$1" = "unknowncmd" ]; then
  echo "error: unrecognized subcommand 'unknowncmd'" >&2
  echo "Usage: rtk <COMMAND>" >&2
  exit 2
fi
echo "rtk compact: $*"
"$@" 2>&1 | head -3
`
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDelegationToRTK(t *testing.T) {
	rtk := fakeRTK(t)
	bin := t.TempDir()
	// A command without a native filter that prints a lot.
	if err := os.WriteFile(filepath.Join(bin, "noisy"), []byte("#!/bin/sh\nseq 1 400\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "unknowncmd"), []byte("#!/bin/sh\nseq 1 400\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	st := Store{Dir: t.TempDir()}

	res, err := RunCommand(context.Background(), []string{"noisy", "--all"}, Options{Store: st, Delegate: domain.DelegateAuto, RTK: rtk})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "rtk compact: noisy --all") || !strings.Contains(res.Text, "(filtered by rtk)") || strings.Contains(res.Text, "400") {
		t.Fatalf("%s", res.Text)
	}

	// rtk refuses a command it does not know: it runs natively, kept in full.
	res, err = RunCommand(context.Background(), []string{"unknowncmd"}, Options{Store: st, Delegate: domain.DelegateAuto, RTK: rtk})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Text, "rtk") || res.Record.ID == "" || res.Record.Delegated != "" {
		t.Fatalf("%+v", res)
	}

	// auto keeps the native filters; rtk takes every command; off never.
	res, _ = RunCommand(context.Background(), []string{"ls", bin}, Options{Store: st, Delegate: domain.DelegateAuto, RTK: rtk})
	if strings.Contains(res.Text, "rtk compact") {
		t.Fatalf("ls has a native filter: %s", res.Text)
	}
	if !Delegable(domain.DelegateRTK, ToolGitStatus, "git status") || Delegable(domain.DelegateAuto, ToolGitStatus, "git status") {
		t.Fatal("rtk mode delegates native tools too")
	}
	if Delegable(domain.DelegateOff, ToolGeneric, "noisy") || Delegable("", ToolGeneric, "noisy") {
		t.Fatal("off")
	}
	for _, cmd := range []string{"noisy | head", "A=1 noisy", "echo hi", `noisy "a b"`, ""} {
		if Delegable(domain.DelegateAuto, ToolGeneric, cmd) {
			t.Errorf("%q delegated", cmd)
		}
	}
	res, _ = RunCommand(context.Background(), []string{"noisy"}, Options{Store: st, Delegate: domain.DelegateOff, RTK: rtk})
	if strings.Contains(res.Text, "rtk") {
		t.Fatal(res.Text)
	}
	// --raw prints everything and never delegates.
	res, _ = RunCommand(context.Background(), []string{"noisy"}, Options{Store: st, Delegate: domain.DelegateRTK, RTK: rtk, Raw: true})
	if !strings.HasSuffix(res.Text, "\n400") || res.Shortened {
		t.Fatalf("%q", res.Text[len(res.Text)-20:])
	}
}

func TestDelegateFromEnvAndLookRTK(t *testing.T) {
	t.Setenv(domain.EnvOutfilterDelegate, "OFF")
	if DelegateFromEnv() != domain.DelegateOff {
		t.Fatal("off")
	}
	t.Setenv(domain.EnvOutfilterDelegate, "bogus")
	if DelegateFromEnv() != domain.DelegateAuto {
		t.Fatal("default")
	}
	rtk := fakeRTK(t)
	t.Setenv("PATH", filepath.Dir(rtk))
	if LookRTK() != rtk {
		t.Fatal(LookRTK())
	}
	t.Setenv("PATH", t.TempDir())
	if LookRTK() != "" {
		t.Fatal("found rtk on an empty PATH")
	}
}
