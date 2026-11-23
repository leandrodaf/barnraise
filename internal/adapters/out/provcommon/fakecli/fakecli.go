// Package fakecli turns a test binary into a scripted stand-in for a provider
// CLI (the helper-process pattern). A test package calls Main from TestMain;
// Command returns a provcommon.CommandFunc that re-runs the test binary with a
// script in its environment.
package fakecli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/leandrodaf/barnraise/internal/adapters/out/provcommon"
)

const modeVar = "BARNRAISE_FAKECLI"

// Script describes what the fake CLI does.
type Script struct {
	Fixture   string            // file printed to stdout
	Outputs   map[string]string // first argument(s) joined by space -> file printed instead (e.g. "--version")
	Stderr    string            // text written to stderr
	Exit      int               // exit code
	Sleep     time.Duration     // wait after printing, until killed or interrupted
	IgnoreInt bool              // ignore SIGINT while sleeping (forces SIGKILL)
	Record    string            // file receiving the Call as JSON
}

// Call is what the fake CLI saw.
type Call struct {
	Args  []string `json:"args"`
	Env   []string `json:"env"`
	Dir   string   `json:"dir"`
	Stdin string   `json:"stdin"`
}

// Command returns a CommandFunc that runs the current test binary as the CLI.
// The binary name is recorded as the first argument.
func Command(s Script) provcommon.CommandFunc {
	abs := func(p string) string {
		if p == "" || filepath.IsAbs(p) {
			return p
		}
		a, err := filepath.Abs(p)
		if err != nil {
			return p
		}
		return a
	}
	s.Fixture, s.Record = abs(s.Fixture), abs(s.Record)
	outs := make(map[string]string, len(s.Outputs))
	for k, v := range s.Outputs {
		outs[k] = abs(v)
	}
	s.Outputs = outs
	return func(_ context.Context, name string, args ...string) *exec.Cmd {
		b, _ := json.Marshal(s)
		cmd := exec.Command(os.Args[0], append([]string{name}, args...)...)
		// The race detector sleeps 1s at exit by default; tests run many fakes.
		cmd.Env = []string{modeVar + "=" + string(b), "GORACE=atexit_sleep_ms=0"}
		return cmd
	}
}

// ReadCall loads what the fake CLI recorded.
func ReadCall(path string) (Call, error) {
	var c Call
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	err = json.Unmarshal(b, &c)
	return c, err
}

// Main runs the fake CLI and exits when the process was started by Command;
// otherwise it returns at once. Call it first thing in TestMain.
func Main() {
	raw, ok := os.LookupEnv(modeVar)
	if !ok {
		return
	}
	var s Script
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		fmt.Fprintln(os.Stderr, "fakecli:", err)
		os.Exit(97)
	}
	os.Exit(run(s, os.Args[1:]))
}

func run(s Script, args []string) int {
	if s.IgnoreInt {
		signal.Ignore(syscall.SIGINT)
	}
	cliArgs := args
	if len(cliArgs) > 0 {
		cliArgs = cliArgs[1:] // drop the binary name
	}
	var stdin []byte
	if fi, err := os.Stdin.Stat(); err == nil && fi.Mode()&os.ModeCharDevice == 0 {
		stdin, _ = io.ReadAll(os.Stdin)
	}
	if s.Record != "" {
		dir, _ := os.Getwd()
		var env []string
		for _, kv := range os.Environ() {
			if !strings.HasPrefix(kv, modeVar+"=") && !strings.HasPrefix(kv, "GORACE=") {
				env = append(env, kv)
			}
		}
		b, _ := json.Marshal(Call{Args: args, Env: env, Dir: dir, Stdin: string(stdin)})
		_ = os.WriteFile(s.Record, b, 0o600)
	}
	file := s.Fixture
	for n := len(cliArgs); n > 0; n-- {
		if f, ok := s.Outputs[strings.Join(cliArgs[:n], " ")]; ok {
			file = f
			break
		}
	}
	if file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			fmt.Fprintln(os.Stderr, "fakecli:", err)
			return 98
		}
		_, _ = os.Stdout.Write(b)
	}
	if s.Stderr != "" {
		fmt.Fprint(os.Stderr, s.Stderr)
	}
	if s.Sleep > 0 {
		sig := make(chan os.Signal, 1)
		if !s.IgnoreInt {
			signal.Notify(sig, syscall.SIGINT)
		}
		select {
		case <-time.After(s.Sleep):
		case <-sig:
			return 130
		}
	}
	return s.Exit
}
