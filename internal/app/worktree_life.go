package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// The life of an item worktree beyond its checkout (ADR-0040, ADR-0041):
// its own services (compose project, database, ports), the build caches it
// starts from, and the hooks of its phases.

// Markers in the session folder, which git ignores.
const (
	postCreateMarker = ".barnraise-session/post-create-done"
	releasedMarker   = ".barnraise-session/services-released"
)

// worktreeKeyOf is the item key of a directory Barnraise made under
// worktrees/<project>/ for an item (I-42, or I-42~b for the second
// attempt), or "" for a scratch checkout.
func (s *Service) worktreeKeyOf(project, dir string) string {
	if filepath.Dir(dir) != filepath.Join(s.d.DataDir, "worktrees", project) {
		return ""
	}
	key := filepath.Base(dir)
	id, _ := domain.SplitWorktreeKey(key)
	if k, _, err := domain.ParseID(id); err != nil || k != domain.KindItem {
		return ""
	}
	return key
}

// worktreeEnv is the isolation environment of an item key in dir: PORT,
// the port range, COMPOSE_PROJECT_NAME and the project's isolate.env.
func (s *Service) worktreeEnv(p *Project, key, dir string) map[string]string {
	cfg := p.Cfg()
	v := cfg.Worktree.Identity(p.Name, key, cfg.PortBase, dir)
	env, err := cfg.Worktree.Isolate.Render(v, s.includedEnv(cfg, dir))
	if err != nil {
		s.d.Log.Warn("worktree isolation environment", "item", key, "err", err)
		return v.BaseEnv()
	}
	env["BARNRAISE_ITEM"] = v.ItemID
	return env
}

// includedEnv reads the KEY=VALUE lines of the worktree's included env
// files (worktree.include), for the template function env.
func (s *Service) includedEnv(cfg *domain.ProjectConfig, dir string) map[string]string {
	out := map[string]string{}
	for _, f := range cfg.EnvFiles {
		fh, err := os.Open(filepath.Join(dir, f))
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(fh)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			k, v, ok := strings.Cut(strings.TrimPrefix(line, "export "), "=")
			if !ok {
				continue
			}
			v = strings.TrimSpace(v)
			if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
				v = v[1 : len(v)-1]
			}
			out[strings.TrimSpace(k)] = v
		}
		_ = fh.Close()
	}
	return out
}

// environ is the daemon's environment with env on top, for a command run
// in a worktree.
func environ(env map[string]string) []string {
	if len(env) == 0 {
		return nil // inherit, as before
	}
	out := os.Environ()
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, k+"="+env[k])
	}
	return out
}

// worktreeCmdEnv is the environment of a project command run for an item
// key in dir (setup, hooks, teardown, checks): the key's isolation
// environment (none for a checkout of no item) and the shared stores.
func (s *Service) worktreeCmdEnv(p *Project, key, dir string) map[string]string {
	env := map[string]string{}
	if key != "" {
		env = s.worktreeEnv(p, key, dir)
	}
	maps.Copy(env, s.StoreEnv(p))
	return env
}

// runInWorktree runs a shell command of the project's configuration in a
// worktree, the way the setup command runs: inside the setup sandbox
// profile (worktree.sandbox) where this machine can confine it. It returns
// the confinement layer used ("" when none).
func (s *Service) runInWorktree(ctx context.Context, p *Project, what, cmd, dir string, env map[string]string, timeout time.Duration) (string, error) {
	if cmd == "" || s.d.Exec == nil {
		return "", nil
	}
	name, args, backend, err := s.wrapCommand(p, ConfineSetup, p.Cfg().Sandbox.SetupSandbox(), dir, slices.Sorted(maps.Keys(env)), "sh", []string{"-c", "exec 2>&1\n" + cmd})
	if err != nil {
		return "", fmt.Errorf("%s (%s): %w", what, cmd, err)
	}
	res, err := s.d.Exec.Run(ctx, ports.Cmd{Name: name, Args: args, Dir: dir, Env: environ(env), Timeout: timeout})
	if err == nil && res.ExitCode != 0 {
		err = fmt.Errorf("exit code %d", res.ExitCode)
	}
	if err != nil {
		return backend, fmt.Errorf("%s (%s) failed: %w\n%s", what, cmd, err, truncate(lastLines(string(res.Stdout), 20), 2000))
	}
	return backend, nil
}

// runHook runs the worktree hook of a phase in an item worktree.
func (s *Service) runHook(ctx context.Context, p *Project, phase, key, dir string) error {
	hooks := p.Cfg().Worktree.Hooks
	cmd := hooks.Command(phase)
	if cmd == "" {
		return nil
	}
	env := s.worktreeCmdEnv(p, key, dir)
	env["BARNRAISE_HOOK"] = phase
	env["BARNRAISE_PROJECT"] = p.Name
	env["BARNRAISE_WORKTREE"] = dir
	timeout := hooks.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	started := s.d.Clock.Now()
	backend, err := s.runInWorktree(ctx, p, "worktree hook "+phase, cmd, dir, env, timeout)
	if err != nil {
		return err
	}
	s.d.Log.Info("worktree hook", "phase", phase, "item", key, "sandbox", def(backend, "none"), "took", s.d.Clock.Now().Sub(started).Round(time.Second))
	return nil
}

// prepareWorktree makes a new, interrupted or stripped item worktree ready:
// its build caches, seed and setup command (setupWorktree, until they
// finish once), then its post_create hook, once in the worktree's life.
func (s *Service) prepareWorktree(ctx context.Context, p *Project, dir, key string, cacheFrom []string) error {
	if err := s.setupWorktree(ctx, p, dir, key, cacheFrom); err != nil {
		return err
	}
	if p.Cfg().Worktree.Hooks.PostCreate == "" {
		return nil
	}
	marker := filepath.Join(dir, filepath.FromSlash(postCreateMarker))
	if _, err := os.Stat(marker); err == nil {
		return nil
	}
	if err := s.runHook(ctx, p, domain.HookPostCreate, key, dir); err != nil {
		return err
	}
	s.excludeFromGit(dir, "/.barnraise-session/")
	if err := os.MkdirAll(filepath.Dir(marker), 0o700); err != nil {
		return err
	}
	return os.WriteFile(marker, []byte(p.Cfg().Worktree.Hooks.PostCreate+"\n"), 0o600)
}

// buildCacheMu keeps a snapshot from being replaced while a new worktree
// is seeded from it.
var buildCacheMu sync.Mutex

// buildCacheDir keeps the build caches of the last integrated worktree.
func (s *Service) buildCacheDir(project string) string {
	return filepath.Join(s.d.DataDir, "buildcache", project)
}

// buildCacheSources are where a new item worktree takes its build caches
// from: the last integrated worktree's, else the main checkout's.
func (s *Service) buildCacheSources(p *Project) []string {
	if len(p.Cfg().Worktree.BuildCache) == 0 {
		return nil
	}
	return []string{s.buildCacheDir(p.Name), p.Cfg().Root}
}

// seedBuildCache clones each build cache directory into dir from the
// first source that has it. A filesystem without copy-on-write skips it
// (unless build_cache_fallback is copy): the build then starts cold, as
// without a cache, and never waits for a full copy nobody asked for.
func (s *Service) seedBuildCache(ctx context.Context, p *Project, dir string, sources []string) {
	wo := p.Cfg().Worktree
	if len(wo.BuildCache) == 0 || s.d.Cloner == nil {
		return
	}
	buildCacheMu.Lock()
	defer buildCacheMu.Unlock()
	for _, d := range wo.BuildCache {
		dst := filepath.Join(dir, filepath.FromSlash(d))
		if _, err := os.Lstat(dst); err == nil {
			continue // in git, or seeded before
		}
		for _, src := range sources {
			from := filepath.Join(src, filepath.FromSlash(d))
			if fi, err := os.Stat(from); err != nil || !fi.IsDir() {
				continue
			}
			started := s.d.Clock.Now()
			err := s.d.Cloner.CloneTree(ctx, from, dst, wo.BuildCacheCopy)
			switch {
			case errors.Is(err, ports.ErrCloneUnsupported):
				s.d.Log.Info("build cache skipped: the filesystem has no copy-on-write (build_cache_fallback: copy copies it in full)", "dir", d)
			case err != nil:
				s.d.Log.Warn("build cache", "dir", d, "from", from, "err", err)
			default:
				s.d.Log.Info("build cache cloned", "dir", d, "from", from, "took", s.d.Clock.Now().Sub(started).Round(time.Millisecond))
			}
			break
		}
	}
}

// snapshotBuildCache keeps the build caches of a worktree that was just
// integrated: they match main closely, so the next worktree starts warm.
func (s *Service) snapshotBuildCache(ctx context.Context, p *Project, dir string) {
	wo := p.Cfg().Worktree
	if len(wo.BuildCache) == 0 || s.d.Cloner == nil {
		return
	}
	buildCacheMu.Lock()
	defer buildCacheMu.Unlock()
	root := s.buildCacheDir(p.Name)
	for _, d := range wo.BuildCache {
		from := filepath.Join(dir, filepath.FromSlash(d))
		if fi, err := os.Stat(from); err != nil || !fi.IsDir() {
			continue
		}
		dst := filepath.Join(root, filepath.FromSlash(d))
		tmp := dst + ".new"
		_ = os.RemoveAll(tmp)
		if err := s.d.Cloner.CloneTree(ctx, from, tmp, wo.BuildCacheCopy); err != nil {
			if !errors.Is(err, ports.ErrCloneUnsupported) {
				s.d.Log.Warn("keep the build cache", "dir", d, "err", err)
			}
			continue
		}
		_ = os.RemoveAll(dst)
		if err := os.Rename(tmp, dst); err != nil {
			s.d.Log.Warn("keep the build cache", "dir", d, "err", err)
			_ = os.RemoveAll(tmp)
		}
	}
}

// How an item worktree is retired.
type retireMode int

const (
	retireRemove    retireMode = iota // the item closed, or its worktree is a leftover: it goes away
	retireIntegrate                   // the item was integrated: hook, keep its build caches, then remove
)

// retireWorktree ends an item worktree's life: post_integrate (integrated
// only), pre_remove, the isolate.teardown command and
// `docker compose -p <name> down -v` when compose was used, then the
// removal. Failures are logged: the item already ended.
func (s *Service) retireWorktree(ctx context.Context, p *Project, key, dir string, mode retireMode) {
	if _, err := os.Stat(dir); err != nil {
		return
	}
	if mode == retireIntegrate {
		if err := s.runHook(ctx, p, domain.HookPostIntegrate, key, dir); err != nil {
			s.d.Log.Warn("worktree hook", "phase", domain.HookPostIntegrate, "item", key, "err", err)
		}
		s.snapshotBuildCache(ctx, p, dir)
	}
	if err := s.runHook(ctx, p, domain.HookPreRemove, key, dir); err != nil {
		s.d.Log.Warn("worktree hook", "phase", domain.HookPreRemove, "item", key, "err", err)
	}
	s.releaseServices(ctx, p, key, dir)
	if g := p.Ports().Git; g != nil {
		if err := g.RemoveWorktree(ctx, dir); err != nil {
			s.d.Log.Warn("remove the worktree", "item", key, "err", err)
		}
	}
}

// ReleaseServices frees what an item's worktree started: the teardown
// command, then its compose project with its volumes. Once per worktree.
// The daemon also calls it for the scratch checkout of a check run for
// another machine.
func (s *Service) ReleaseServices(ctx context.Context, p *Project, itemID, dir string) {
	s.releaseServices(ctx, p, itemID, dir)
}

func (s *Service) releaseServices(ctx context.Context, p *Project, key, dir string) {
	if key == "" || s.d.Exec == nil {
		return
	}
	marker := filepath.Join(dir, filepath.FromSlash(releasedMarker))
	if _, err := os.Stat(marker); err == nil {
		return
	}
	iso := p.Cfg().Worktree.Isolate
	compose := iso.ComposeMode() == "on" || (iso.ComposeMode() == "auto" && hasComposeFile(dir))
	if iso.Teardown == "" && !compose {
		return
	}
	env := s.worktreeCmdEnv(p, key, dir)
	timeout := iso.TeardownTimeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	if iso.Teardown != "" {
		if _, err := s.runInWorktree(ctx, p, "worktree teardown", iso.Teardown, dir, env, timeout); err != nil {
			s.d.Log.Warn("worktree teardown", "item", key, "err", err)
		}
	}
	if compose {
		name := env["COMPOSE_PROJECT_NAME"]
		res, err := s.d.Exec.Run(ctx, ports.Cmd{Name: "docker", Args: []string{"compose", "-p", name, "down", "-v", "--remove-orphans"},
			Dir: dir, Env: environ(env), Timeout: timeout})
		switch {
		case err != nil:
			s.d.Log.Debug("compose down", "project", name, "err", err) // no docker here: nothing was started
		case res.ExitCode != 0:
			s.d.Log.Warn("compose down", "project", name, "exit", res.ExitCode, "out", truncate(lastLines(string(res.Stdout)+string(res.Stderr), 10), 1000))
		default:
			s.d.Log.Info("compose project down", "project", name)
		}
	}
	_ = os.MkdirAll(filepath.Dir(marker), 0o700)
	_ = os.WriteFile(marker, nil, 0o600)
}

// composeFiles are the names docker compose looks for.
var composeFiles = []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"}

func hasComposeFile(dir string) bool {
	for _, f := range composeFiles {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			return true
		}
	}
	return false
}

// mergeEnv adds what env lacks from extra.
func mergeEnv(env, extra map[string]string) {
	for k, v := range extra {
		if _, ok := env[k]; !ok {
			env[k] = v
		}
	}
}
