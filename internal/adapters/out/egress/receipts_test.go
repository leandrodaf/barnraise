package egress

import (
	"bytes"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReceiptsChainAndVerify(t *testing.T) {
	dir := t.TempDir()
	now := func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) }
	rs, err := OpenReceipts(dir, now)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if _, err := rs.Record(Receipt{Decision: "allow", Host: "h", Method: "GET", Path: "/" + string(rune('a'+i)), Rule: "allow-host", PayloadSHA256: PayloadHash(nil)}); err != nil {
			t.Fatal(err)
		}
	}
	rs.Close()
	// Reopening continues the chain.
	rs, err = OpenReceipts(dir, now)
	if err != nil {
		t.Fatal(err)
	}
	r, _ := rs.Record(Receipt{Decision: "block", Host: "h", Method: "POST", Rule: "path-rule"})
	rs.Close()
	if r.Seq != 4 || r.Prev == "" || !r.Time.Equal(now()) {
		t.Fatalf("%+v", r)
	}
	for _, name := range []string{ReceiptsKeyFile, ReceiptsLog} {
		if st, _ := os.Stat(filepath.Join(dir, name)); st.Mode().Perm() != 0o600 {
			t.Fatal(name, st.Mode())
		}
	}
	pub, err := ReadPublicKey(filepath.Join(dir, ReceiptsPubFile))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, ReceiptsLog))
	if n, err := VerifyReceipts(bytes.NewReader(data), pub); err != nil || n != 4 {
		t.Fatal(n, err)
	}
	lines := strings.SplitAfter(strings.TrimSpace(string(data)), "\n")
	tampered := strings.Replace(string(data), `"path":"/b"`, `"path":"/z"`, 1)
	removed := lines[0] + lines[2] + lines[3]
	headless := lines[1] + lines[2] + lines[3]
	swapped := lines[0] + lines[2] + lines[1] + lines[3]
	for name, c := range map[string]string{"tampered": tampered, "removed": removed, "headless": headless, "swapped": swapped, "garbage": lines[0] + "not json\n"} {
		if n, err := VerifyReceipts(strings.NewReader(c), pub); err == nil {
			t.Errorf("%s: verified %d", name, n)
		}
	}
	// Another key does not verify.
	other, _ := OpenReceipts(t.TempDir(), nil)
	defer other.Close()
	if _, err := VerifyReceipts(bytes.NewReader(data), other.PublicKey()); err == nil {
		t.Fatal("verified with another key")
	}
	if _, err := ReadPublicKey(filepath.Join(dir, ReceiptsLog)); err == nil {
		t.Fatal("log read as a key")
	}
	if _, err := ReadPublicKey(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing key read")
	}
}

func TestReceiptsBadFiles(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, ReceiptsKeyFile), []byte("nope"), 0o600)
	if _, err := OpenReceipts(dir, nil); err == nil {
		t.Fatal("bad key accepted")
	}
	dir = t.TempDir()
	rs, _ := OpenReceipts(dir, nil)
	rs.Close()
	_ = os.WriteFile(filepath.Join(dir, ReceiptsLog), []byte("{broken\n"), 0o600)
	if _, err := OpenReceipts(dir, nil); err == nil {
		t.Fatal("broken log accepted")
	}
	_ = os.WriteFile(filepath.Join(dir, ReceiptsLog), []byte("\n\n"), 0o600)
	if rs, err := OpenReceipts(dir, nil); err != nil {
		t.Fatal(err)
	} else {
		rs.Close()
	}
}

func TestCAFilesAndBundle(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "egress")
	a, err := LoadOrCreateCA(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := LoadOrCreateCA(dir, nil)
	if err != nil || !bytes.Equal(a.CertPEM(), b.CertPEM()) {
		t.Fatal("CA not reloaded", err)
	}
	for _, name := range []string{CACertFile, CAKeyFile} {
		if st, _ := os.Stat(filepath.Join(dir, name)); st.Mode().Perm() != 0o600 {
			t.Fatal(name, st.Mode())
		}
	}
	l1, _ := a.Leaf("x.test")
	l2, _ := a.Leaf("x.test")
	ip, _ := a.Leaf("127.0.0.1")
	if l1 != l2 || len(ip.Leaf.IPAddresses) != 1 || l1.Leaf.DNSNames[0] != "x.test" {
		t.Fatal("leaf cache or names")
	}
	sys := filepath.Join(t.TempDir(), "roots.pem")
	_ = os.WriteFile(sys, []byte(strings.TrimSpace(string(b.CertPEM()))), 0o644)
	old := SystemBundlePaths
	defer func() { SystemBundlePaths = old }()
	t.Setenv("SSL_CERT_FILE", "")
	SystemBundlePaths = []string{filepath.Join(t.TempDir(), "none"), sys}
	bundle, err := a.TrustBundle()
	if err != nil || !bytes.HasSuffix(bundle, a.CertPEM()) || bytes.Count(bundle, []byte("BEGIN CERTIFICATE")) != 2 {
		t.Fatal(err, string(bundle))
	}
	SystemBundlePaths = nil
	if _, err := a.TrustBundle(); err == nil {
		t.Fatal("bundle without system roots")
	}
	_ = os.WriteFile(filepath.Join(dir, CAKeyFile), []byte("junk"), 0o600)
	if _, err := LoadOrCreateCA(dir, nil); err == nil {
		t.Fatal("junk key accepted")
	}
}

func TestBrokerUnit(t *testing.T) {
	b := NewBroker([]string{"API.example"})
	if _, _, ok := b.Seal("other.example", "k"); ok {
		t.Fatal("sealed for an unguarded host")
	}
	if _, _, ok := b.Seal("api.example", ""); ok {
		t.Fatal("sealed an empty key")
	}
	ph, release, ok := b.Seal("api.example", "real")
	if !ok || len(b.Hosts()) != 1 {
		t.Fatal(ph)
	}
	defer release()
	h := http.Header{"Authorization": {"Bearer " + ph}, "X-Other": {"plain", SealedPrefix + "short"}}
	if n := b.Swap("other.example", h.Clone()); n != 0 {
		t.Fatal("swapped toward another host")
	}
	if n := b.Swap("api.example", h); n != 1 || h.Get("Authorization") != "Bearer real" {
		t.Fatal(h)
	}
	if !b.Foreign("other.example", []byte("x"+ph)) || b.Foreign("api.example", []byte(ph)) || b.Foreign("x", []byte(SealedPrefix+strings.Repeat("0", 48))) {
		t.Fatal("foreign detection")
	}
	b.SetCABundle([]byte("pem"))
	if string(b.CABundle()) != "pem" {
		t.Fatal("bundle")
	}
}

func TestScanRequestSkipsCredentialHeaders(t *testing.T) {
	tok := "ghp_" + strings.Repeat("aB3dE6gH9k", 4)
	u, _ := url.Parse("https://h/x?a=1")
	if f := scanRequest(u, http.Header{"X-Api-Key": {tok}, "Cookie": {tok}}, nil, false); len(f) != 0 {
		t.Fatal(f)
	}
	if f := scanRequest(u, http.Header{"X-Note": {tok}}, nil, false); len(f) != 1 {
		t.Fatal(f)
	}
	if f := scanRequest(u, nil, []byte("not gzip "+tok), true); len(f) != 1 {
		t.Fatal(f)
	}
}
