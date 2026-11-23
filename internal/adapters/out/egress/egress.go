// Package egress is the allow-list proxy agents' traffic is forced through
// (with nftables rules per agent user), so an agent cannot send data to
// arbitrary hosts even if a prompt injection tells it to.
package egress

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// DefaultAllow are the hosts agents and their CLIs need.
var DefaultAllow = []string{
	"api.anthropic.com", "claude.ai", "statsig.anthropic.com", "console.anthropic.com",
	"api.openai.com", "chatgpt.com", "auth.openai.com",
	"github.com", "api.github.com", "codeload.github.com", "objects.githubusercontent.com", "raw.githubusercontent.com",
	"proxy.golang.org", "sum.golang.org", "registry.npmjs.org", "repo.maven.apache.org", "dl.google.com",
	"plugins.gradle.org", "services.gradle.org", "pypi.org", "files.pythonhosted.org", "crates.io", "static.crates.io",
}

// Proxy allows CONNECT and plain HTTP only to allowed hosts (exact or subdomain).
type Proxy struct {
	allow   []string
	log     *slog.Logger
	OnBlock func(host string)
	mu      sync.Mutex
	blocked map[string]int
	dial    func(ctx context.Context, network, addr string) (net.Conn, error)
}

// New creates a proxy for the hosts.
func New(allow []string, log *slog.Logger) *Proxy {
	if log == nil {
		log = slog.Default()
	}
	var d net.Dialer
	return &Proxy{allow: normalize(allow), log: log, blocked: map[string]int{}, dial: d.DialContext}
}

func normalize(hosts []string) []string {
	var out []string
	for _, h := range hosts {
		h = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, "*.")))
		if h != "" {
			out = append(out, h)
		}
	}
	return out
}

// Allowed reports whether host (without port) may be reached.
func (p *Proxy) Allowed(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	for _, a := range p.allow {
		if host == a || strings.HasSuffix(host, "."+a) {
			return true
		}
	}
	return false
}

// Blocked returns how many times each host was refused.
func (p *Proxy) Blocked() map[string]int {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := map[string]int{}
	maps.Copy(out, p.blocked)
	return out
}

func (p *Proxy) refuse(w http.ResponseWriter, host string) {
	p.mu.Lock()
	p.blocked[host]++
	p.mu.Unlock()
	p.log.Warn("egress blocked", "host", host)
	if p.OnBlock != nil {
		p.OnBlock(host)
	}
	http.Error(w, "barnraise: "+host+" is not in the allowed hosts", http.StatusForbidden)
}

// ServeHTTP handles CONNECT tunnels and absolute-URI HTTP requests.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		host := r.Host
		if !p.Allowed(host) {
			p.refuse(w, host)
			return
		}
		up, err := p.dial(r.Context(), "tcp", host)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			up.Close()
			http.Error(w, "hijack unsupported", http.StatusInternalServerError)
			return
		}
		client, buf, err := hj.Hijack()
		if err != nil {
			up.Close()
			return
		}
		_, _ = client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
		if buf.Reader.Buffered() > 0 {
			_, _ = io.CopyN(up, buf, int64(buf.Reader.Buffered()))
		}
		go pipe(up, client)
		pipe(client, up)
		return
	}
	if r.URL.Host == "" {
		http.Error(w, "barnraise egress proxy: use it as HTTP(S)_PROXY", http.StatusBadRequest)
		return
	}
	if !p.Allowed(r.URL.Host) {
		p.refuse(w, r.URL.Host)
		return
	}
	out := r.Clone(r.Context())
	out.RequestURI = ""
	out.Header.Del("Proxy-Connection")
	out.Header.Del("Proxy-Authorization")
	tr := &http.Transport{DialContext: p.dial, ResponseHeaderTimeout: time.Minute}
	resp, err := tr.RoundTrip(out)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func pipe(dst, src net.Conn) {
	defer dst.Close()
	defer src.Close()
	_, _ = io.Copy(dst, src)
}

// Serve runs the proxy on addr until ctx ends.
func (p *Proxy) Serve(ctx context.Context, l net.Listener) error {
	hs := &http.Server{Handler: p, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		_ = hs.Close()
	}()
	err := hs.Serve(l)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

// NFTRules returns nftables rules that only let uid reach the proxy port on
// loopback (plus loopback services), rejecting every other outbound packet.
// The Android emulator needs an exception when it runs as the agent user.
func NFTRules(uid int, proxyPort int, extraLoopbackPorts []int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Barnraise egress rules for uid %d: only the allow-list proxy on 127.0.0.1:%d.\n", uid, proxyPort)
	b.WriteString("table inet barnraise {\n  chain output {\n    type filter hook output priority 0; policy accept;\n")
	fmt.Fprintf(&b, "    meta skuid %d oif lo tcp dport %d accept\n", uid, proxyPort)
	for _, p := range extraLoopbackPorts {
		fmt.Fprintf(&b, "    meta skuid %d oif lo tcp dport %d accept\n", uid, p)
	}
	fmt.Fprintf(&b, "    meta skuid %d oif lo udp dport 53 accept\n", uid)
	fmt.Fprintf(&b, "    meta skuid %d counter reject with icmpx type admin-prohibited\n", uid)
	b.WriteString("  }\n}\n")
	return b.String()
}
