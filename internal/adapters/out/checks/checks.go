// Package checks implements ports.CheckRunner: it runs a check locally, or
// dispatches it to another machine, to GitHub Actions or to a webhook and
// reports it as pending. Local runs retry flaky failures on the branch and
// on main (domain.FlakyVerdict) and keep the full log next to a short
// summary for the agent.
package checks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/leandrodaf/barnraise/internal/adapters/out/execx"
	"github.com/leandrodaf/barnraise/internal/adapters/out/outfilter"
	"github.com/leandrodaf/barnraise/internal/adapters/out/webhook"
	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// Options configures the runner. Callbacks left nil make the matching
// runs_on kind return an error.
type Options struct {
	// Exec runs local commands (default execx.New()).
	Exec ports.Exec
	// LogDir receives the full log of every local run (default: temp dir).
	LogDir string
	// Blobs, when set, also stores each kept log and fills LogHash.
	Blobs ports.BlobStore
	// MainDir returns a checkout of main for flaky comparison ("" = none).
	MainDir func(spec ports.CheckSpec) string
	// AreaOf resolves a touched file to its area ("" = no area).
	AreaOf func(file string) string
	// Remote dispatches a check to a machine with the label.
	Remote func(ctx context.Context, label string, spec ports.CheckSpec) error
	// Dispatch starts a GitHub Actions workflow on branch with inputs
	// item, commit and check.
	Dispatch func(ctx context.Context, workflow, branch string, inputs map[string]string) error
	// CallbackURL is sent to webhook checks so they can report back.
	CallbackURL string
	// WebhookSecret signs webhook check requests (webhook package scheme).
	WebhookSecret string
	// HTTPClient posts webhook checks (default 30s timeout).
	HTTPClient *http.Client
	// DefaultTimeout applies when Def.Timeout is zero (default 30m).
	DefaultTimeout time.Duration
	// Now and NewID are injectable for tests.
	Now   func() time.Time
	NewID func() string
}

// Runner implements ports.CheckRunner.
type Runner struct{ o Options }

var _ ports.CheckRunner = (*Runner)(nil)

// MaxFlakyRuns caps runs per side (the doc: up to 3 on branch and main).
const MaxFlakyRuns = 3

// NewRunner returns a check runner.
func NewRunner(o Options) *Runner {
	if o.Exec == nil {
		o.Exec = execx.New()
	}
	if o.LogDir == "" {
		o.LogDir = filepath.Join(os.TempDir(), "barnraise-checks")
	}
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	if o.DefaultTimeout <= 0 {
		o.DefaultTimeout = 30 * time.Minute
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.NewID == nil {
		o.NewID = func() string { return fmt.Sprintf("chk-%d", time.Now().UnixNano()) }
	}
	return &Runner{o: o}
}

// Run runs or dispatches spec.
func (r *Runner) Run(ctx context.Context, spec ports.CheckSpec) (domain.CheckResult, bool, error) {
	def := spec.Def
	res := domain.CheckResult{
		ID: r.o.NewID(), ItemID: spec.ItemID, Check: def.Name, Commit: spec.Commit,
		RunsOn: def.RunsOn, At: r.o.Now().UTC(),
	}
	if skip, why := r.outOfScope(spec); skip {
		res.Passed = true
		res.Summary = why
		return res, false, nil
	}
	switch {
	case def.RunsOn == "local" || def.RunsOn == "":
		res.RunsOn = "local"
		err := r.runLocal(ctx, spec, &res)
		return res, false, err
	case strings.HasPrefix(def.RunsOn, "machine:"):
		label := strings.TrimPrefix(def.RunsOn, "machine:")
		if r.o.Remote == nil {
			return res, false, fmt.Errorf("check %s runs on a machine with label %q, but no remote dispatch is configured", def.Name, label)
		}
		if err := r.o.Remote(ctx, label, spec); err != nil {
			return res, false, fmt.Errorf("dispatch check %s to machine %s: %w", def.Name, label, err)
		}
		res.Summary = "dispatched to a machine with label " + label
		return res, true, nil
	case def.RunsOn == "github-actions":
		if r.o.Dispatch == nil {
			return res, false, fmt.Errorf("check %s runs on GitHub Actions, but no workflow dispatch is configured", def.Name)
		}
		inputs := map[string]string{"item": spec.ItemID, "commit": spec.Commit, "check": def.Name}
		if err := r.o.Dispatch(ctx, def.Workflow, spec.Branch, inputs); err != nil {
			return res, false, fmt.Errorf("dispatch workflow %s for check %s: %w", def.Workflow, def.Name, err)
		}
		res.Summary = "dispatched workflow " + def.Workflow + " on " + spec.Branch
		return res, true, nil
	case def.RunsOn == "webhook":
		if err := r.postWebhook(ctx, spec); err != nil {
			return res, false, err
		}
		res.Summary = "sent to " + def.URL
		return res, true, nil
	}
	return res, false, fmt.Errorf("check %s: runs_on %q must be local, github-actions, machine:<label> or webhook", def.Name, def.RunsOn)
}

// outOfScope skips a check the change does not concern (see CheckDef.Applies).
func (r *Runner) outOfScope(spec ports.CheckSpec) (bool, string) {
	ok, why := spec.Def.Applies(spec.Touched, spec.Areas, r.o.AreaOf)
	return !ok, why
}

type runOutcome struct {
	passed   bool
	summary  string
	logPath  string
	duration time.Duration
}

func (r *Runner) runLocal(ctx context.Context, spec ports.CheckSpec, res *domain.CheckResult) error {
	def := spec.Def
	if strings.TrimSpace(def.Run) == "" {
		return fmt.Errorf("check %s runs locally but has no command", def.Name)
	}
	runs := 1
	if def.FlakyRetries > 0 {
		runs = min(def.FlakyRetries, MaxFlakyRuns)
	}
	var branch, main []bool
	var last, firstFail runOutcome
	var total time.Duration

	for i := 0; i < runs; i++ {
		out, err := r.once(ctx, spec, spec.Dir, false, i+1)
		if err != nil {
			return err
		}
		total += out.duration
		branch = append(branch, out.passed)
		last = out
		if !out.passed && firstFail.logPath == "" {
			firstFail = out
		}
		if out.passed || def.FlakyRetries <= 0 {
			break
		}
	}
	branchFailedAll := !last.passed
	if branchFailedAll && def.FlakyRetries > 0 && r.o.MainDir != nil {
		mainSpec := spec
		mainSpec.Main = true
		if dir := r.o.MainDir(mainSpec); dir != "" {
			for i := 0; i < runs; i++ {
				out, err := r.once(ctx, mainSpec, dir, true, i+1)
				if err != nil {
					return err
				}
				total += out.duration
				main = append(main, out.passed)
				if !out.passed {
					break // red on main too: not this item's fault
				}
			}
		}
	}
	passed, flaky := domain.FlakyVerdict(branch, main)
	res.Passed, res.Flaky, res.Duration = passed, flaky, total

	report := last
	if last.passed && firstFail.logPath != "" {
		report = firstFail // show what failed before it passed
	}
	res.Summary = report.summary
	if flaky {
		res.Summary = fmt.Sprintf("flaky: branch %s, main %s\n%s", runsText(branch), runsText(main), report.summary)
	}
	res.LogPath = report.logPath
	if r.o.Blobs != nil && report.logPath != "" {
		if f, err := os.Open(report.logPath); err == nil {
			h, perr := r.o.Blobs.Put(ctx, f)
			f.Close()
			if perr == nil {
				res.LogHash = h
			}
		}
	}
	return nil
}

func runsText(rs []bool) string {
	if len(rs) == 0 {
		return "not run"
	}
	p := 0
	for _, x := range rs {
		if x {
			p++
		}
	}
	return fmt.Sprintf("%d/%d passed", p, len(rs))
}

// once runs the command a single time. Timeouts are failed runs, not
// errors; only start failures and cancellation are errors.
func (r *Runner) once(ctx context.Context, spec ports.CheckSpec, dir string, onMain bool, n int) (runOutcome, error) {
	def := spec.Def
	timeout := def.Timeout
	if timeout <= 0 {
		timeout = r.o.DefaultTimeout
	}
	env := os.Environ()
	for k, v := range spec.Env {
		env = append(env, k+"="+v)
	}
	branch := spec.Branch
	if onMain {
		branch = "main"
		env = append(env, "BARNRAISE_MAIN=1")
	}
	env = append(env,
		"BARNRAISE_ITEM="+spec.ItemID,
		"BARNRAISE_CHECK="+def.Name,
		"BARNRAISE_TOUCHED="+strings.Join(spec.Touched, "\n"),
		"BARNRAISE_AREAS="+strings.Join(spec.Areas, "\n"),
		"BARNRAISE_BRANCH="+branch,
		"BARNRAISE_COMMIT="+spec.Commit,
	)
	// Merge stderr into stdout inside the shell so the log keeps the order.
	// A check of one part of the repository runs inside its folder.
	cmd := ports.Cmd{Name: "sh", Args: []string{"-c", "exec 2>&1\n" + def.Run}, Dir: filepath.Join(dir, def.Dir), Env: env, Timeout: timeout}
	res, err := r.o.Exec.Run(ctx, cmd)
	timedOut := errors.Is(err, execx.ErrTimeout)
	if err != nil && !timedOut {
		return runOutcome{}, fmt.Errorf("check %s: %w", def.Name, err)
	}
	output := append(append([]byte(nil), res.Stdout...), res.Stderr...)
	side := "branch"
	if onMain {
		side = "main"
	}
	logPath, lerr := r.saveLog(spec, side, n, output)
	if lerr != nil {
		return runOutcome{}, lerr
	}
	tool := outfilter.Detect(def.Run)
	summary := outfilter.FilterWithLog(tool, output, res.ExitCode, logPath)
	passed := res.ExitCode == 0 && !timedOut
	if timedOut {
		summary = fmt.Sprintf("timed out after %s\n%s", timeout, summary)
	}
	return runOutcome{passed: passed, summary: summary, logPath: logPath, duration: res.Duration}, nil
}

func (r *Runner) saveLog(spec ports.CheckSpec, side string, n int, out []byte) (string, error) {
	if err := os.MkdirAll(r.o.LogDir, 0o700); err != nil {
		return "", err
	}
	name := fmt.Sprintf("%s-%s-%s-%s-%d.log", r.o.Now().UTC().Format("20060102T150405.000000000Z"),
		safe(spec.ItemID), safe(spec.Def.Name), side, n)
	p := filepath.Join(r.o.LogDir, name)
	return p, os.WriteFile(p, out, 0o600)
}

func safe(s string) string {
	if s == "" {
		return "none"
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		}
		return '_'
	}, s)
}

// WebhookRequest is the body posted to a webhook check.
type WebhookRequest struct {
	Item        string `json:"item"`
	Check       string `json:"check"`
	Branch      string `json:"branch"`
	Commit      string `json:"commit"`
	CallbackURL string `json:"callback_url,omitempty"`
}

func (r *Runner) postWebhook(ctx context.Context, spec ports.CheckSpec) error {
	def := spec.Def
	if def.URL == "" {
		return fmt.Errorf("check %s runs on a webhook but has no url", def.Name)
	}
	body, _ := json.Marshal(WebhookRequest{Item: spec.ItemID, Check: def.Name, Branch: spec.Branch, Commit: spec.Commit, CallbackURL: r.o.CallbackURL})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, def.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "barnraise-check")
	webhook.SignRequest(req, r.o.WebhookSecret, body, r.o.Now())
	resp, err := r.o.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("check %s: post to %s: %w", def.Name, def.URL, err)
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("check %s: %s answered %d: %s", def.Name, def.URL, resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return nil
}
