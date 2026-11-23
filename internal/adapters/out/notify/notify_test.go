package notify

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/adapters/out/webhook"
	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

type capture struct {
	mu     sync.Mutex
	path   string
	header http.Header
	body   map[string]any
	raw    []byte
	status int
	reply  string
}

func (c *capture) server(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.path, c.header, c.raw = r.URL.Path, r.Header, b
		c.body = nil
		json.Unmarshal(b, &c.body)
		st, reply := c.status, c.reply
		c.mu.Unlock()
		if st == 0 {
			st = 200
		}
		w.WriteHeader(st)
		io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return srv
}

var question = ports.Notification{
	Title: "Approve plan for I-42?", Body: "Login screen plan", Urgent: true,
	Link: "https://barn/i/I-42", Options: []string{"approve", "rework", "later"}, Kind: KindOwnerApproval,
}

func TestShouldInterrupt(t *testing.T) {
	for _, k := range []string{"rework_exhausted", "owner_approval", "secret", "deploy", "policy_violation", "accounts_exhausted", "SIGNING_KEY"} {
		if !ShouldInterrupt(k) {
			t.Errorf("%s should interrupt", k)
		}
	}
	for _, k := range []string{"digest", "item_integrated", "proposal", "flaky_quarantined", ""} {
		if ShouldInterrupt(k) {
			t.Errorf("%s should wait for the digest", k)
		}
	}
}

func TestWebhookSigned(t *testing.T) {
	c := &capture{}
	srv := c.server(t)
	now := time.Unix(1767323045, 0)
	w := &Webhook{URL: srv.URL, Secret: "k", Now: func() time.Time { return now }}
	if err := w.Notify(context.Background(), question); err != nil {
		t.Fatal(err)
	}
	if c.body["title"] != question.Title || c.body["kind"] != KindOwnerApproval {
		t.Fatalf("%v", c.body)
	}
	if err := webhook.VerifyAt(now, "k", c.raw, c.header.Get(webhook.HeaderSignature), c.header.Get(webhook.HeaderTimestamp), time.Minute); err != nil {
		t.Fatal(err)
	}
	c.status = 500
	if err := w.Notify(context.Background(), question); err == nil {
		t.Fatal("5xx must error")
	}
}

func TestNtfy(t *testing.T) {
	c := &capture{}
	srv := c.server(t)
	n := &Ntfy{Server: srv.URL, Topic: "owner-phone", Token: "tk", ActionURL: "https://barn/api/answer"}
	if err := n.Notify(context.Background(), question); err != nil {
		t.Fatal(err)
	}
	if c.body["topic"] != "owner-phone" || c.body["priority"].(float64) != 4 || c.body["click"] != question.Link {
		t.Fatalf("%v", c.body)
	}
	if c.header.Get("Authorization") != "Bearer tk" {
		t.Fatal("token not sent")
	}
	acts := c.body["actions"].([]any)
	if len(acts) != 3 || acts[0].(map[string]any)["action"] != "view" || acts[1].(map[string]any)["body"] != "approve" {
		t.Fatalf("%v", acts)
	}
	// Without an action url the options go into the text; normal priority.
	n2 := &Ntfy{Server: srv.URL, Topic: "t"}
	if err := n2.Notify(context.Background(), ports.Notification{Title: "x", Body: "b", Options: []string{"a", "b"}, Kind: "digest"}); err != nil {
		t.Fatal(err)
	}
	if c.body["priority"].(float64) != 3 || !strings.Contains(c.body["message"].(string), "Options: a / b") {
		t.Fatalf("%v", c.body)
	}
}

func TestTelegram(t *testing.T) {
	c := &capture{reply: `{"ok":true}`}
	srv := c.server(t)
	tg := &Telegram{Token: "123:abc", ChatID: "42", APIBase: srv.URL}
	if err := tg.Notify(context.Background(), question); err != nil {
		t.Fatal(err)
	}
	if c.path != "/bot123:abc/sendMessage" || c.body["chat_id"] != "42" || !strings.Contains(c.body["text"].(string), "Login screen plan") {
		t.Fatalf("%s %v", c.path, c.body)
	}
	kb := c.body["reply_markup"].(map[string]any)["inline_keyboard"].([]any)
	if len(kb) != 4 || kb[0].([]any)[0].(map[string]any)["callback_data"] != "approve" || kb[3].([]any)[0].(map[string]any)["url"] != question.Link {
		t.Fatalf("%v", kb)
	}
	c.reply = `{"ok":false,"description":"chat not found"}`
	if err := tg.Notify(context.Background(), question); err == nil || !strings.Contains(err.Error(), "chat not found") {
		t.Fatalf("err = %v", err)
	}
	c.status, c.reply = 401, "unauthorized"
	err := tg.Notify(context.Background(), question)
	if err == nil || strings.Contains(err.Error(), "123:abc") {
		t.Fatalf("error must not leak the token: %v", err)
	}
}

func TestSlack(t *testing.T) {
	c := &capture{}
	srv := c.server(t)
	if err := (&Slack{WebhookURL: srv.URL}).Notify(context.Background(), question); err != nil {
		t.Fatal(err)
	}
	blocks := c.body["blocks"].([]any)
	if len(blocks) != 4 || blocks[0].(map[string]any)["type"] != "header" || !strings.Contains(c.body["text"].(string), question.Title) {
		t.Fatalf("%v", c.body)
	}
}

func TestDesktop(t *testing.T) {
	var got []string
	d := &Desktop{
		LookPath: func(n string) (string, error) {
			if n == "notify-send" {
				return "/usr/bin/notify-send", nil
			}
			return "", errors.New("no")
		},
		Run: func(_ context.Context, name string, args ...string) error {
			got = append([]string{name}, args...)
			return nil
		},
	}
	if err := d.Notify(context.Background(), question); err != nil {
		t.Fatal(err)
	}
	if got[0] != "/usr/bin/notify-send" || got[2] != "--urgency=critical" || got[3] != question.Title {
		t.Fatalf("%v", got)
	}
	none := &Desktop{LookPath: func(string) (string, error) { return "", errors.New("no") }}
	if err := none.Notify(context.Background(), question); err == nil {
		t.Fatal("expected error without a notifier")
	}
}

type fn func(context.Context, ports.Notification) error

func (f fn) Notify(ctx context.Context, n ports.Notification) error { return f(ctx, n) }

func TestMultiAndQuiet(t *testing.T) {
	var mu sync.Mutex
	var sent []ports.Notification
	ok := fn(func(_ context.Context, n ports.Notification) error {
		mu.Lock()
		sent = append(sent, n)
		mu.Unlock()
		return nil
	})
	bad := fn(func(context.Context, ports.Notification) error { return errors.New("down") })
	err := Multi{ok, bad, ok}.Notify(context.Background(), question)
	if err == nil || !strings.Contains(err.Error(), "down") || len(sent) != 2 {
		t.Fatalf("err=%v sent=%d", err, len(sent))
	}

	sent = nil
	q := NewQuiet(ok)
	q.Notify(context.Background(), ports.Notification{Title: "Integrated I-1", Kind: "digest"})
	q.Notify(context.Background(), ports.Notification{Title: "New proposals", Body: "3 in app\nmore", Kind: "proposal"})
	q.Notify(context.Background(), ports.Notification{Title: "Secret blocked", Kind: KindSecret})
	if len(sent) != 1 || sent[0].Title != "Secret blocked" || q.Pending() != 2 {
		t.Fatalf("sent=%v pending=%d", sent, q.Pending())
	}
	if err := q.Flush(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 2 || sent[1].Kind != "digest" || sent[1].Body != "- Integrated I-1\n- New proposals: 3 in app" || q.Pending() != 0 {
		t.Fatalf("%+v", sent)
	}
	qb := NewQuiet(bad)
	qb.Notify(context.Background(), ports.Notification{Title: "x"})
	if qb.Flush(context.Background(), "") == nil || qb.Pending() != 1 {
		t.Fatal("failed flush must keep notifications")
	}
}

type memSecrets map[string]string

func (m memSecrets) Get(k string) (string, error) {
	if v, ok := m[k]; ok {
		return v, nil
	}
	return "", ports.ErrSecretNotFound
}
func (m memSecrets) Set(k, v string) error { m[k] = v; return nil }
func (m memSecrets) Delete(k string) error { delete(m, k); return nil }

func TestFromDefs(t *testing.T) {
	t.Setenv("BR_HOOK", "https://hooks.example/x")
	sec := memSecrets{"tg": "123:abc", "smtp": "pw"}
	n, err := FromDefs([]domain.NotifierDef{
		{Type: "telegram", Target: "42", Secret: "keychain:tg"},
		{Type: "ntfy", Target: "https://ntfy.example/phone"},
		{Type: "slack", Secret: "env:BR_HOOK"},
		{Type: "email", Target: "a@x, b@x", Secret: "smtp", Extra: map[string]string{"host": "smtp.x", "username": "me@x"}},
		{Type: "webhook", Target: "https://n8n/x"},
		{Type: "desktop"},
	}, sec)
	if err != nil {
		t.Fatal(err)
	}
	m := n.(Multi)
	if len(m) != 6 {
		t.Fatalf("%d notifiers", len(m))
	}
	if tg := m[0].(*Telegram); tg.Token != "123:abc" || tg.ChatID != "42" {
		t.Fatalf("%+v", tg)
	}
	if nt := m[1].(*Ntfy); nt.Server != "https://ntfy.example" || nt.Topic != "phone" {
		t.Fatalf("%+v", nt)
	}
	if sl := m[2].(*Slack); sl.WebhookURL != "https://hooks.example/x" {
		t.Fatalf("%+v", sl)
	}
	if em := m[3].(*Email); em.Port != 587 || em.Password != "pw" || em.From != "me@x" || len(em.To) != 2 {
		t.Fatalf("%+v", em)
	}
	_, err = FromDefs([]domain.NotifierDef{{Type: "telegram", Target: "1", Secret: "missing"}, {Type: "pigeon"}}, sec)
	if err == nil || !strings.Contains(err.Error(), "secret not found") || !strings.Contains(err.Error(), "pigeon") {
		t.Fatalf("err = %v", err)
	}
}

// fakeSMTP is a minimal SMTP server without STARTTLS.
func fakeSMTP(t *testing.T) (addr string, got chan string) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	got = make(chan string, 4)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				r := bufio.NewReader(conn)
				say := func(s string) { io.WriteString(conn, s+"\r\n") }
				say("220 fake")
				var log strings.Builder
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					cmd := strings.ToUpper(strings.TrimSpace(line))
					switch {
					case strings.HasPrefix(cmd, "EHLO"):
						say("250-fake")
						say("250 AUTH PLAIN")
					case strings.HasPrefix(cmd, "AUTH PLAIN"):
						dec, _ := base64.StdEncoding.DecodeString(strings.Fields(strings.TrimSpace(line))[2])
						log.WriteString("AUTH " + strings.ReplaceAll(string(dec), "\x00", "|") + "\n")
						say("235 ok")
					case strings.HasPrefix(cmd, "MAIL"), strings.HasPrefix(cmd, "RCPT"):
						log.WriteString(strings.TrimSpace(line) + "\n")
						say("250 ok")
					case cmd == "DATA":
						say("354 go")
						for {
							l, err := r.ReadString('\n')
							if err != nil {
								return
							}
							if l == ".\r\n" {
								break
							}
							log.WriteString(l)
						}
						say("250 queued")
					case cmd == "QUIT":
						say("221 bye")
						got <- log.String()
						return
					default:
						say("250 ok")
					}
				}
			}(conn)
		}
	}()
	return ln.Addr().String(), got
}

func TestEmail(t *testing.T) {
	addr, got := fakeSMTP(t)
	host, port, _ := net.SplitHostPort(addr)
	p := 0
	for _, c := range port {
		p = p*10 + int(c-'0')
	}
	e := &Email{Host: host, Port: p, Username: "me", Password: "pw", From: "me@x", To: []string{"you@x"}, AllowPlain: true}
	if err := e.Notify(context.Background(), question); err != nil {
		t.Fatal(err)
	}
	msg := <-got
	for _, want := range []string{"AUTH |me|pw", "MAIL FROM:<me@x>", "RCPT TO:<you@x>", "Subject: [action needed] Approve plan for I-42?", "Login screen plan", "Options: approve / rework / later"} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing %q in:\n%s", want, msg)
		}
	}
	e.AllowPlain = false
	if err := e.Notify(context.Background(), question); err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("must refuse plain text: %v", err)
	}
}
