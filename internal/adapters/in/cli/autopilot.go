package cli

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	pb "github.com/leandrodaf/barnraise/gen/barnraise/v1"
)

// autopilotCmd switches the autopilot: the advisor takes the owner's decisions.
func (e *env) autopilotCmd() *cobra.Command {
	var all bool
	c := &cobra.Command{Use: "autopilot [on|off]", Short: "Let the advisor take your decisions (questions, approvals, proposals); only what it cannot is left to you",
		Long: "With the autopilot on, the advisor (the project's strongest model, reading only) answers the questions agents ask,\n" +
			"approves or sends back the plans and deliveries waiting for you, and decides the proposals, each with its reasons.\n" +
			"What only you can do (an account, money, a device, an access, a personal choice) is left to you, with what to do.\n" +
			"--all switches every project of this daemon. Without arguments it shows which projects are on.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := e.ctx()
			defer cancel()
			cl := e.client()
			if len(args) == 0 {
				res, err := cl.ListProjects(ctx, req(&pb.Empty{}))
				if err != nil {
					return err
				}
				for _, p := range res.Msg.Projects {
					state := "off"
					if slices.Contains(res.Msg.Autopilot, p) {
						state = "on"
					}
					fmt.Fprintf(e.out, "%-24s autopilot %s\n", p, state)
				}
				return nil
			}
			var on bool
			switch strings.ToLower(args[0]) {
			case "on":
				on = true
			case "off":
			default:
				return errors.New("on or off")
			}
			r := &pb.AutopilotRequest{On: on, All: all}
			if !all {
				r.Project = e.proj(ctx)
			}
			res, err := cl.SetAutopilot(ctx, req(r))
			if err != nil {
				return err
			}
			failed := 0
			for _, p := range res.Msg.Projects {
				switch {
				case p.Error != "":
					failed++
					fmt.Fprintf(e.out, "✘ %s: %s\n", p.Project, p.Error)
				case p.On:
					fmt.Fprintf(e.out, "✔ %s: autopilot on; the advisor takes your decisions and leaves you only what it cannot\n", p.Project)
				default:
					fmt.Fprintf(e.out, "✔ %s: autopilot off; the decisions wait for you\n", p.Project)
				}
			}
			if failed > 0 {
				return fmt.Errorf("%d project(s) kept their autopilot", failed)
			}
			return nil
		}}
	c.Flags().BoolVar(&all, "all", false, "every project of this daemon")
	return c
}
