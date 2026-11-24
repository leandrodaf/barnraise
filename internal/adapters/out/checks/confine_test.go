package checks

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/leandrodaf/barnraise/internal/ports"
)

// A local check runs through the confiner the composition root gives, with
// the variables it adds; a confinement error fails the run instead of
// running the check unconfined.
func TestLocalCheckRunsConfined(t *testing.T) {
	dir := t.TempDir()
	var sawDir string
	r := newRunner(t, Options{Confine: func(spec ports.CheckSpec, d string, env map[string]string) (*ports.Confine, map[string]string, error) {
		sawDir = d
		return &ports.Confine{Backend: "test", Wrap: func(name string, args []string) (string, []string, error) {
			// The "confinement" marks the run, then runs the command.
			return "sh", []string{"-c", `echo confined; exec "$@"`, "wrap", name, args[0], args[1]}, nil
		}}, map[string]string{"HTTPS_PROXY": "http://127.0.0.1:7421"}, nil
	}})
	res, _, err := r.Run(context.Background(), spec(dir, `echo "proxy=$HTTPS_PROXY"`))
	if err != nil || !res.Passed || sawDir != dir {
		t.Fatalf("%+v %v %s", res, err, sawDir)
	}
	log, _ := os.ReadFile(res.LogPath)
	if !strings.Contains(string(log), "confined") || !strings.Contains(string(log), "proxy=http://127.0.0.1:7421") {
		t.Fatalf("log: %s", log)
	}

	bad := newRunner(t, Options{Confine: func(ports.CheckSpec, string, map[string]string) (*ports.Confine, map[string]string, error) {
		return nil, nil, errors.New("no profile")
	}})
	if _, _, err := bad.Run(context.Background(), spec(dir, "true")); err == nil || !strings.Contains(err.Error(), "sandbox") {
		t.Fatalf("confinement error: %v", err)
	}
	refuse := newRunner(t, Options{Confine: func(ports.CheckSpec, string, map[string]string) (*ports.Confine, map[string]string, error) {
		return &ports.Confine{Wrap: func(string, []string) (string, []string, error) { return "", nil, errors.New("refused") }}, nil, nil
	}})
	if _, _, err := refuse.Run(context.Background(), spec(dir, "true")); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("wrap error: %v", err)
	}
	// No confiner for this check: it runs as before.
	plain := newRunner(t, Options{Confine: func(ports.CheckSpec, string, map[string]string) (*ports.Confine, map[string]string, error) {
		return nil, nil, nil
	}})
	if res, _, err := plain.Run(context.Background(), spec(dir, "true")); err != nil || !res.Passed {
		t.Fatalf("%+v %v", res, err)
	}
}
