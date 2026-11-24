package egress

import (
	"crypto/tls"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// intercept answers a CONNECT by terminating TLS with a certificate of the
// daemon's CA for host, then serves the requests inside as seen requests.
// The client cannot ask for another name inside the tunnel (SNI must be
// the CONNECT host) and cannot redirect a request elsewhere with its Host
// header: every request goes to the CONNECT host.
func (p *Proxy) intercept(w http.ResponseWriter, r *http.Request, host string) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijack unsupported", http.StatusInternalServerError)
		return
	}
	client, _, err := hj.Hijack()
	if err != nil {
		return
	}
	_, _ = client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
	hn := domain.HostOnly(host)
	tc := tls.Server(client, &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"http/1.1"},
		GetCertificate: func(hi *tls.ClientHelloInfo) (*tls.Certificate, error) {
			if hi.ServerName != "" && domain.HostOnly(hi.ServerName) != hn {
				return nil, errors.New("server name differs from the CONNECT host")
			}
			return p.CA.Leaf(hn)
		},
	})
	_ = tc.SetDeadline(time.Now().Add(30 * time.Second))
	if err := tc.HandshakeContext(r.Context()); err != nil {
		p.log.Debug("egress TLS handshake failed", "host", hn, "err", err)
		_ = tc.Close()
		return
	}
	_ = tc.SetDeadline(time.Time{})
	l := newOneConnListener(tc)
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			p.seen(w, req, "https", host, true)
		}),
		ReadHeaderTimeout: 30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          log.New(io.Discard, "", 0),
		ConnState: func(_ net.Conn, s http.ConnState) {
			if s == http.StateClosed || s == http.StateHijacked {
				l.finish()
			}
		},
	}
	_ = srv.Serve(l)
}

// oneConnListener hands out one connection, then blocks until it closes.
type oneConnListener struct {
	conn net.Conn
	once sync.Once
	done chan struct{}
	end  sync.Once
}

func newOneConnListener(c net.Conn) *oneConnListener {
	return &oneConnListener{conn: c, done: make(chan struct{})}
}

func (l *oneConnListener) Accept() (net.Conn, error) {
	var c net.Conn
	l.once.Do(func() { c = l.conn })
	if c != nil {
		return c, nil
	}
	<-l.done
	return nil, net.ErrClosed
}

func (l *oneConnListener) finish() { l.end.Do(func() { close(l.done) }) }

func (l *oneConnListener) Close() error { l.finish(); return nil }

func (l *oneConnListener) Addr() net.Addr { return l.conn.LocalAddr() }
