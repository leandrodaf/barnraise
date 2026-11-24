package egress

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// TestRealGitHubThroughTerminatingProxy reaches github.com; it runs only
// with BARNRAISE_REAL_NET=1. With the default repository scope, github.com
// is terminated by the proxy: real git must trust the local CA and fetch,
// and a write toward another repository must be refused.
func TestRealGitHubThroughTerminatingProxy(t *testing.T) {
	if os.Getenv("BARNRAISE_REAL_NET") != "1" {
		t.Skip("set BARNRAISE_REAL_NET=1 to reach github.com")
	}
	allow := append([]string(nil), DefaultAllow...)
	allow = append(allow, domain.RepoScopeRules("github", "github.com", "leandrodaf/barnraise")...)
	px := New(allow, nil)
	ca, err := LoadOrCreateCA(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := ca.TrustBundle()
	if err != nil {
		t.Fatal(err)
	}
	px.CA = ca
	if !px.Intercepts("github.com") || !px.Intercepts("api.github.com") {
		t.Fatal("github hosts are terminated under the repository scope")
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = px.Serve(ctx, l) }()
	proxyURL := "http://" + l.Addr().String()
	bundlePath := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(bundlePath, bundle, 0o644); err != nil {
		t.Fatal(err)
	}

	// Real git: fetch refs of a public repository through the proxy.
	cmd := exec.Command("git", "ls-remote", "https://github.com/git/git", "HEAD")
	cmd.Env = append(os.Environ(), "HTTPS_PROXY="+proxyURL, "https_proxy="+proxyURL, "GIT_SSL_CAINFO="+bundlePath,
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "HEAD") {
		t.Fatalf("git ls-remote through the proxy: %v\n%s", err, out)
	}
	t.Logf("git ls-remote: %s", strings.TrimSpace(string(out)))

	// A write toward another repository is refused by the proxy itself.
	pu, _ := url.Parse(proxyURL)
	cl := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(pu), TLSClientConfig: &tls.Config{RootCAs: ca.Pool()}}}
	resp, err := cl.Post("https://api.github.com/repos/git/git/issues", "application/json", strings.NewReader(`{"title":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || resp.Header.Get("Server") == "GitHub.com" {
		t.Fatalf("write to another repository: %d %v", resp.StatusCode, resp.Header)
	}
	// A read of any repository still goes through to GitHub.
	resp, err = cl.Get("https://api.github.com/repos/git/git")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusForbidden /* rate limit */ {
		t.Fatalf("read through the proxy: %d", resp.StatusCode)
	}
	t.Logf("read api.github.com: %d", resp.StatusCode)
}
