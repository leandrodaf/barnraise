package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// conversationView mirrors GET /v1/conversation.
type conversationView struct {
	SessionID string `json:"session_id"`
	Status    struct {
		Running   bool   `json:"running"`
		Busy      bool   `json:"busy"`
		RemoteURL string `json:"remote_url"`
		Remote    string `json:"remote"`
		Provider  string `json:"provider"`
		Err       string `json:"error"`
	} `json:"status"`
	Keep      bool   `json:"keep"`
	Name      string `json:"name"`
	Available bool   `json:"available"`
	Why       string `json:"why"`
	Account   string `json:"account"`
	Accounts  []struct {
		ID       string  `json:"id"`
		Provider string  `json:"provider"`
		Email    string  `json:"email"`
		Week     float64 `json:"week_pct"`
		Usable   bool    `json:"usable"`
		Why      string  `json:"why"`
	} `json:"accounts"`
	Entries []struct {
		Seq    int    `json:"seq"`
		Role   string `json:"role"`
		Text   string `json:"text"`
		Tool   string `json:"tool"`
		Input  string `json:"input"`
		Failed bool   `json:"failed"`
	} `json:"entries"`
}

// postJSON sends a JSON body to the daemon.
func (e *env) postJSON(ctx context.Context, path string, in, out any) error {
	cl := e.client()
	b, _ := json.Marshal(in)
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, cl.BaseURL+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	r.Header.Set("Content-Type", "application/json")
	if cl.Token != "" {
		r.Header.Set("Authorization", "Bearer "+cl.Token)
	}
	resp, err := cl.HTTP.Do(r)
	if err != nil {
		return fmt.Errorf("the daemon is not reachable (`barnraise up` starts it): %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		var m map[string]string
		if json.Unmarshal(body, &m) == nil && m["error"] != "" {
			return fmt.Errorf("%s", m["error"])
		}
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(body, out)
}

func (e *env) conversation(ctx context.Context, after int) (*conversationView, error) {
	var v conversationView
	err := e.apiJSON(ctx, http.MethodGet, "/v1/conversation", url.Values{"after": {strconv.Itoa(after)}}, &v)
	return &v, err
}

func (e *env) printEntries(v *conversationView) int {
	last := -1
	for _, en := range v.Entries {
		last = en.Seq
		switch en.Role {
		case "user":
			fmt.Fprintln(e.out, "You: "+en.Text)
		case "assistant":
			fmt.Fprintln(e.out, "Barnraise: "+en.Text)
		case "tool":
			mark := "·"
			if en.Failed {
				mark = "✗"
			}
			fmt.Fprintf(e.out, "  %s %s %s\n", mark, en.Tool, en.Input)
		}
	}
	return last + 1
}

// conversationCmd: the owner's one conversation, the same one the panel and
// the Claude app on a phone show.
func (e *env) conversationCmd() *cobra.Command {
	c := &cobra.Command{Use: "chat [message]", Short: "Talk to Barnraise's AI about everything in your projects (the same conversation as the panel and your phone)",
		Long: "One conversation per machine, kept running by the daemon. It reads every project, the code and Barnraise itself,\n" +
			"and acts on what you ask: focus, pause, priorities, new items, answers. It asks before what cannot be undone.\n" +
			"With a message it sends it and prints the answer; without one it opens an interactive prompt (quit to leave:\n" +
			"the conversation stays). Open it on your phone with the Remote Control link of `barnraise chat status`.",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			v, err := e.conversation(ctx, 0)
			if err != nil {
				return err
			}
			if !v.Available {
				return fmt.Errorf("%s", v.Why)
			}
			next := len(v.Entries)
			if len(args) > 0 {
				_, err := e.say(ctx, strings.Join(args, " "), next)
				return err
			}
			if len(v.Entries) > 6 {
				v.Entries = v.Entries[len(v.Entries)-6:]
			}
			e.printEntries(v)
			fmt.Fprintln(e.out, "(quit to leave; the conversation stays)")
			sc := bufio.NewScanner(e.in)
			for {
				fmt.Fprint(e.out, "You: ")
				if !sc.Scan() {
					return nil
				}
				line := strings.TrimSpace(sc.Text())
				if line == "" {
					continue
				}
				if l := strings.ToLower(line); l == "quit" || l == "exit" || l == "sair" {
					return nil
				}
				if next, err = e.say(ctx, line, next); err != nil {
					fmt.Fprintln(e.out, "Barnraise:", err)
				}
			}
		}}
	c.AddCommand(e.conversationStatusCmd(), e.conversationActionCmd("new", "Start a new conversation in place of this one"),
		e.conversationActionCmd("stop", "Stop the session (the conversation resumes on the next message)"),
		e.conversationActionCmd("interrupt", "Stop the answer under way"), e.conversationKeepCmd(), e.conversationAccountCmd(), e.conversationPairCmd())
	return c
}

// say sends a message and prints what follows until the turn ends.
func (e *env) say(ctx context.Context, text string, next int) (int, error) {
	if err := e.postJSON(ctx, "/v1/conversation/messages", map[string]string{"text": text}, nil); err != nil {
		return next, err
	}
	// The message shows in the transcript first; then the answer until the
	// session is no longer busy.
	idle := 0
	deadline := time.Now().Add(30 * time.Minute)
	sawAnswer := false
	for time.Now().Before(deadline) {
		time.Sleep(time.Second)
		v, err := e.conversation(ctx, next)
		if err != nil {
			return next, err
		}
		for _, en := range v.Entries {
			if en.Role == "assistant" {
				sawAnswer = true
			}
		}
		// The owner's own message is not printed back.
		filtered := v.Entries[:0]
		for _, en := range v.Entries {
			if !(en.Role == "user" && en.Text == strings.TrimSpace(text)) {
				filtered = append(filtered, en)
			}
		}
		if len(v.Entries) > 0 {
			next = v.Entries[len(v.Entries)-1].Seq + 1
		}
		v.Entries = filtered
		e.printEntries(v)
		if !v.Status.Running {
			return next, fmt.Errorf("the session stopped: %s", v.Status.Err)
		}
		if v.Status.Busy {
			idle = 0
			continue
		}
		if idle++; sawAnswer && idle >= 3 {
			return next, nil
		}
	}
	return next, nil
}

func (e *env) conversationStatusCmd() *cobra.Command {
	return &cobra.Command{Use: "status", Short: "Whether the conversation runs, and the link that opens it on your phone", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			v, err := e.conversation(cmd.Context(), 0)
			if err != nil {
				return err
			}
			if !v.Available {
				return fmt.Errorf("%s", v.Why)
			}
			state := "stopped (the next message starts it)"
			switch {
			case v.Status.Busy:
				state = "answering"
			case v.Status.Running:
				state = "running"
			}
			fmt.Fprintf(e.out, "Conversation %s · %s · %d entries\n", v.Name, state, len(v.Entries))
			if v.Status.RemoteURL != "" {
				fmt.Fprintln(e.out, "On your phone (Claude app) or browser: "+v.Status.RemoteURL)
			}
			if v.Status.Remote != "" {
				fmt.Fprintf(e.out, "ChatGPT Remote Control: %s (pair the ChatGPT app once with `barnraise chat pair`)\n", v.Status.Remote)
			}
			if v.Keep {
				fmt.Fprintln(e.out, "Kept running from the daemon's start.")
			}
			if v.Status.Err != "" {
				fmt.Fprintln(e.out, "Last stop: "+v.Status.Err)
			}
			return nil
		}}
}

func (e *env) conversationActionCmd(action, short string) *cobra.Command {
	return &cobra.Command{Use: action, Short: short, Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := e.postJSON(cmd.Context(), "/v1/conversation/"+action, map[string]string{}, nil); err != nil {
				return err
			}
			fmt.Fprintln(e.out, "Done.")
			return nil
		}}
}

func (e *env) conversationKeepCmd() *cobra.Command {
	return &cobra.Command{Use: "keep on|off", Short: "Keep the conversation running from the daemon's start, for your phone to find it", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			action := map[string]string{"on": "keep", "off": "release"}[args[0]]
			if action == "" {
				return fmt.Errorf("keep on or keep off")
			}
			if err := e.postJSON(cmd.Context(), "/v1/conversation/"+action, map[string]string{}, nil); err != nil {
				return err
			}
			fmt.Fprintln(e.out, "Done.")
			return nil
		}}
}

func (e *env) conversationAccountCmd() *cobra.Command {
	return &cobra.Command{Use: "account [id|own]", Short: "List the accounts the conversation can run as, or switch to one (the phone shows it in that account's Claude app)",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				id := args[0]
				if id == "own" {
					id = ""
				}
				if err := e.postJSON(cmd.Context(), "/v1/conversation/account", map[string]string{"account": id}, nil); err != nil {
					return err
				}
			}
			v, err := e.conversation(cmd.Context(), 1<<30)
			if err != nil {
				return err
			}
			for _, a := range v.Accounts {
				mark, id := " ", a.ID
				if a.ID == v.Account {
					mark = "●"
				}
				if id == "" {
					id = "own"
				}
				line := fmt.Sprintf("%s %-10s %-7s %s", mark, id, a.Provider, a.Email)
				if a.ID != "" && a.ID != "codex" && a.Usable {
					line += fmt.Sprintf("  week %.0f%%", a.Week)
				}
				if a.Why != "" {
					line += "  (" + a.Why + ")"
				}
				fmt.Fprintln(e.out, line)
			}
			return nil
		}}
}

func (e *env) conversationPairCmd() *cobra.Command {
	return &cobra.Command{Use: "pair", Short: "A code that pairs the ChatGPT app on your phone with this machine (conversation on a ChatGPT account)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var p struct {
				Manual  string    `json:"manual"`
				Expires time.Time `json:"expires"`
			}
			if err := e.postJSON(cmd.Context(), "/v1/conversation/pair", map[string]string{}, &p); err != nil {
				return err
			}
			fmt.Fprintf(e.out, "In the ChatGPT app: Codex, connect a computer, and type %s (valid until %s).\n", p.Manual, p.Expires.Local().Format("15:04"))
			return nil
		}}
}
