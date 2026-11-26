package cli

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	pb "github.com/leandrodaf/barnraise/gen/barnraise/v1"
)

// projectState is one project as `barnraise projects` shows it.
type projectState struct {
	Name    string `json:"name"`
	Paused  bool   `json:"paused"`
	Why     string `json:"why,omitempty"`
	Working int    `json:"working"`
	Open    int    `json:"open"`
}

// projectsCmd shows which projects run and which are paused, and runs only
// some of them: `barnraise projects`, `barnraise projects only a b`.
func (e *env) projectsCmd() *cobra.Command {
	c := &cobra.Command{Use: "projects", Short: "Which projects run and which are paused; run only some of them",
		Long: "Lists the projects of this daemon: running or paused (and why), and how many agents work on each.\n" +
			"A pause holds on every machine: no agent, check or merge starts in that project anywhere.\n\n" +
			"  barnraise pause -p <name> [why]    pause one project\n" +
			"  barnraise resume -p <name>         resume it\n" +
			"  barnraise projects only <name>…    run only these, pause every other\n" +
			"  barnraise pause --all / resume --all\n\n" +
			"To keep one machine from working, whatever the project, use `barnraise standby on`.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			states, err := e.projectStates()
			if err != nil {
				return err
			}
			if e.json {
				return json.NewEncoder(e.out).Encode(states)
			}
			e.printProjects(states)
			return nil
		}}
	only := &cobra.Command{Use: "only <project>…", Short: "Run only these projects: resume them and pause every other",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			states, err := e.projectStates()
			if err != nil {
				return err
			}
			names := make([]string, len(states))
			for i, s := range states {
				names[i] = s.Name
			}
			for _, a := range args {
				if !slices.Contains(names, a) {
					return fmt.Errorf("no project %q here (projects: %s)", a, strings.Join(names, ", "))
				}
			}
			ctx, cancel := e.ctx()
			defer cancel()
			why := "running only " + strings.Join(args, ", ")
			for _, s := range states {
				keep := slices.Contains(args, s.Name)
				switch {
				case keep && s.Paused:
					if _, err := e.client().Resume(ctx, req(&pb.ProjectRef{Project: s.Name})); err != nil {
						return fmt.Errorf("%s: %w", s.Name, err)
					}
				case !keep && !s.Paused:
					if _, err := e.client().Pause(ctx, req(&pb.PauseRequest{Project: s.Name, Why: why})); err != nil {
						return fmt.Errorf("%s: %w", s.Name, err)
					}
				}
			}
			states, err = e.projectStates()
			if err != nil {
				return err
			}
			e.printProjects(states)
			return nil
		}}
	c.AddCommand(only)
	return c
}

// projectStates reads every project's pause and how many agents work on it.
func (e *env) projectStates() ([]projectState, error) {
	ctx, cancel := e.ctx()
	defer cancel()
	res, err := e.client().ListProjects(ctx, req(&pb.Empty{}))
	if err != nil {
		return nil, err
	}
	var out []projectState
	for _, name := range res.Msg.Projects {
		st, err := e.client().Status(ctx, req(&pb.ProjectRef{Project: name}))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		s := projectState{Name: name, Paused: st.Msg.Paused, Why: st.Msg.PausedWhy}
		for _, a := range st.Msg.Agents {
			if a.State == "working" || a.State == "starting" {
				s.Working++
			}
		}
		for status, n := range st.Msg.Counts {
			if status != "integrated" && status != "closed" {
				s.Open += int(n)
			}
		}
		out = append(out, s)
	}
	return out, nil
}

func (e *env) printProjects(states []projectState) {
	running := 0
	width := 0
	for _, s := range states {
		if !s.Paused {
			running++
		}
		width = max(width, len(s.Name))
	}
	fmt.Fprintf(e.out, "%d of %d projects running\n", running, len(states))
	for _, s := range states {
		mark, state := "●", "running"
		if s.Paused {
			mark, state = "‖", "paused"
		}
		line := fmt.Sprintf("  %s %-*s  %-7s  %d working, %d open", mark, width, s.Name, state, s.Working, s.Open)
		if s.Paused && s.Why != "" {
			line += "  · " + s.Why
		}
		fmt.Fprintln(e.out, line)
	}
	fmt.Fprintln(e.out, "Pause or resume one: barnraise pause -p <name> / barnraise resume -p <name>. Run only some: barnraise projects only <name>…")
}
