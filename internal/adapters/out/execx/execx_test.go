package execx

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/ports"
)

func TestRunCapturesOutputAndExitCode(t *testing.T) {
	r := New()
	res, err := r.Run(context.Background(), ports.Cmd{Name: "sh", Args: []string{"-c", "echo out; echo err >&2; exit 3"}})
	if err != nil {
		t.Fatalf("non-zero exit must not be an error: %v", err)
	}
	if res.ExitCode != 3 {
		t.Fatalf("exit code = %d", res.ExitCode)
	}
	if string(res.Stdout) != "out\n" || string(res.Stderr) != "err\n" {
		t.Fatalf("stdout=%q stderr=%q", res.Stdout, res.Stderr)
	}
}

func TestRunDirEnvStdin(t *testing.T) {
	dir := t.TempDir()
	res, err := New().Run(context.Background(), ports.Cmd{
		Name: "sh", Args: []string{"-c", "pwd; echo $FOO; cat"},
		Dir: dir, Env: []string{"FOO=bar", "PATH=/usr/bin:/bin"}, Stdin: strings.NewReader("in"),
	})
	if err != nil {
		t.Fatal(err)
	}
	got := string(res.Stdout)
	if !strings.Contains(got, dir) || !strings.Contains(got, "bar\n") || !strings.HasSuffix(got, "in") {
		t.Fatalf("stdout=%q", got)
	}
}

func TestRunStartFailure(t *testing.T) {
	_, err := New().Run(context.Background(), ports.Cmd{Name: "/does/not/exist"})
	if err == nil {
		t.Fatal("expected start error")
	}
}

func TestRunTimeoutKillsGroup(t *testing.T) {
	start := time.Now()
	// The background sleep keeps stdout open; only a group kill ends it quickly.
	res, err := New(WithKillGrace(500*time.Millisecond)).Run(context.Background(), ports.Cmd{
		Name: "sh", Args: []string{"-c", "echo started; sleep 30 & sleep 30"}, Timeout: 200 * time.Millisecond,
	})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
	var te *TimeoutError
	if !errors.As(err, &te) || te.Timeout != 200*time.Millisecond {
		t.Fatalf("err = %#v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("took %s", time.Since(start))
	}
	if !strings.Contains(string(res.Stdout), "started") {
		t.Fatalf("partial output lost: %q", res.Stdout)
	}
}

func TestRunContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	_, err := New().Run(ctx, ports.Cmd{Name: "sleep", Args: []string{"30"}, Timeout: time.Minute})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestOutputCapKeepsTail(t *testing.T) {
	res, err := New(WithMaxOutput(10)).Run(context.Background(), ports.Cmd{Name: "sh", Args: []string{"-c", "printf 0123456789abcdef"}})
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Stdout) != "6789abcdef" {
		t.Fatalf("stdout=%q", res.Stdout)
	}
}

func TestTailBuffer(t *testing.T) {
	b := NewTailBuffer(5)
	b.Write([]byte("abc"))
	b.Write([]byte("def"))
	if string(b.Bytes()) != "bcdef" || !b.Truncated() {
		t.Fatalf("%q %v", b.Bytes(), b.Truncated())
	}
	b.Write([]byte("0123456789"))
	if string(b.Bytes()) != "56789" {
		t.Fatalf("%q", b.Bytes())
	}
}
