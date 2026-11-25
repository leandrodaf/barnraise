package app

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// The advisor only reads: it never runs a command. To confirm what it
// says (the tests are green, the goal is met), the owner runs the project's
// own checks on main from the conversation. The daemon runs them, never the
// advisor: only the commands of barnraise.yaml (a trusted file), in a clean
// checkout of main, under the same sandbox as an item's checks. The result
// comes into the conversation as a message the advisor reads next.

// checksAdvice tells the advisor how the checks reach it.
const checksAdvice = "- To confirm something that only running the code shows (the tests pass, the check is green), do not say you cannot: " +
	"ask the owner to press \"Run the checks\" under the message box. Barnraise then runs the project's checks on main in a sandbox, " +
	"and their result comes into this conversation as a message from Barnraise. Quote it when you answer.\n"

// RunChecksInChat runs the project's local checks on main for a
// conversation and posts their result into it. It returns once the run has
// started; one run per project at a time.
func (s *Service) RunChecksInChat(ctx context.Context, project string, a Actor, thread string) error {
	p, err := s.Project(project)
	if err != nil {
		return err
	}
	if err := a.Require(ScopeApprove, p.Name); err != nil {
		return err
	}
	thread = strings.ToUpper(strings.TrimSpace(thread))
	if thread == "" {
		return fmt.Errorf("%w: say in which conversation", ErrInvalid)
	}
	if !IsChat(thread) {
		p.mu.Lock()
		_, q := p.state.Questions[thread]
		_, it := p.state.Items[thread]
		_, pr := p.state.Proposals[thread]
		p.mu.Unlock()
		if !q && !it && !pr {
			return fmt.Errorf("%w: %s is not a conversation", ErrNotFound, thread)
		}
	}
	if s.d.Checks == nil || p.Ports().Git == nil {
		return fmt.Errorf("%w: this machine cannot run checks", ErrConflict)
	}
	names := localChecks(p.Cfg())
	if len(names) == 0 {
		return fmt.Errorf("%w: the project has no check that runs on this machine (checks: in barnraise.yaml)", ErrInvalid)
	}
	if _, busy := s.chatChecks.LoadOrStore(p.Name, thread); busy {
		return fmt.Errorf("%w: the checks are already running for this project", ErrConflict)
	}
	main, err := p.Ports().Git.MainHead(ctx)
	if err != nil {
		s.chatChecks.Delete(p.Name)
		return fmt.Errorf("main: %w", err)
	}
	var cmds []string
	for _, n := range names {
		cmds = append(cmds, fmt.Sprintf("%s (`%s`)", n, strings.TrimSpace(p.Cfg().Checks[n].Run)))
	}
	start := &domain.DiscussionMessage{Thread: thread, Role: domain.DiscussChecks, By: "barnraise", At: s.d.Clock.Now().UTC(),
		Text: fmt.Sprintf("Running the checks on main %s, asked by %s: %s.", short(main), actorName(a), strings.Join(cmds, ", "))}
	if err := s.emitDiscussion(ctx, p, start, "barnraise"); err != nil {
		s.chatChecks.Delete(p.Name)
		return err
	}
	go func() {
		defer s.chatChecks.Delete(p.Name)
		text := s.runMainChecks(p, main, names)
		m := &domain.DiscussionMessage{Thread: thread, Role: domain.DiscussChecks, By: "barnraise", At: s.d.Clock.Now().UTC(), Text: text}
		if err := s.emitDiscussion(context.Background(), p, m, "barnraise"); err != nil {
			s.d.Log.Warn("checks of a conversation", "project", p.Name, "thread", thread, "err", err)
		}
	}()
	return nil
}

// ChecksRunning reports whether the checks of a conversation run now for the project.
func (s *Service) ChecksRunning(project string) bool {
	_, ok := s.chatChecks.Load(project)
	return ok
}

// localChecks are the checks this machine runs itself, by name.
func localChecks(cfg *domain.ProjectConfig) []string {
	var out []string
	for n, def := range cfg.Checks {
		if (def.RunsOn == "" || def.RunsOn == "local") && strings.TrimSpace(def.Run) != "" {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// runMainChecks runs the checks in a scratch checkout of main and says how
// each one went.
func (s *Service) runMainChecks(p *Project, main string, names []string) string {
	budget := 30 * time.Minute
	for _, n := range names {
		budget += max(p.Cfg().Checks[n].Timeout, 20*time.Minute)
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	dir := s.WorktreeDir(p.Name, "_main-"+short(main)+"-chat")
	if err := p.Ports().Git.AddDetachedWorktree(ctx, dir, main); err != nil {
		return "The checks could not run: no checkout of main (" + err.Error() + ")."
	}
	defer func() { _ = p.Ports().Git.RemoveWorktree(context.Background(), dir) }()
	if err := s.SetupWorktree(ctx, p, dir); err != nil {
		return "The checks could not run: the worktree setup failed (" + truncate(err.Error(), 600) + ")."
	}
	var b strings.Builder
	passed := 0
	for _, n := range names {
		def := p.Cfg().Checks[n]
		def.Scope = "all"
		res, pending, err := s.d.Checks.Run(ctx, ports.CheckSpec{Def: def, Dir: dir, Commit: main, Branch: "main", Main: true,
			Env: s.worktreeCmdEnv(p, "", dir)})
		switch {
		case err != nil:
			fmt.Fprintf(&b, "\n### ✗ %s could not run\n%s\n", n, truncate(err.Error(), 600))
		case pending:
			fmt.Fprintf(&b, "\n### … %s was sent elsewhere\n%s\n", n, truncate(res.Summary, 600))
		case res.Passed:
			passed++
			fmt.Fprintf(&b, "\n### ✓ %s passed in %s\n", n, res.Duration.Round(time.Second))
			if sum := strings.TrimSpace(res.Summary); sum != "" {
				fmt.Fprintf(&b, "```\n%s\n```\n", truncate(sum, 1500))
			}
		default:
			fmt.Fprintf(&b, "\n### ✗ %s failed after %s\n```\n%s\n```\n", n, res.Duration.Round(time.Second), truncate(strings.TrimSpace(res.Summary), 3000))
		}
	}
	head := fmt.Sprintf("Checks on main %s: %d of %d passed.", short(main), passed, len(names))
	if passed == len(names) {
		head = fmt.Sprintf("Checks on main %s: all %d passed.", short(main), len(names))
	}
	return head + "\n" + b.String()
}
