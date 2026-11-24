package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leandrodaf/barnraise/internal/adapters/out/egress"
)

func TestEgressReceiptsCommands(t *testing.T) {
	data := t.TempDir()
	t.Setenv("BARNRAISE_DATA_DIR", data)
	dir := filepath.Join(data, "egress")
	rs, err := egress.OpenReceipts(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = rs.Record(egress.Receipt{Decision: "allow", Host: "github.com", Method: "GET", Path: "/o/r", Rule: "allow-host"})
	_, _ = rs.Record(egress.Receipt{Decision: "block", Host: "github.com", Method: "POST", Path: "/e/x", Rule: "path-rule", Detail: "d"})
	rs.Close()
	run := func(jsonOut bool, args ...string) (string, error) {
		var out bytes.Buffer
		e := &env{out: &out, errOut: &out, json: jsonOut}
		c := e.egressReceiptsCmd()
		c.SetArgs(args)
		c.SetOut(&out)
		c.SetErr(&out)
		err := c.Execute()
		return out.String(), err
	}
	if out, err := run(false, "verify"); err != nil || !strings.Contains(out, "2 receipts verified") {
		t.Fatal(out, err)
	}
	if out, err := run(false, "list", "--blocked"); err != nil || !strings.Contains(out, "path-rule  d") || strings.Contains(out, "allow-host") {
		t.Fatal(out, err)
	}
	if out, err := run(true, "list", "--last", "1"); err != nil || !strings.Contains(out, `"decision":"block"`) {
		t.Fatal(out, err)
	}
	log := filepath.Join(dir, egress.ReceiptsLog)
	b, _ := os.ReadFile(log)
	_ = os.WriteFile(log, bytes.Replace(b, []byte(`"/o/r"`), []byte(`"/o/x"`), 1), 0o600)
	if _, err := run(false, "verify"); err == nil || !strings.Contains(err.Error(), "0 receipts valid") {
		t.Fatal("tampered log verified", err)
	}
	if _, err := run(false, "verify", "--pub", filepath.Join(dir, "none")); err == nil {
		t.Fatal("missing key")
	}
	if _, err := run(false, "list", "--file", filepath.Join(dir, "none")); err == nil {
		t.Fatal("missing log")
	}
	if _, err := verifyReceiptsFile(filepath.Join(dir, "none"), filepath.Join(dir, egress.ReceiptsPubFile)); err == nil {
		t.Fatal("missing log verified")
	}
}
