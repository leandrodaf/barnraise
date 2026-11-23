package notify

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"github.com/leandrodaf/barnraise/internal/ports"
)

// Email sends through SMTP. Port 465 uses implicit TLS; other ports
// require STARTTLS unless AllowPlain is set (only for local relays and
// tests). Authentication is PLAIN when Username is set.
type Email struct {
	Host     string
	Port     int // default 587
	Username string
	Password string
	From     string
	To       []string
	// AllowPlain permits a server without STARTTLS.
	AllowPlain bool
	// TLSConfig overrides the TLS settings (tests).
	TLSConfig *tls.Config
	Timeout   time.Duration
	Now       func() time.Time
}

// Notify sends n as a plain-text message.
func (e *Email) Notify(ctx context.Context, n ports.Notification) error {
	if e.Host == "" || len(e.To) == 0 || e.From == "" {
		return errors.New("email needs a host, a sender and at least one recipient")
	}
	port := e.Port
	if port == 0 {
		port = 587
	}
	timeout := e.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	addr := net.JoinHostPort(e.Host, strconv.Itoa(port))
	tlsCfg := e.TLSConfig
	if tlsCfg == nil {
		tlsCfg = &tls.Config{ServerName: e.Host, MinVersion: tls.VersionTLS12}
	}
	d := net.Dialer{Timeout: timeout}
	var conn net.Conn
	var err error
	if port == 465 {
		conn, err = (&tls.Dialer{NetDialer: &d, Config: tlsCfg}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("smtp dial %s: %w", addr, err)
	}
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	} else {
		conn.SetDeadline(time.Now().Add(timeout))
	}
	c, err := smtp.NewClient(conn, e.Host)
	if err != nil {
		conn.Close()
		return err
	}
	defer c.Close()
	if port != 465 {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(tlsCfg); err != nil {
				return fmt.Errorf("smtp starttls: %w", err)
			}
		} else if !e.AllowPlain {
			return errors.New("smtp server does not offer STARTTLS; refusing to send the password in clear")
		}
	}
	if e.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", e.Username, e.Password, e.Host)); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}
	if err := c.Mail(e.From); err != nil {
		return err
	}
	for _, t := range e.To {
		if err := c.Rcpt(t); err != nil {
			return fmt.Errorf("smtp rcpt %s: %w", t, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(e.message(n)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

func (e *Email) message(n ports.Notification) []byte {
	now := time.Now
	if e.Now != nil {
		now = e.Now
	}
	subject := n.Title
	if n.Urgent || ShouldInterrupt(n.Kind) {
		subject = "[action needed] " + subject
	}
	var b strings.Builder
	h := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	h("From", e.From)
	h("To", strings.Join(e.To, ", "))
	h("Subject", mime.QEncoding.Encode("utf-8", stripCRLF(subject)))
	h("Date", now().Format(time.RFC1123Z))
	h("MIME-Version", "1.0")
	h("Content-Type", "text/plain; charset=utf-8")
	h("Content-Transfer-Encoding", "8bit")
	if n.Urgent {
		h("X-Priority", "1")
	}
	b.WriteString("\r\n")
	body := n.Body
	if len(n.Options) > 0 {
		body += "\n\nOptions: " + strings.Join(n.Options, " / ")
	}
	if n.Link != "" {
		body += "\n\n" + n.Link
	}
	// net/smtp's data writer does the dot-stuffing.
	for l := range strings.SplitSeq(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		b.WriteString(l + "\r\n")
	}
	return []byte(b.String())
}

func stripCRLF(s string) string { return strings.NewReplacer("\r", " ", "\n", " ").Replace(s) }
