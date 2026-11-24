// Package egress is the allow-list proxy agents' traffic is forced through
// (with nftables rules per agent user), so an agent cannot send data to
// arbitrary hosts even if a prompt injection tells it to.
//
// Most traffic is tunneled (CONNECT) and only checked by host. For the
// hosts it must see into (key-based providers, whose real key the proxy
// injects; hosts with method/path rules; hosts the project asks to
// inspect) the proxy terminates TLS with the daemon's own CA, checks the
// request, scans it for secrets and forwards it. Every decision leaves a
// signed receipt.
package egress

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"
	"sync"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// DefaultAllow are the hosts agents and their CLIs need.
var DefaultAllow = []string{
	"api.anthropic.com", "claude.ai", "statsig.anthropic.com", "console.anthropic.com",
	"api.openai.com", "chatgpt.com", "auth.openai.com",
	"openrouter.ai", // the built-in key-based provider; others come from `providers:`
	"github.com", "api.github.com", "codeload.github.com", "objects.githubusercontent.com", "raw.githubusercontent.com",
	"proxy.golang.org", "sum.golang.org", "registry.npmjs.org", "repo.maven.apache.org", "dl.google.com",
	"plugins.gradle.org", "services.gradle.org", "pypi.org", "files.pythonhosted.org", "crates.io", "static.crates.io",
}

// Proxy allows CONNECT and plain HTTP only to allowed hosts (exact or
// subdomain), and applies method/path rules where it sees the request.
type Proxy struct {
	rules   domain.EgressRules
	log     *slog.Logger
	OnBlock func(host string)
	// OnDeny is called for every request refused after the proxy saw it
	// (a path rule, a secret in it, a sealed key toward the wrong host).
	OnDeny func(Receipt)

	// CA, when set, lets the proxy terminate TLS for the hosts it must
	// see; without it every CONNECT is a plain tunnel (as before).
	CA *CA
	// Broker swaps sealed placeholders for the providers' real keys.
	Broker *Broker
	// Receipts, when set, records every decision.
	Receipts *Receipts
	// Inspect are extra hosts (and their subdomains) to terminate and scan.
	Inspect []string
	// UpstreamTLS verifies the real hosts (nil: the system roots).
	UpstreamTLS *tls.Config

	mu      sync.Mutex
	blocked map[string]int
	dial    func(ctx context.Context, network, addr string) (net.Conn, error)
	trOnce  sync.Once
	tr      *http.Transport
}

// New creates a proxy for allow_hosts entries: plain hosts and
// "METHOD host/path" rules. An entry that does not parse is left out
// (configuration is validated before it gets here).
func New(allow []string, log *slog.Logger) *Proxy {
	if log == nil {
		log = slog.Default()
	}
	var entries []string
	for _, a := range allow {
		if _, _, err := domain.ParseAllowEntry(a); err != nil {
			log.Warn("egress: allow entry ignored", "entry", a, "err", err)
			continue
		}
		entries = append(entries, a)
	}
	rules, _ := domain.CompileEgress(entries)
	var d net.Dialer
	return &Proxy{rules: rules, log: log, blocked: map[string]int{}, dial: d.DialContext}
}

// Allowed reports whether host (a port may follow) may be reached at all.
func (p *Proxy) Allowed(host string) bool { return p.rules.HostAllowed(host) }

// Rules are the compiled allow list.
func (p *Proxy) Rules() domain.EgressRules { return p.rules }

// Intercepts reports whether the proxy terminates TLS for host.
func (p *Proxy) Intercepts(host string) bool {
	if p.CA == nil {
		return false
	}
	h := domain.HostOnly(host)
	if p.Broker != nil && p.Broker.Guards(h) {
		return true
	}
	if p.rules.HasRules(h) {
		return true
	}
	for _, i := range p.Inspect {
		i = strings.ToLower(strings.TrimPrefix(i, "*."))
		if h == i || strings.HasSuffix(h, "."+i) {
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

func (p *Proxy) receipt(r Receipt) Receipt {
	if p.Receipts == nil {
		return r
	}
	out, err := p.Receipts.Record(r)
	if err != nil {
		p.log.Warn("egress receipt not written", "err", err)
	}
	return out
}

func (p *Proxy) refuse(w http.ResponseWriter, method, host, path string) {
	p.mu.Lock()
	p.blocked[host]++
	p.mu.Unlock()
	p.log.Warn("egress blocked", "host", host)
	p.receipt(Receipt{Decision: "block", Host: host, Method: method, Path: path, Rule: "not-allowed", PayloadSHA256: PayloadHash(nil)})
	if p.OnBlock != nil {
		p.OnBlock(host)
	}
	http.Error(w, "barnraise: "+host+" is not in the allowed hosts", http.StatusForbidden)
}

func (p *Proxy) deny(w http.ResponseWriter, r Receipt, why string) {
	r.Decision = "block"
	r = p.receipt(r)
	p.log.Warn("egress request refused", "host", r.Host, "method", r.Method, "path", r.Path, "rule", r.Rule)
	if p.OnDeny != nil {
		p.OnDeny(r)
	}
	http.Error(w, "barnraise: "+why, http.StatusForbidden)
}

// ServeHTTP handles CONNECT tunnels and absolute-URI HTTP requests.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.connect(w, r)
		return
	}
	if r.URL.Host == "" {
		http.Error(w, "barnraise egress proxy: use it as HTTP(S)_PROXY", http.StatusBadRequest)
		return
	}
	p.seen(w, r, "http", r.URL.Host, false)
}

func (p *Proxy) connect(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	if !p.Allowed(host) {
		p.refuse(w, http.MethodConnect, host, "")
		return
	}
	if p.Intercepts(host) {
		p.intercept(w, r, host)
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
	p.receipt(Receipt{Decision: "allow", Host: host, Method: http.MethodConnect, Rule: "tunnel", PayloadSHA256: PayloadHash(nil)})
	_, _ = client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
	if buf.Reader.Buffered() > 0 {
		_, _ = io.CopyN(up, buf, int64(buf.Reader.Buffered()))
	}
	go pipe(up, client)
	pipe(client, up)
}

func (p *Proxy) transport() *http.Transport {
	p.trOnce.Do(func() {
		cfg := p.UpstreamTLS
		if cfg == nil {
			cfg = &tls.Config{MinVersion: tls.VersionTLS12}
		}
		p.tr = &http.Transport{DialContext: p.dial, TLSClientConfig: cfg, ForceAttemptHTTP2: true,
			ResponseHeaderTimeout: 10 * time.Minute, IdleConnTimeout: 90 * time.Second, MaxIdleConnsPerHost: 8}
	})
	return p.tr
}

// seen handles a request the proxy can read: plain HTTP, or one inside a
// TLS connection it terminated (terminated true). host carries the port
// when the client gave one.
func (p *Proxy) seen(w http.ResponseWriter, r *http.Request, scheme, host string, terminated bool) {
	hn := domain.HostOnly(host)
	rc := Receipt{Host: hn, Method: r.Method, Path: r.URL.Path}
	ok, rule := p.rules.Allow(r.Method, host, r.URL.Path)
	if !ok && rule == "not-allowed" {
		p.refuse(w, r.Method, hn, r.URL.Path)
		return
	}
	if !ok {
		rc.Rule, rc.PayloadSHA256 = rule, PayloadHash(nil)
		p.deny(w, rc, fmt.Sprintf("%s %s%s is not allowed by the method/path rules of %s", r.Method, hn, r.URL.Path, hn))
		return
	}
	rc.Rule = rule
	provider := terminated && p.Broker != nil && p.Broker.Guards(hn)
	var body []byte
	var bodyHash hash.Hash
	scanned := false
	if r.Body != nil && r.Body != http.NoBody && !provider {
		data, err := io.ReadAll(io.LimitReader(r.Body, MaxInspectBody+1))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if int64(len(data)) <= MaxInspectBody {
			body, scanned = data, true
			r.Body = io.NopCloser(bytes.NewReader(data))
		} else {
			// Too large to hold: pass it on unscanned, hashing as it goes.
			bodyHash = sha256.New()
			r.Body = struct {
				io.Reader
				io.Closer
			}{io.TeeReader(io.MultiReader(bytes.NewReader(data), r.Body), bodyHash), r.Body}
			rc.Rule += " (body too large to scan)"
		}
	} else if r.Body != nil && r.Body != http.NoBody {
		bodyHash = sha256.New()
		r.Body = struct {
			io.Reader
			io.Closer
		}{io.TeeReader(r.Body, bodyHash), r.Body}
	} else {
		scanned = true
	}
	if p.Broker != nil {
		parts := [][]byte{[]byte(r.URL.String()), headerBytes(r.Header)}
		if !provider {
			parts = append(parts, body)
		}
		if p.Broker.Foreign(hn, parts...) {
			rc.Rule, rc.PayloadSHA256 = "sealed-key-foreign-host", PayloadHash(body)
			p.deny(w, rc, "a sealed provider key cannot be sent to "+hn)
			return
		}
	}
	gz := strings.Contains(strings.ToLower(r.Header.Get("Content-Encoding")), "gzip")
	if found := scanRequest(r.URL, r.Header, body, gz); len(found) > 0 {
		f := found[0]
		rc.Rule, rc.Detail, rc.PayloadSHA256 = "secret:"+f.Rule, f.File+": "+f.Redacted, PayloadHash(body)
		p.deny(w, rc, "the request carries what looks like a secret ("+f.Rule+")")
		return
	}
	if terminated && p.Broker != nil {
		p.Broker.Swap(hn, r.Header)
	}
	// The receipt is written when the upstream answers (the request body
	// has been sent by then), before the client reads the response.
	var once sync.Once
	record := func(detail string) {
		once.Do(func() {
			rc.Decision, rc.Detail = "allow", detail
			switch {
			case scanned:
				rc.PayloadSHA256 = PayloadHash(body)
			case bodyHash != nil:
				rc.PayloadSHA256 = hex.EncodeToString(bodyHash.Sum(nil))
			}
			p.receipt(rc)
		})
	}
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme, pr.Out.URL.Host, pr.Out.Host = scheme, host, hostHeader(scheme, host)
		},
		Transport:      p.transport(),
		FlushInterval:  -1,
		ModifyResponse: func(*http.Response) error { record(""); return nil },
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			record("upstream error")
			http.Error(w, err.Error(), http.StatusBadGateway)
		},
	}
	rp.ServeHTTP(w, r)
	record("")
}

// hostHeader drops the scheme's default port from the Host header.
func hostHeader(scheme, host string) string {
	if h, port, err := net.SplitHostPort(host); err == nil && ((scheme == "https" && port == "443") || (scheme == "http" && port == "80")) {
		return h
	}
	return host
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
