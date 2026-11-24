package egress

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// seenUpstream records what the real host received.
type seenUpstream struct {
	mu   sync.Mutex
	auth []string
	path []string
}

func (s *seenUpstream) last() (string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.auth) == 0 {
		return "", ""
	}
	return s.auth[len(s.auth)-1], s.path[len(s.path)-1]
}

func (s *seenUpstream) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.path)
}

type mitmRig struct {
	proxy      *Proxy
	proxyCA    *CA
	upstreamCA *CA
	up         *seenUpstream
	client     *http.Client
	addr       string
	dir        string
	denied     []Receipt
	mu         sync.Mutex
}

func newMitmRig(t *testing.T) *mitmRig {
	t.Helper()
	upCA, err := NewCA(nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := &seenUpstream{}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		seen.mu.Lock()
		seen.auth = append(seen.auth, r.Header.Get("Authorization"))
		seen.path = append(seen.path, r.Host+r.URL.Path)
		seen.mu.Unlock()
		_, _ = w.Write([]byte("upstream:" + r.Host))
	}))
	srv.TLS = &tls.Config{GetCertificate: func(hi *tls.ClientHelloInfo) (*tls.Certificate, error) { return upCA.Leaf(hi.ServerName) }}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	plain := httptest.NewServer(srv.Config.Handler)
	t.Cleanup(plain.Close)
	tlsAddr, plainAddr := srv.Listener.Addr().String(), plain.Listener.Addr().String()

	pxCA, err := NewCA(nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	rcpt, err := OpenReceipts(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rcpt.Close() })
	p := New([]string{"provider.test", "inspect.test", "other.test", "plain.test", "code.test", "POST,PUT code.test/o/r/**", "BREW bad.test/x"}, nil)
	p.CA, p.Broker, p.Receipts, p.Inspect = pxCA, NewBroker([]string{"provider.test"}), rcpt, []string{"inspect.test"}
	p.UpstreamTLS = &tls.Config{RootCAs: upCA.Pool()}
	var d net.Dialer
	p.dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		h, port, _ := net.SplitHostPort(addr)
		if strings.HasSuffix(h, ".test") {
			if port == "443" {
				addr = tlsAddr
			} else {
				addr = plainAddr
			}
		}
		return d.DialContext(ctx, network, addr)
	}
	rig := &mitmRig{proxy: p, proxyCA: pxCA, upstreamCA: upCA, up: seen, dir: dir}
	p.OnDeny = func(r Receipt) { rig.mu.Lock(); rig.denied = append(rig.denied, r); rig.mu.Unlock() }
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go p.Serve(t.Context(), l)
	rig.addr = l.Addr().String()
	pu, _ := url.Parse("http://" + rig.addr)
	roots := pxCA.Pool()
	roots.AddCert(upCA.cert)
	rig.client = &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu), TLSClientConfig: &tls.Config{RootCAs: roots}}}
	return rig
}

func (rig *mitmRig) do(t *testing.T, method, u, auth, body string, hdr ...string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(method, u, strings.NewReader(body))
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := rig.client.Do(req)
	if err != nil {
		t.Fatal(method, u, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestKeyBrokerSwapsOnlyTowardItsHost(t *testing.T) {
	rig := newMitmRig(t)
	ph, release, ok := rig.proxy.Broker.Seal("provider.test", "sk-real-key")
	if !ok || !strings.HasPrefix(ph, SealedPrefix) {
		t.Fatal(ph, ok)
	}
	resp, body := rig.do(t, "POST", "https://provider.test/v1/responses", "Bearer "+ph, `{"input":"aws_secret_access_key = wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEYzz"}`)
	if resp.StatusCode != 200 || body != "upstream:provider.test" {
		t.Fatal(resp.Status, body)
	}
	if auth, _ := rig.up.last(); auth != "Bearer sk-real-key" {
		t.Fatalf("upstream got %q", auth)
	}
	// The leaf the client saw was issued by the proxy's CA.
	if !resp.TLS.VerifiedChains[0][len(resp.TLS.VerifiedChains[0])-1].Equal(rig.proxyCA.cert) {
		t.Fatal("provider host was not terminated by the proxy")
	}
	n := rig.up.count()
	// Toward another host, terminated or plain HTTP, the placeholder is refused.
	if resp, _ := rig.do(t, "GET", "https://inspect.test/x", "Bearer "+ph, ""); resp.StatusCode != http.StatusForbidden {
		t.Fatal("placeholder accepted toward inspect.test", resp.Status)
	}
	if resp, _ := rig.do(t, "GET", "http://other.test/?k="+ph, "", ""); resp.StatusCode != http.StatusForbidden {
		t.Fatal("placeholder accepted toward other.test", resp.Status)
	}
	if resp, _ := rig.do(t, "POST", "http://other.test/up", "", "key="+ph); resp.StatusCode != http.StatusForbidden {
		t.Fatal("placeholder in a body accepted", resp.Status)
	}
	if rig.up.count() != n {
		t.Fatal("a refused request reached the upstream")
	}
	release()
	release()
	rig.do(t, "GET", "https://provider.test/v1/models", "Bearer "+ph, "")
	if auth, _ := rig.up.last(); auth != "Bearer "+ph {
		t.Fatalf("released placeholder still swapped: %q", auth)
	}
	// The real key never shows in the receipts.
	data, _ := os.ReadFile(filepath.Join(rig.dir, ReceiptsLog))
	if bytes.Contains(data, []byte("sk-real-key")) || !bytes.Contains(data, []byte("sealed-key-foreign-host")) {
		t.Fatal(string(data))
	}
}

func TestTunnelForHostsThatNeedNoLook(t *testing.T) {
	rig := newMitmRig(t)
	resp, body := rig.do(t, "GET", "https://plain.test/a", "", "")
	if resp.StatusCode != 200 || body != "upstream:plain.test" {
		t.Fatal(resp.Status, body)
	}
	if !resp.TLS.VerifiedChains[0][len(resp.TLS.VerifiedChains[0])-1].Equal(rig.upstreamCA.cert) {
		t.Fatal("plain.test was terminated by the proxy; it must be tunneled untouched")
	}
	// Without a CA nothing is terminated.
	rig.proxy.CA = nil
	if rig.proxy.Intercepts("provider.test") {
		t.Fatal("intercepts without a CA")
	}
}

func TestInspectionBlocksSecrets(t *testing.T) {
	rig := newMitmRig(t)
	tok := "ghp_" + strings.Repeat("aB3dE6gH9k", 4)
	resp, _ := rig.do(t, "POST", "https://inspect.test/paste", "", "content: "+tok)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatal(resp.Status)
	}
	if resp, _ := rig.do(t, "GET", "http://other.test/?q="+tok, "", ""); resp.StatusCode != http.StatusForbidden {
		t.Fatal("secret in the query passed", resp.Status)
	}
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write([]byte("x = 1\ntoken: " + tok + "\n"))
	_ = zw.Close()
	if resp, _ := rig.do(t, "POST", "https://inspect.test/gz", "", gz.String(), "Content-Encoding", "gzip"); resp.StatusCode != http.StatusForbidden {
		t.Fatal("gzip body passed", resp.Status)
	}
	// The destination's own credential header is not a leak.
	if resp, _ := rig.do(t, "GET", "https://inspect.test/me", "token "+tok, ""); resp.StatusCode != 200 {
		t.Fatal("credential header refused", resp.Status)
	}
	if resp, _ := rig.do(t, "POST", "https://inspect.test/ok", "", "hello world"); resp.StatusCode != 200 {
		t.Fatal(resp.Status)
	}
	rig.mu.Lock()
	defer rig.mu.Unlock()
	if len(rig.denied) != 3 || rig.denied[0].Rule != "secret:github-token" || !strings.Contains(rig.denied[0].Detail, "ghp_") || strings.Contains(rig.denied[0].Detail, tok) {
		t.Fatalf("%+v", rig.denied)
	}
}

func TestLargeBodyPassesUnscanned(t *testing.T) {
	rig := newMitmRig(t)
	old := MaxInspectBody
	MaxInspectBody = 8
	defer func() { MaxInspectBody = old }()
	resp, _ := rig.do(t, "POST", "https://inspect.test/big", "", strings.Repeat("z", 64))
	if resp.StatusCode != 200 {
		t.Fatal(resp.Status)
	}
	data, _ := os.ReadFile(filepath.Join(rig.dir, ReceiptsLog))
	if !bytes.Contains(data, []byte("body too large to scan")) || !bytes.Contains(data, []byte(PayloadHash([]byte(strings.Repeat("z", 64))))) {
		t.Fatal(string(data))
	}
}

func TestPathRulesWhereTheProxySees(t *testing.T) {
	rig := newMitmRig(t)
	for _, c := range []struct {
		method, u string
		want      int
	}{
		{"POST", "https://code.test/o/r/git-receive-pack", 200},
		{"PUT", "https://code.test/o/r/x", 200},
		{"POST", "https://code.test/evil/x/git-receive-pack", 403},
		{"GET", "https://code.test/evil/x", 200},
		{"POST", "http://code.test/evil/x", 403},
		{"GET", "http://bad.test/x", 403},
	} {
		if resp, _ := rig.do(t, c.method, c.u, "", "x"); resp.StatusCode != c.want {
			t.Errorf("%s %s: %s", c.method, c.u, resp.Status)
		}
	}
	if rig.proxy.Rules().Hosts == nil || !rig.proxy.Intercepts("code.test:443") || rig.proxy.Intercepts("plain.test:443") {
		t.Fatal("interception set")
	}
	if rig.proxy.Blocked()["bad.test"] != 1 {
		t.Fatal(rig.proxy.Blocked())
	}
	// Every decision is in the signed log, and the log verifies.
	f, _ := os.Open(filepath.Join(rig.dir, ReceiptsLog))
	defer f.Close()
	n, err := VerifyReceipts(f, rig.proxy.Receipts.PublicKey())
	if err != nil || n < 6 {
		t.Fatal(n, err)
	}
}

func TestTerminatedTunnelRefusesAnotherName(t *testing.T) {
	rig := newMitmRig(t)
	conn, err := net.Dial("tcp", rig.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = conn.Write([]byte("CONNECT provider.test:443 HTTP/1.1\r\nHost: provider.test:443\r\n\r\n"))
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil || resp.StatusCode != 200 {
		t.Fatal(resp, err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(rig.proxyCA.cert)
	tc := tls.Client(conn, &tls.Config{ServerName: "evil.test", RootCAs: roots})
	if err := tc.Handshake(); err == nil {
		t.Fatal("handshake for another name succeeded")
	}
}
