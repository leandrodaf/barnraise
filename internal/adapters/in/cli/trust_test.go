package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/adapters/out/sqlite"
	"github.com/leandrodaf/barnraise/internal/app"
	"github.com/leandrodaf/barnraise/internal/domain"
)

func TestPrintChainAndTrust(t *testing.T) {
	var b bytes.Buffer
	printChain(&b, domain.ChainReport{From: 1, To: 9, Checked: 9, Head: "abcdef0123456789", Genesis: 4, Anchor: &domain.ChainAnchorCheck{Seq: 8, OK: true}})
	if s := b.String(); !strings.Contains(s, "intact: 9 events") || !strings.Contains(s, "head abcdef012345\n") ||
		!strings.Contains(s, "up to seq 4") || !strings.Contains(s, "anchor at seq 8 holds") {
		t.Fatal(s)
	}
	b.Reset()
	printChain(&b, domain.ChainReport{Checked: 3, Break: &domain.ChainBreak{Seq: 2, Reason: "link"}, Anchor: &domain.ChainAnchorCheck{Seq: 1, Reason: "cut"}})
	if s := b.String(); !strings.Contains(s, "broken at seq 2 (link)") || !strings.Contains(s, "seq 1: cut") {
		t.Fatal(s)
	}
	b.Reset()
	printChain(&b, domain.ChainReport{})
	if !strings.Contains(b.String(), "no signed anchor yet") {
		t.Fatal(b.String())
	}
	b.Reset()
	printTrust(&b, app.TrustReport{Project: "demo", Item: "I-4", Recorded: domain.Manifest{"b": "2", "a": "1"},
		Problems: []app.TrustProblem{{Scope: "root", Changes: []domain.TrustChange{{Path: "x", Kind: "changed"}}, Fix: "do it"}}}, true)
	if s := b.String(); !strings.Contains(s, "instruction files of I-4: 2 files") || strings.Index(s, "  a") > strings.Index(s, "  b") || !strings.Contains(s, "fix: do it") {
		t.Fatal(s)
	}
	b.Reset()
	printTrust(&b, app.TrustReport{Project: "demo"}, false)
	if !strings.Contains(b.String(), "nothing would refuse") {
		t.Fatal(b.String())
	}
}

func TestVerifyOffline(t *testing.T) {
	ctx := context.Background()
	t.Setenv("BARNRAISE_CONFIG_DIR", t.TempDir())
	db := filepath.Join(t.TempDir(), "barnraise.db")
	st, err := sqlite.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = st.Append(ctx, domain.NewEvent("e1", domain.EvJournal, "p", "", "m", "a", time.Now(), domain.Payload{}))
	st.Close()
	rep, err := verifyOffline(ctx, db, 0)
	if err != nil || !rep.OK() || rep.Checked != 1 {
		t.Fatalf("%+v %v", rep, err)
	}
	if _, err := verifyOffline(ctx, filepath.Join(t.TempDir(), "none.db"), 0); err == nil {
		t.Fatal("missing state")
	}
	if !daemonDown(errString("dial unix: connect: connection refused")) || daemonDown(errString("forbidden")) {
		t.Fatal("daemonDown")
	}
	if short("abc") != "abc" {
		t.Fatal("short")
	}
}

func TestPreToolHookFailsClosedOnTamperedPolicy(t *testing.T) {
	dir := t.TempDir()
	pol := filepath.Join(dir, "policy.json")
	_ = os.WriteFile(pol, []byte(`{}`), 0o600)
	t.Setenv("BARNRAISE_POLICY", pol)
	t.Setenv(domain.PolicyHashEnv, strings.Repeat("0", 64))
	t.Setenv("BARNRAISE_SOCKET", filepath.Join(dir, "none.sock"))
	var out bytes.Buffer
	e := &env{out: &out, errOut: &out, in: strings.NewReader(`{"tool_name":"Bash","tool_input":{"command":"ls"}}`)}
	c := e.hookCmd()
	c.SetArgs([]string{"pre-tool"})
	if err := c.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"permissionDecision":"deny"`) || !strings.Contains(out.String(), "not the one Barnraise wrote") {
		t.Fatalf("hook output %s", out.String())
	}
}

type errString string

func (e errString) Error() string { return string(e) }
