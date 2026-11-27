package egress

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestAllowed(t *testing.T) {
	p := New([]string{"github.com", "*.npmjs.org"}, nil)
	for host, want := range map[string]bool{"github.com": true, "api.github.com": true, "github.com:443": true, "evilgithub.com": false,
		"registry.npmjs.org": true, "example.com": false, "GITHUB.COM.": true} {
		if p.Allowed(host) != want {
			t.Errorf("%s: want %v", host, want)
		}
	}
}

func TestProxyHTTPAndConnect(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("hello")) }))
	defer target.Close()
	tu, _ := url.Parse(target.URL)
	p := New([]string{"allowed.test"}, nil)
	// Resolve allowed.test to the target server.
	var d net.Dialer
	p.dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if strings.HasPrefix(addr, "allowed.test") {
			addr = tu.Host
		}
		return d.DialContext(ctx, network, addr)
	}
	blocked := ""
	p.OnBlock = func(h string) { blocked = h }
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	ctx := t.Context()
	go p.Serve(ctx, l)
	pu, _ := url.Parse("http://" + l.Addr().String())
	c := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}}
	resp, err := c.Get("http://allowed.test/x")
	if err != nil || resp.StatusCode != 200 {
		t.Fatal(err, resp)
	}
	resp.Body.Close()
	resp, err = c.Get("http://evil.test/steal")
	if err != nil || resp.StatusCode != http.StatusForbidden || blocked != "evil.test" {
		t.Fatal(err, resp, blocked)
	}
	resp.Body.Close()
	// CONNECT to a blocked host is refused.
	conn, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = conn.Write([]byte("CONNECT evil.test:443 HTTP/1.1\r\nHost: evil.test:443\r\n\r\n"))
	buf := make([]byte, 64)
	n, _ := conn.Read(buf)
	conn.Close()
	if !strings.Contains(string(buf[:n]), "403") {
		t.Fatalf("CONNECT: %q", buf[:n])
	}
	// CONNECT to an allowed host tunnels.
	conn, _ = net.Dial("tcp", l.Addr().String())
	_, _ = conn.Write([]byte("CONNECT allowed.test:80 HTTP/1.1\r\nHost: allowed.test:80\r\n\r\nGET / HTTP/1.1\r\nHost: x\r\n\r\n"))
	all := make([]byte, 0, 512)
	for len(all) < 300 {
		n, err := conn.Read(buf)
		all = append(all, buf[:n]...)
		if err != nil || strings.Contains(string(all), "hello") {
			break
		}
	}
	conn.Close()
	if !strings.Contains(string(all), "200 Connection Established") || !strings.Contains(string(all), "hello") {
		t.Fatalf("tunnel: %q", all)
	}
	if p.Blocked()["evil.test"] != 1 {
		t.Fatal(p.Blocked())
	}
}

func TestNFTRules(t *testing.T) {
	r := NFTRules(1001, 7421, []int{5554})
	for _, want := range []string{"meta skuid 1001 oif lo tcp dport 7421 accept", "dport 5554", "reject"} {
		if !strings.Contains(r, want) {
			t.Fatalf("missing %q in\n%s", want, r)
		}
	}
}

// The hosts of the MCP servers agents may use change while the daemon runs:
// the proxy asks for them at each request.
func TestExtraHosts(t *testing.T) {
	p := New(nil, nil)
	hosts := []string{"mcp.example.com"}
	p.Extra = func() []string { return hosts }
	for host, want := range map[string]bool{"mcp.example.com:443": true, "eu.mcp.example.com": true, "evilmcp.example.com": false, "example.com": false} {
		if got := p.Allowed(host); got != want {
			t.Errorf("Allowed(%s) = %v, want %v", host, got, want)
		}
	}
	hosts = nil
	if p.Allowed("mcp.example.com") {
		t.Error("a host whose server is no longer allowed stays open")
	}
}
