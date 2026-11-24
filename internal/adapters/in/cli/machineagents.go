package cli

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	pb "github.com/leandrodaf/barnraise/gen/barnraise/v1"
)

// machineAgentsCmd sets how many agents a machine runs at once.
func (e *env) machineAgentsCmd() *cobra.Command {
	var machines []string
	c := &cobra.Command{Use: "agents <n|auto>", Short: "How many agents a machine runs at once (auto: measured from its CPUs and memory)",
		Long: "By default each machine measures it: 2 CPUs and 4 GB of memory per agent, 2 GB kept for the system.\n" +
			"A number replaces that measure on the machines named (--machine, default this one); auto goes back to it.\n" +
			"The limit is kept in each machine's daemon.yaml (max_agents).",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			n := 0
			if args[0] != "auto" {
				v, err := strconv.Atoi(args[0])
				if err != nil || v < 1 || v > 64 {
					return errors.New("a number from 1 to 64, or auto")
				}
				n = v
			}
			ctx, cancel := e.ctx()
			defer cancel()
			res, err := e.client().SetMaxAgents(ctx, req(&pb.MaxAgentsRequest{MaxAgents: int32(n), Machines: machines}))
			if err != nil {
				return err
			}
			failed := 0
			for _, m := range res.Msg.Machines {
				if !m.Ok {
					failed++
					fmt.Fprintf(e.out, "✘ %s: %s\n", m.Name, m.Error)
					continue
				}
				if n == 0 {
					fmt.Fprintf(e.out, "✔ %s measures its limit of agents again\n", m.Name)
				} else {
					fmt.Fprintf(e.out, "✔ %s runs up to %d agents at once\n", m.Name, n)
				}
			}
			if failed > 0 {
				return fmt.Errorf("%d machine(s) kept their limit", failed)
			}
			return nil
		}}
	c.Flags().StringSliceVar(&machines, "machine", nil, "the machines (repeat, or comma-separated; default: this machine)")
	return c
}

// machineShareCmd has other machines clone this project and work on it too.
func (e *env) machineShareCmd() *cobra.Command {
	var machines []string
	var all, paused bool
	c := &cobra.Command{Use: "share", Short: "Have other machines clone this project's repository and work on it too",
		Long: "Each machine named clones the project's repository (its origin) into its data folder and registers it,\n" +
			"so its agents take items of the project. The machine needs read and write access to the repository.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if all == (len(machines) > 0) {
				return errors.New("name the machines with --machine, or --all")
			}
			ctx, cancel := e.ctx()
			defer cancel()
			project := e.proj(ctx)
			res, err := e.client().ShareProject(ctx, req(&pb.ShareProjectRequest{Project: project, Machines: machines, All: all, Paused: paused}))
			if err != nil {
				return err
			}
			failed := 0
			for _, m := range res.Msg.Machines {
				if !m.Ok {
					failed++
					fmt.Fprintf(e.out, "✘ %s: %s\n", m.Name, m.Error)
					continue
				}
				fmt.Fprintf(e.out, "✔ %s works on %s now\n", m.Name, project)
			}
			if failed > 0 {
				return fmt.Errorf("%d machine(s) did not take %s", failed, project)
			}
			return nil
		}}
	c.Flags().StringSliceVar(&machines, "machine", nil, "the machines (repeat, or comma-separated)")
	c.Flags().BoolVar(&all, "all", false, "every connected machine")
	c.Flags().BoolVar(&paused, "paused", false, "register it paused there")
	return c
}
