package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	pb "github.com/leandrodaf/barnraise/gen/barnraise/v1"
	"github.com/leandrodaf/barnraise/internal/paths"
)

// addCmd is the panel's "Add a project" from a terminal: one command from
// a repository address (or a folder) to a project at work.
func (e *env) addCmd() *cobra.Command {
	var token, lang string
	var tokenStdin bool
	c := &cobra.Command{Use: "add <repository or folder>", Short: "Add a project from its repository address or a folder: clone, configure, register",
		Long: "Clones the repository (owner/name, github.com/owner/name, an HTTPS or SSH address) into Barnraise's data folder,\n" +
			"or takes a folder of this machine, writes its barnraise.yaml from what it holds when it has none, and registers it.\n" +
			"It starts the daemon first when it is not running. A private repository asks for a token, kept in the keychain\n" +
			"for that repository only. What this machine still lacks to run agents is listed, and the project waits paused.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			src := args[0]
			// A relative folder is this shell's: the daemon needs it absolute.
			if fi, err := os.Stat(src); err == nil && fi.IsDir() {
				if abs, err := filepath.Abs(src); err == nil {
					src = abs
				}
			}
			if tokenStdin {
				b, err := bufio.NewReader(e.in).ReadString('\n')
				if err != nil && b == "" {
					return fmt.Errorf("reading the token from stdin: %w", err)
				}
				token = strings.TrimSpace(b)
			}
			if err := e.ensureDaemon(); err != nil {
				return err
			}
			if lang == "" {
				lang = detectLang()
			}
			for {
				ctx, cancel := context.WithTimeout(cmd.Context(), 16*time.Minute)
				fmt.Fprintf(e.out, "Adding %s…\n", src)
				res, err := e.client().AddProject(ctx, req(&pb.AddProjectRequest{Source: src, Token: token, Language: lang}))
				cancel()
				if err != nil {
					return err
				}
				r := res.Msg
				if r.NeedsToken {
					fmt.Fprintln(e.out, "This machine cannot reach the repository: it is private or needs a login.")
					if r.TokenUrl != "" {
						fmt.Fprintf(e.out, "Create a token here (every permission is filled in):\n  %s\n", r.TokenUrl)
					}
					if token != "" || !isTerminal(e.in) {
						return fmt.Errorf("no access: %s", firstLine(r.Detail))
					}
					fmt.Fprint(e.out, "Paste the token (empty to stop): ")
					line, _ := bufio.NewReader(e.in).ReadString('\n')
					if token = strings.TrimSpace(line); token == "" {
						return nil
					}
					continue
				}
				fmt.Fprintf(e.out, "✔ %s is on this machine (%s).\n", r.Project, r.Root)
				if r.Configured {
					fmt.Fprintf(e.out, "  barnraise.yaml was written from the repository; `barnraise explain -p %s` shows it.\n", r.Project)
				}
				if r.Paused {
					fmt.Fprintf(e.out, "  %s waits paused: this machine is not ready for its agents yet:\n", r.Project)
					for _, p := range r.NotReady {
						fmt.Fprintf(e.out, "  ✘ %s\n", p)
					}
					fmt.Fprintf(e.out, "  Then: barnraise resume -p %s\n", r.Project)
				}
				fmt.Fprintln(e.out, "  Next: `barnraise panel` opens the panel, already signed in.")
				return nil
			}
		}}
	c.Flags().StringVar(&lang, "language", "", "language people read Barnraise in, for a new barnraise.yaml (pt-BR, en…)")
	c.Flags().BoolVar(&tokenStdin, "token-stdin", false, "read a token for a private repository from stdin")
	return c
}

// ensureDaemon starts this machine's daemon when it is not running, as
// `barnraise up` does.
func (e *env) ensureDaemon() error {
	if e.daemonRunning() {
		return nil
	}
	if _, err := ensureOwnerToken(); err != nil {
		return err
	}
	fmt.Fprintln(e.out, "Starting the daemon of this machine…")
	if err := startBackground(); err != nil {
		return err
	}
	deadline := time.Now().Add(90 * time.Second)
	for !e.daemonRunning() {
		if time.Now().After(deadline) {
			return fmt.Errorf("the daemon did not start; see %s", filepath.Join(paths.DataDir(), "daemon.log"))
		}
		time.Sleep(300 * time.Millisecond)
	}
	return nil
}

// isTerminal tells whether r is a terminal someone types in.
func isTerminal(r any) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
