package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	pb "github.com/leandrodaf/barnraise/gen/barnraise/v1"
	"github.com/leandrodaf/barnraise/internal/domain"
)

// loginPoll is how often the command asks the machines how their login goes.
var loginPoll = time.Second

// accountLoginCmd logs one account in on many machines at the same time:
// every machine runs the provider's own login and keeps it, and this
// command shows each one's link and code and hands back the codes asked.
func (e *env) accountLoginCmd() *cobra.Command {
	var mf machineFlags
	c := &cobra.Command{Use: "login <claude|codex|account id>", Short: "Log an account in on several machines at the same time",
		Long: "Starts the provider's own login of one account on the machines named (--all: every connected machine), all at once.\n" +
			"Each machine prints where to sign in: open the link, and type the one-time code there (codex) or paste here the code the page shows (claude).\n" +
			"Every machine keeps its own login under its data dir; no credential travels between machines.\n" +
			"A provider's name starts a new account; an account id logs that account in on more machines, or again where its login expired.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := mf.check(); err != nil {
				return err
			}
			r := &pb.AccountLoginRequest{Account: args[0], Machines: mf.machines, All: mf.all}
			if domain.Contains(domain.LoginProviders, args[0]) {
				r.Account, r.Provider = "", args[0]
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 25*time.Minute)
			defer cancel()
			cl := e.client()
			res, err := cl.StartAccountLogin(ctx, req(r))
			if err != nil {
				return err
			}
			account := res.Msg.Account
			fmt.Fprintf(e.out, "Logging %s in on %d machine(s). Ctrl+C stops following; the logins go on.\n", account, len(res.Msg.Logins))
			in := bufio.NewScanner(e.in)
			shown, asked := map[string]bool{}, map[string]bool{}
			for {
				running := 0
				for _, l := range res.Msg.Logins {
					if l.Url != "" && !shown[l.Machine] {
						shown[l.Machine] = true
						fmt.Fprintf(e.out, "\n%s: open %s\n", l.Machine, l.Url)
						if l.Code != "" {
							fmt.Fprintf(e.out, "%s: and type this one-time code there: %s\n", l.Machine, l.Code)
						}
					}
					if l.State != domain.LoginRunning {
						continue
					}
					running++
					if l.WantsCode && !asked[l.Machine] {
						fmt.Fprintf(e.out, "%s: paste the code the page shows (empty: skip for now): ", l.Machine)
						if !in.Scan() {
							return errors.New("no code: the logins go on, follow them with the same command")
						}
						code := strings.TrimSpace(in.Text())
						if code == "" {
							asked[l.Machine] = true
							continue
						}
						if _, err := cl.AnswerAccountLogin(ctx, req(&pb.AccountLoginRequest{Account: account, Machines: []string{l.Machine}, Code: code})); err != nil {
							fmt.Fprintf(e.out, "%s: %v\n", l.Machine, err)
						}
					}
				}
				if running == 0 {
					break
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(loginPoll):
				}
				if res, err = cl.GetAccountLogin(ctx, req(&pb.AccountLoginRequest{Account: account, Machines: mf.machines, All: mf.all})); err != nil {
					return err
				}
			}
			failed := 0
			fmt.Fprintln(e.out)
			for _, l := range res.Msg.Logins {
				switch l.State {
				case domain.LoginDone:
					fmt.Fprintf(e.out, "✔ %s: %s is logged in\n", l.Machine, account)
				default:
					failed++
					fmt.Fprintf(e.out, "✘ %s: %s %s\n", l.Machine, l.State, l.Error)
				}
			}
			if failed > 0 {
				return fmt.Errorf("%s is not logged in on %d machine(s)", account, failed)
			}
			return nil
		}}
	mf.add(c, "log in")
	return c
}
