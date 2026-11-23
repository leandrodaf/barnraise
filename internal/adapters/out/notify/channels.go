package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"runtime"
	"strings"

	"github.com/leandrodaf/barnraise/internal/ports"
)

// Ntfy pushes to a phone through an ntfy topic (ntfy.sh or self-hosted).
type Ntfy struct {
	Server string // default https://ntfy.sh
	Topic  string
	Token  string // optional access token
	// ActionURL, when set, turns Options into "http" buttons that POST the
	// chosen option to it; without it options are listed in the message.
	ActionURL string
	Client    *http.Client
}

type ntfyAction struct {
	Action string            `json:"action"`
	Label  string            `json:"label"`
	URL    string            `json:"url"`
	Method string            `json:"method,omitempty"`
	Body   string            `json:"body,omitempty"`
	Header map[string]string `json:"headers,omitempty"`
}

// Notify publishes n (JSON publishing API).
func (x *Ntfy) Notify(ctx context.Context, n ports.Notification) error {
	server := strings.TrimRight(x.Server, "/")
	if server == "" {
		server = "https://ntfy.sh"
	}
	prio := 3
	tags := []string{"barnraise"}
	if n.Urgent || ShouldInterrupt(n.Kind) {
		prio = 4
		tags = append(tags, "warning")
	}
	msg := n.Body
	var actions []ntfyAction
	if n.Link != "" {
		actions = append(actions, ntfyAction{Action: "view", Label: "Open", URL: n.Link})
	}
	if len(n.Options) > 0 {
		if x.ActionURL != "" {
			for _, o := range n.Options {
				if len(actions) == 3 { // ntfy allows three actions
					break
				}
				actions = append(actions, ntfyAction{Action: "http", Label: o, URL: x.ActionURL, Method: "POST", Body: o})
			}
		} else {
			msg = strings.TrimSpace(msg + "\n\nOptions: " + strings.Join(n.Options, " / "))
		}
	}
	payload := map[string]any{
		"topic": x.Topic, "title": n.Title, "message": msg, "priority": prio, "tags": tags,
	}
	if n.Link != "" {
		payload["click"] = n.Link
	}
	if len(actions) > 0 {
		payload["actions"] = actions
	}
	hdr := map[string]string{}
	if x.Token != "" {
		hdr["Authorization"] = "Bearer " + x.Token
	}
	_, err := postJSON(ctx, x.Client, server, payload, hdr)
	return err
}

// Telegram sends through the Bot API, with an inline keyboard for options.
// Option buttons carry the option text (up to 64 bytes) as callback data.
type Telegram struct {
	Token   string
	ChatID  string
	APIBase string // default https://api.telegram.org
	Client  *http.Client
}

type tgButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data,omitempty"`
	URL          string `json:"url,omitempty"`
}

// Notify calls sendMessage.
func (x *Telegram) Notify(ctx context.Context, n ports.Notification) error {
	base := strings.TrimRight(x.APIBase, "/")
	if base == "" {
		base = "https://api.telegram.org"
	}
	text := n.Title
	if n.Urgent || ShouldInterrupt(n.Kind) {
		text = "⚠️ " + text
	}
	if n.Body != "" {
		text += "\n\n" + n.Body
	}
	payload := map[string]any{"chat_id": x.ChatID, "text": truncate(text, 4096), "disable_web_page_preview": true}
	var rows [][]tgButton
	for _, o := range n.Options {
		rows = append(rows, []tgButton{{Text: o, CallbackData: truncBytes(o, 64)}})
	}
	if n.Link != "" {
		rows = append(rows, []tgButton{{Text: "Open", URL: n.Link}})
	}
	if len(rows) > 0 {
		payload["reply_markup"] = map[string]any{"inline_keyboard": rows}
	}
	out, err := postJSON(ctx, x.Client, base+"/bot"+x.Token+"/sendMessage", payload, nil)
	if err != nil {
		return err
	}
	var r struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if json.Unmarshal(out, &r) == nil && !r.OK {
		return fmt.Errorf("telegram refused the message: %s", r.Description)
	}
	return nil
}

func truncBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8Start(s[n]) {
		n--
	}
	return s[:n]
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

// Slack posts to an incoming webhook with blocks. Incoming webhooks cannot
// receive button clicks, so options are listed as text.
type Slack struct {
	WebhookURL string
	Client     *http.Client
}

// Notify posts n.
func (x *Slack) Notify(ctx context.Context, n ports.Notification) error {
	title := n.Title
	if n.Urgent || ShouldInterrupt(n.Kind) {
		title = ":warning: " + title
	}
	blocks := []map[string]any{
		{"type": "header", "text": map[string]any{"type": "plain_text", "text": truncate(title, 150), "emoji": true}},
	}
	if n.Body != "" {
		blocks = append(blocks, map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": truncate(n.Body, 3000)}})
	}
	if len(n.Options) > 0 {
		blocks = append(blocks, map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": "*Options:* " + strings.Join(n.Options, " · ")}})
	}
	if n.Link != "" {
		blocks = append(blocks, map[string]any{"type": "actions", "elements": []map[string]any{
			{"type": "button", "text": map[string]any{"type": "plain_text", "text": "Open"}, "url": n.Link},
		}})
	}
	_, err := postJSON(ctx, x.Client, x.WebhookURL, map[string]any{"text": title, "blocks": blocks}, nil)
	return err
}

// ErrNoDesktop is returned when no desktop notifier is installed.
var ErrNoDesktop = errors.New("no desktop notifier found (notify-send or osascript)")

// Desktop shows a local notification with notify-send (Linux) or
// osascript (macOS).
type Desktop struct {
	// Run executes the command; tests replace it. Default: exec.
	Run func(ctx context.Context, name string, args ...string) error
	// LookPath finds binaries; tests replace it. Default: exec.LookPath.
	LookPath func(string) (string, error)
}

// Notify shows n.
func (x *Desktop) Notify(ctx context.Context, n ports.Notification) error {
	look, run := x.LookPath, x.Run
	if look == nil {
		look = exec.LookPath
	}
	if run == nil {
		run = func(ctx context.Context, name string, args ...string) error {
			return exec.CommandContext(ctx, name, args...).Run()
		}
	}
	body := n.Body
	if len(n.Options) > 0 {
		body = strings.TrimSpace(body + "\nOptions: " + strings.Join(n.Options, " / "))
	}
	if p, err := look("notify-send"); err == nil {
		urgency := "normal"
		if n.Urgent || ShouldInterrupt(n.Kind) {
			urgency = "critical"
		}
		return run(ctx, p, "--app-name=Barnraise", "--urgency="+urgency, n.Title, body)
	}
	if runtime.GOOS == "darwin" {
		if p, err := look("osascript"); err == nil {
			script := fmt.Sprintf("display notification %s with title %s", appleQuote(body), appleQuote(n.Title))
			return run(ctx, p, "-e", script)
		}
	}
	return ErrNoDesktop
}

func appleQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
