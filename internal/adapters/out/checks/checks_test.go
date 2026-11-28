package checks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/adapters/out/webhook"
	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

var now = time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

func newRunner(t *testing.T, o Options) *Runner {
	t.Helper()
	if o.LogDir == "" {
		o.LogDir = t.TempDir()
	}
	o.Now = func() time.Time { return now }
	o.NewID = func() string { return "c1" }
	return NewRunner(o)
}

func spec(dir, run string) ports.CheckSpec {
	return ports.CheckSpec{
		Def:    domain.CheckDef{Name: "unit", Run: run, RunsOn: "local", Timeout: 10 * time.Second},
		ItemID: "I-42", Branch: "barnraise/I-42-x", Commit: "abc123", Dir: dir,
		Touched: []string{"app/a.kt", "app/b.kt"}, Areas: []string{"A-app"},
	}
}

func TestLocalPassEnvAndLog(t *testing.T) {
	dir := t.TempDir()
	r := newRunner(t, Options{})
	s := spec(dir, `echo "item=$BARNRAISE_ITEM branch=$BARNRAISE_BRANCH commit=$BARNRAISE_COMMIT areas=$BARNRAISE_AREAS"; printf '%s' "$BARNRAISE_TOUCHED" > touched.txt; pwd; echo warn >&2`)
	res, pending, err := r.Run(context.Background(), s)
	if err != nil || pending {
		t.Fatalf("err=%v pending=%v", err, pending)
	}
	if !res.Passed || res.Flaky || res.ItemID != "I-42" || res.Check != "unit" || res.Commit != "abc123" || res.RunsOn != "local" || res.ID != "c1" {
		t.Fatalf("%+v", res)
	}
	log, err := os.ReadFile(res.LogPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(log), "item=I-42 branch=barnraise/I-42-x commit=abc123 areas=A-app") || !strings.Contains(string(log), dir) || !strings.Contains(string(log), "warn") {
		t.Fatalf("log %q", log)
	}
	touched, _ := os.ReadFile(filepath.Join(dir, "touched.txt"))
	if string(touched) != "app/a.kt\napp/b.kt" {
		t.Fatalf("touched %q", touched)
	}
	if !strings.HasSuffix(res.Summary, "full log: "+res.LogPath) {
		t.Fatalf("summary %q", res.Summary)
	}
}

func TestLocalFailNoRetries(t *testing.T) {
	r := newRunner(t, Options{})
	res, _, err := r.Run(context.Background(), spec(t.TempDir(), "echo '--- FAIL: TestX (0.00s)'; exit 1"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Passed || res.Flaky || !strings.Contains(res.Summary, "--- FAIL: TestX") {
		t.Fatalf("%+v", res)
	}
}

const bump = `n=$(cat count 2>/dev/null || echo 0); n=$((n+1)); echo $n > count; echo "run $n"; `

// counter makes a command that fails the first `fails` times in its dir.
func counter(fails int) string { return bump + `[ $n -gt ` + strconv.Itoa(fails) + ` ]` }

func runs(t *testing.T, dir string) string {
	b, _ := os.ReadFile(filepath.Join(dir, "count"))
	return strings.TrimSpace(string(b))
}

func TestFlakyOscillatesOnBranch(t *testing.T) {
	dir := t.TempDir()
	r := newRunner(t, Options{})
	s := spec(dir, counter(1))
	s.Def.FlakyRetries = 3
	res, _, err := r.Run(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Passed || !res.Flaky || runs(t, dir) != "2" {
		t.Fatalf("%+v runs=%s", res, runs(t, dir))
	}
	if !strings.Contains(res.Summary, "flaky: branch 1/2 passed") || !strings.Contains(res.Summary, "run 1") {
		t.Fatalf("summary %q", res.Summary)
	}
}

func TestFailsOnBranchPassesOnMain(t *testing.T) {
	dir, mainDir := t.TempDir(), t.TempDir()
	r := newRunner(t, Options{MainDir: func(s ports.CheckSpec) string {
		if !s.Main {
			t.Error("main spec not marked")
		}
		return mainDir
	}})
	s := spec(dir, bump+`[ "$BARNRAISE_MAIN" = 1 ] && [ "$BARNRAISE_BRANCH" = main ]`)
	s.Def.FlakyRetries = 3
	res, _, err := r.Run(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if res.Passed || res.Flaky {
		t.Fatalf("real failure expected: %+v", res)
	}
	if runs(t, dir) != "3" || runs(t, mainDir) != "3" {
		t.Fatalf("branch runs %s, main runs %s", runs(t, dir), runs(t, mainDir))
	}
}

// Red on the branch and on main: not the item's fault, and not verified
// either. It neither passes nor counts as a flake; the caller holds the item.
func TestFailsOnBothIsRedOnMain(t *testing.T) {
	dir, mainDir := t.TempDir(), t.TempDir()
	r := newRunner(t, Options{MainDir: func(ports.CheckSpec) string { return mainDir }})
	s := spec(dir, counter(9))
	s.Def.FlakyRetries = 2
	res, _, err := r.Run(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if res.Passed || res.Flaky || !res.MainRed || runs(t, dir) != "2" || runs(t, mainDir) != "1" {
		t.Fatalf("%+v branch=%s main=%s", res, runs(t, dir), runs(t, mainDir))
	}
	if !strings.HasPrefix(res.Summary, "red on main too: branch 0/2 passed, main 0/1 passed") {
		t.Fatalf("summary = %q", res.Summary)
	}
}

func TestTimeoutIsAFailedRun(t *testing.T) {
	r := newRunner(t, Options{})
	s := spec(t.TempDir(), "echo begin; sleep 20")
	s.Def.Timeout = 200 * time.Millisecond
	res, _, err := r.Run(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if res.Passed || !strings.HasPrefix(res.Summary, "timed out after 200ms") {
		t.Fatalf("%+v", res)
	}
}

func TestScopeNotTouched(t *testing.T) {
	r := newRunner(t, Options{AreaOf: func(f string) string {
		if strings.HasPrefix(f, "app/") {
			return "app"
		}
		return ""
	}})
	s := spec(t.TempDir(), "exit 1")
	s.Areas = nil
	s.Def.Areas = []string{"A-backend"}
	res, pending, err := r.Run(context.Background(), s)
	if err != nil || pending || !res.Passed || !strings.HasPrefix(res.Summary, "skipped: ") {
		t.Fatalf("%+v %v %v", res, pending, err)
	}
	s.Def.Areas = []string{"app"}
	if res, _, _ := r.Run(context.Background(), s); res.Passed {
		t.Fatal("touched area must run")
	}
	s.Def.Areas = []string{"backend"}
	s.Def.Scope = "all"
	if res, _, _ := r.Run(context.Background(), s); res.Passed {
		t.Fatal("scope all must run")
	}
}

type memBlobs struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (b *memBlobs) Put(_ context.Context, r io.Reader) (string, error) {
	data, _ := io.ReadAll(r)
	h := sha256.Sum256(data)
	k := hex.EncodeToString(h[:])
	b.mu.Lock()
	b.m[k] = data
	b.mu.Unlock()
	return k, nil
}
func (b *memBlobs) Get(context.Context, string) (io.ReadCloser, error) { return nil, nil }
func (b *memBlobs) Path(h string) string                               { return h }

func TestBlobStore(t *testing.T) {
	blobs := &memBlobs{m: map[string][]byte{}}
	r := newRunner(t, Options{Blobs: blobs})
	res, _, err := r.Run(context.Background(), spec(t.TempDir(), "echo hello"))
	if err != nil || res.LogHash == "" || string(blobs.m[res.LogHash]) != "hello\n" {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestRemoteAndActions(t *testing.T) {
	var gotLabel, gotWF, gotBranch string
	var gotInputs map[string]string
	r := newRunner(t, Options{
		Remote: func(_ context.Context, label string, s ports.CheckSpec) error { gotLabel = label; return nil },
		Dispatch: func(_ context.Context, wf, br string, in map[string]string) error {
			gotWF, gotBranch, gotInputs = wf, br, in
			return nil
		},
	})
	s := spec("", "")
	s.Def.RunsOn = "machine:emulators"
	res, pending, err := r.Run(context.Background(), s)
	if err != nil || !pending || gotLabel != "emulators" || res.RunsOn != "machine:emulators" {
		t.Fatalf("%+v %v %v", res, pending, err)
	}
	s.Def.RunsOn, s.Def.Workflow = "github-actions", "ci.yml"
	_, pending, err = r.Run(context.Background(), s)
	if err != nil || !pending || gotWF != "ci.yml" || gotBranch != "barnraise/I-42-x" ||
		gotInputs["item"] != "I-42" || gotInputs["commit"] != "abc123" || gotInputs["check"] != "unit" {
		t.Fatalf("%v %v %s %s %v", pending, err, gotWF, gotBranch, gotInputs)
	}
	if _, _, err := NewRunner(Options{}).Run(context.Background(), s); err == nil {
		t.Fatal("missing dispatch must error")
	}
	s.Def.RunsOn = "mars"
	if _, _, err := r.Run(context.Background(), s); err == nil {
		t.Fatal("unknown runs_on must error")
	}
}

func TestWebhookCheck(t *testing.T) {
	var body []byte
	var hdr http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ = io.ReadAll(req.Body)
		hdr = req.Header
		w.WriteHeader(202)
	}))
	defer srv.Close()
	r := newRunner(t, Options{CallbackURL: "https://me/api/checks", WebhookSecret: "k"})
	s := spec("", "")
	s.Def.RunsOn, s.Def.URL = "webhook", srv.URL
	_, pending, err := r.Run(context.Background(), s)
	if err != nil || !pending {
		t.Fatalf("%v %v", pending, err)
	}
	var wr WebhookRequest
	json.Unmarshal(body, &wr)
	if wr != (WebhookRequest{Item: "I-42", Check: "unit", Branch: "barnraise/I-42-x", Commit: "abc123", CallbackURL: "https://me/api/checks"}) {
		t.Fatalf("%+v", wr)
	}
	if err := webhook.VerifyAt(now, "k", body, hdr.Get(webhook.HeaderSignature), hdr.Get(webhook.HeaderTimestamp), time.Minute); err != nil {
		t.Fatal(err)
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) }))
	defer bad.Close()
	s.Def.URL = bad.URL
	if _, _, err := r.Run(context.Background(), s); err == nil {
		t.Fatal("5xx must error")
	}
}

func TestPluginCheck(t *testing.T) {
	blobs := &memBlobs{m: map[string][]byte{}}
	logPath := filepath.Join(t.TempDir(), "farm.log")
	if err := os.WriteFile(logPath, []byte("3 devices passed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var gotName string
	r := newRunner(t, Options{Blobs: blobs, Plugin: func(_ context.Context, name string, s ports.CheckSpec) (domain.CheckResult, bool, error) {
		gotName = name
		if s.Def.Run == "boom" {
			return domain.CheckResult{}, false, errors.New("farm down")
		}
		return domain.CheckResult{Passed: true, Summary: "3 devices", LogPath: logPath, Artifacts: []string{"shot.png"}, Duration: time.Second}, s.Def.Run == "later", nil
	}})
	s := spec("", "")
	s.Def.RunsOn = "plugin:farm"
	res, pending, err := r.Run(context.Background(), s)
	if err != nil || pending || gotName != "farm" || !res.Passed || res.ID != "c1" || res.ItemID != "I-42" || res.RunsOn != "plugin:farm" ||
		res.Summary != "3 devices" || len(res.Artifacts) != 1 || string(blobs.m[res.LogHash]) != "3 devices passed\n" {
		t.Fatalf("%+v %v %v", res, pending, err)
	}
	s.Def.Run = "later"
	if _, pending, err = r.Run(context.Background(), s); err != nil || !pending {
		t.Fatalf("pending = %v %v", pending, err)
	}
	s.Def.Run = "boom"
	if _, _, err = r.Run(context.Background(), s); err == nil || !strings.Contains(err.Error(), "check unit on plugin farm: farm down") {
		t.Fatalf("err = %v", err)
	}
	if _, _, err = NewRunner(Options{}).Run(context.Background(), s); err == nil || !strings.Contains(err.Error(), "no plugins are configured") {
		t.Fatalf("err = %v", err)
	}
}

// A change of thousands of files passed Linux's limit for one variable and
// the check could not start: the list goes in a file instead.
func TestHugeTouchedListGoesInAFile(t *testing.T) {
	dir := t.TempDir()
	r := newRunner(t, Options{})
	s := spec(dir, `test -z "$BARNRAISE_TOUCHED" && wc -l < "$BARNRAISE_TOUCHED_FILE" > count.txt`)
	s.Touched = nil
	for i := range 5000 {
		s.Touched = append(s.Touched, fmt.Sprintf("art/pixel/keepers/warden/frames/run/east/frame_%04d.png", i))
	}
	res, _, err := r.Run(context.Background(), s)
	if err != nil || !res.Passed {
		t.Fatalf("%v %+v", err, res)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "count.txt")); strings.TrimSpace(string(b)) != "5000" {
		t.Fatalf("the check read %q files", b)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".barnraise-touched-*")); len(left) != 0 {
		t.Fatalf("left behind: %v", left)
	}
}
