// Package notify implements ports.Notifier for the owner's channels:
// generic webhook, ntfy (phone push), Telegram, Slack, email and desktop,
// plus fan-out and the quiet policy (what interrupts now and what waits
// for the daily digest).
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/leandrodaf/barnraise/internal/adapters/out/webhook"
	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// Kinds that interrupt the owner immediately (the design's "goes now"
// column). Everything else goes to the daily digest.
const (
	KindReworkExhausted   = "rework_exhausted"
	KindOwnerApproval     = "owner_approval"
	KindSecret            = "secret"
	KindDeploy            = "deploy"
	KindStore             = "store"
	KindSigningKey        = "signing_key"
	KindPolicyViolation   = "policy_violation"
	KindAccountsExhausted = "accounts_exhausted"
)

var interrupting = map[string]bool{
	KindReworkExhausted: true, KindOwnerApproval: true, KindSecret: true, KindDeploy: true,
	KindStore: true, KindSigningKey: true, KindPolicyViolation: true, KindAccountsExhausted: true,
}

// ShouldInterrupt reports whether a notification kind goes to the owner
// right away; other kinds wait for the digest.
func ShouldInterrupt(kind string) bool { return interrupting[strings.ToLower(strings.TrimSpace(kind))] }

var defaultClient = &http.Client{Timeout: 20 * time.Second}

// Multi fans a notification out to every notifier and joins the errors.
type Multi []ports.Notifier

// Notify sends to all notifiers concurrently.
func (m Multi) Notify(ctx context.Context, n ports.Notification) error {
	errs := make([]error, len(m))
	var wg sync.WaitGroup
	for i, x := range m {
		wg.Add(1)
		go func(i int, x ports.Notifier) {
			defer wg.Done()
			errs[i] = x.Notify(ctx, n)
		}(i, x)
	}
	wg.Wait()
	return errors.Join(errs...)
}

// Quiet applies the quiet policy: urgent notifications and interrupting
// kinds go to Next now; the rest are held until Flush sends one digest.
type Quiet struct {
	Next ports.Notifier
	mu   sync.Mutex
	held []ports.Notification
}

// NewQuiet wraps next with the quiet policy.
func NewQuiet(next ports.Notifier) *Quiet { return &Quiet{Next: next} }

// Notify sends now or holds for the digest.
func (q *Quiet) Notify(ctx context.Context, n ports.Notification) error {
	if n.Urgent || ShouldInterrupt(n.Kind) {
		return q.Next.Notify(ctx, n)
	}
	q.mu.Lock()
	q.held = append(q.held, n)
	q.mu.Unlock()
	return nil
}

// Pending returns how many notifications wait for the digest.
func (q *Quiet) Pending() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.held)
}

// Flush sends the held notifications as one digest (nothing if empty).
// On failure the notifications are kept for the next flush.
func (q *Quiet) Flush(ctx context.Context, title string) error {
	q.mu.Lock()
	held := q.held
	q.held = nil
	q.mu.Unlock()
	if len(held) == 0 {
		return nil
	}
	var b strings.Builder
	for _, n := range held {
		b.WriteString("- ")
		b.WriteString(n.Title)
		if n.Body != "" {
			b.WriteString(": ")
			b.WriteString(firstLine(n.Body))
		}
		if n.Link != "" {
			b.WriteString(" (" + n.Link + ")")
		}
		b.WriteByte('\n')
	}
	if title == "" {
		title = fmt.Sprintf("Daily summary: %d updates", len(held))
	}
	err := q.Next.Notify(ctx, ports.Notification{Title: title, Body: strings.TrimRight(b.String(), "\n"), Kind: "digest"})
	if err != nil {
		q.mu.Lock()
		q.held = append(held, q.held...)
		q.mu.Unlock()
	}
	return err
}

func firstLine(s string) string {
	if before, _, ok := strings.Cut(s, "\n"); ok {
		return before
	}
	return s
}

// postJSON posts v and fails on non-2xx.
func postJSON(ctx context.Context, c *http.Client, url string, v any, hdr map[string]string) ([]byte, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return post(ctx, c, url, "application/json", body, hdr)
}

func post(ctx context.Context, c *http.Client, url, ctype string, body []byte, hdr map[string]string) ([]byte, error) {
	if c == nil {
		c = defaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("User-Agent", "barnraise-notify")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return out, fmt.Errorf("%s answered %d: %s", redactURL(url), resp.StatusCode, truncate(strings.TrimSpace(string(out)), 200))
	}
	return out, nil
}

// redactURL hides tokens that live in paths (Telegram bot token, Slack hook).
func redactURL(u string) string {
	if i := strings.Index(u, "/bot"); i >= 0 {
		if j := strings.Index(u[i+4:], "/"); j >= 0 {
			return u[:i+4] + "***" + u[i+4+j:]
		}
	}
	if strings.Contains(u, "hooks.slack.com") {
		return "https://hooks.slack.com/services/***"
	}
	return u
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// Webhook posts the notification as JSON, signed when Secret is set
// (webhook package scheme).
type Webhook struct {
	URL    string
	Secret string
	Client *http.Client
	Now    func() time.Time
}

// Notify posts n.
func (w *Webhook) Notify(ctx context.Context, n ports.Notification) error {
	body, err := json.Marshal(n)
	if err != nil {
		return err
	}
	now := time.Now
	if w.Now != nil {
		now = w.Now
	}
	ts := strconv.FormatInt(now().Unix(), 10)
	hdr := map[string]string{webhook.HeaderTimestamp: ts}
	if w.Secret != "" {
		hdr[webhook.HeaderSignature] = webhook.Sign(w.Secret, ts, body)
	}
	_, err = post(ctx, w.Client, w.URL, "application/json", body, hdr)
	return err
}

// SecretRef resolves a secret reference: "env:NAME" reads the environment,
// "keychain:key" or a bare key reads the secrets store.
func SecretRef(secrets ports.Secrets, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", nil
	}
	if name, ok := strings.CutPrefix(ref, "env:"); ok {
		v := os.Getenv(name)
		if v == "" {
			return "", fmt.Errorf("environment variable %s is empty", name)
		}
		return v, nil
	}
	if secrets == nil {
		return "", errors.New("no secrets store to read " + ref)
	}
	return secrets.Get(strings.TrimPrefix(ref, "keychain:"))
}

// FromDefs builds the notifiers of a config, resolving secret references.
//
//	webhook:  target = URL; secret = signing secret (optional)
//	ntfy:     target = topic or full topic URL; secret = access token (optional);
//	          extra.server (default https://ntfy.sh), extra.action_url
//	telegram: target = chat id; secret = bot token; extra.api
//	slack:    target = incoming webhook URL, or secret holds the URL
//	email:    target = recipients (comma-separated); secret = SMTP password;
//	          extra.host, extra.port (587), extra.username, extra.from
//	desktop:  no settings
func FromDefs(defs []domain.NotifierDef, secrets ports.Secrets) (ports.Notifier, error) {
	var out Multi
	var errs []error
	for i, d := range defs {
		n, err := fromDef(d, secrets)
		if err != nil {
			errs = append(errs, fmt.Errorf("notification %d (%s): %w", i+1, d.Type, err))
			continue
		}
		out = append(out, n)
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return out, nil
}

func fromDef(d domain.NotifierDef, secrets ports.Secrets) (ports.Notifier, error) {
	secret, err := SecretRef(secrets, d.Secret)
	if err != nil {
		return nil, fmt.Errorf("secret %q: %w", d.Secret, err)
	}
	x := d.Extra
	switch strings.ToLower(d.Type) {
	case "webhook":
		if d.Target == "" {
			return nil, errors.New("webhook needs a target url")
		}
		return &Webhook{URL: d.Target, Secret: secret}, nil
	case "ntfy":
		if d.Target == "" {
			return nil, errors.New("ntfy needs a topic")
		}
		n := &Ntfy{Server: x["server"], Topic: d.Target, Token: secret, ActionURL: x["action_url"]}
		if strings.HasPrefix(d.Target, "http://") || strings.HasPrefix(d.Target, "https://") {
			i := strings.LastIndex(d.Target, "/")
			n.Server, n.Topic = d.Target[:i], d.Target[i+1:]
		}
		return n, nil
	case "telegram":
		if secret == "" || d.Target == "" {
			return nil, errors.New("telegram needs a chat id as target and the bot token as secret")
		}
		return &Telegram{Token: secret, ChatID: d.Target, APIBase: x["api"]}, nil
	case "slack":
		u := d.Target
		if u == "" {
			u = secret
		}
		if u == "" {
			return nil, errors.New("slack needs the incoming webhook url as target or secret")
		}
		return &Slack{WebhookURL: u}, nil
	case "email":
		port := 587
		if p := x["port"]; p != "" {
			if port, err = strconv.Atoi(p); err != nil {
				return nil, fmt.Errorf("email port %q is not a number", p)
			}
		}
		var to []string
		for t := range strings.SplitSeq(d.Target, ",") {
			if t = strings.TrimSpace(t); t != "" {
				to = append(to, t)
			}
		}
		e := &Email{Host: x["host"], Port: port, Username: x["username"], Password: secret, From: x["from"], To: to}
		if e.Host == "" || len(to) == 0 {
			return nil, errors.New("email needs extra.host and at least one recipient as target")
		}
		if e.From == "" {
			e.From = e.Username
		}
		return e, nil
	case "desktop":
		return &Desktop{}, nil
	}
	return nil, fmt.Errorf("type %q is not supported; use webhook, ntfy, telegram, slack, email or desktop", d.Type)
}
