package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	pb "github.com/leandrodaf/barnraise/gen/barnraise/v1"
	"github.com/leandrodaf/barnraise/internal/adapters/in/cli"
	"github.com/leandrodaf/barnraise/internal/adapters/out/github"
	"github.com/leandrodaf/barnraise/internal/app"
	"github.com/leandrodaf/barnraise/internal/config"
)

// A new owner's one step: paste where the project is, and Barnraise clones
// it, writes its barnraise.yaml from what the repository holds and
// registers it. Before, it took a terminal: clone, `barnraise init` with a
// dozen questions, `barnraise up`.

// addProjectTimeout bounds the clone of a large repository.
const addProjectTimeout = 15 * time.Minute

// repoSource is a repository the owner named.
type repoSource struct {
	URL  string // what git clones
	Host string // github.com…
	Path string // owner/name
	Name string // the project's name: the repository's
}

var (
	// owner/name, as GitHub shows it.
	shortRepoRe = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9-]*)/([A-Za-z0-9._-]+?)(\.git)?/?$`)
	// host/owner/name with no scheme, as copied from the address bar.
	hostRepoRe = regexp.MustCompile(`^([A-Za-z0-9.-]+\.[A-Za-z]{2,})/([^\s]+?)(\.git)?/?$`)
)

// parseRepoSource reads a repository address in the forms people paste:
// owner/name (GitHub), github.com/owner/name, https://…, git@host:owner/name
// and ssh://…. A page of the repository (…/tree/main) names the repository.
func parseRepoSource(s string) (repoSource, error) {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasPrefix(s, "-") || strings.ContainsAny(s, " \t\n") {
		return repoSource{}, fmt.Errorf("%q is not a repository address", s)
	}
	var r repoSource
	switch {
	case scpURL.MatchString(s):
		m := scpURL.FindStringSubmatch(s)
		r = repoSource{URL: s, Host: m[1], Path: m[2]}
	case strings.HasPrefix(s, "ssh://"):
		r.URL = s
		u := cloneURLs(s)
		h, p, ok := httpsHostPath(u[len(u)-1])
		if !ok {
			return repoSource{}, fmt.Errorf("%q is not a repository address", s)
		}
		r.Host, r.Path = h, p
	case strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "http://"):
		h, p, ok := httpsHostPath("https://" + strings.TrimPrefix(strings.TrimPrefix(s, "https://"), "http://"))
		if !ok {
			return repoSource{}, fmt.Errorf("%q is not a repository address", s)
		}
		r.Host, r.Path = h, p
	case shortRepoRe.MatchString(s) && !strings.Contains(strings.SplitN(s, "/", 2)[0], "."):
		m := shortRepoRe.FindStringSubmatch(s)
		r.Host, r.Path = "github.com", m[1]+"/"+m[2]
	case hostRepoRe.MatchString(s):
		m := hostRepoRe.FindStringSubmatch(s)
		r.Host, r.Path = m[1], m[2]
	default:
		return repoSource{}, fmt.Errorf("%q is not a repository address: paste it as owner/name, github.com/owner/name or the address git clones", s)
	}
	r.Path = strings.TrimSuffix(strings.Trim(r.Path, "/"), ".git")
	// A page of the repository (…/tree/main, …/-/issues): keep owner/name.
	if seg := strings.Split(r.Path, "/"); r.Host == "github.com" && len(seg) > 2 {
		r.Path = strings.Join(seg[:2], "/")
	} else if i := slices.Index(seg, "-"); i > 0 {
		r.Path = strings.Join(seg[:i], "/")
	}
	if r.URL == "" {
		r.URL = "https://" + r.Host + "/" + r.Path + ".git"
	}
	seg := strings.Split(r.Path, "/")
	if len(seg) < 2 || seg[0] == "" || seg[len(seg)-1] == "" {
		return repoSource{}, fmt.Errorf("%q names no repository (owner/name)", s)
	}
	r.Name = seg[len(seg)-1]
	if !projectNameRe.MatchString(r.Name) {
		return repoSource{}, fmt.Errorf("%q cannot be a project name", r.Name)
	}
	return r, nil
}

// localFolder is the folder the owner named, when the source is one.
func localFolder(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if after, ok := strings.CutPrefix(s, "~/"); ok {
		home, _ := os.UserHomeDir()
		s = filepath.Join(home, after)
	}
	if !filepath.IsAbs(s) {
		return "", false
	}
	fi, err := os.Stat(s)
	return filepath.Clean(s), err == nil && fi.IsDir()
}

// noAccess tells git's answers that mean "these credentials do not reach
// the repository" (or it does not exist for them: a private repository
// reads as not found).
func noAccess(msg string) bool {
	m := strings.ToLower(msg)
	for _, s := range []string{"authentication failed", "could not read username", "terminal prompts disabled", "repository not found", "permission denied", "requested url returned error: 40", "access denied", "could not read from remote repository"} {
		if strings.Contains(m, s) {
			return true
		}
	}
	return false
}

// addProjectFrom answers AddProject.
func (d *daemon) addProjectFrom(ctx context.Context, r *pb.AddProjectRequest) (*pb.AddProjectResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, addProjectTimeout)
	defer cancel()
	out := &pb.AddProjectResponse{}
	root, isDir := localFolder(r.Source)
	if !isDir && root != "" {
		return nil, fmt.Errorf("%s is not a folder of %s", root, d.df.Machine)
	}
	if isDir {
		if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
			return nil, fmt.Errorf("%s is not a git repository: Barnraise works on branches and a main (git init, and a remote to push to)", root)
		}
	} else {
		src, err := parseRepoSource(r.Source)
		if err != nil {
			return nil, err
		}
		// The repository's folder; another repository of the same name
		// (someone else's app) gets the owner in its folder's name.
		root = filepath.Join(d.dataDir, "projects", src.Name)
		if other := cloneOf(ctx, root); other != "" && !sameRepo(other, src) {
			root = filepath.Join(d.dataDir, "projects", strings.ReplaceAll(src.Path, "/", "-"))
			if other := cloneOf(ctx, root); other != "" && !sameRepo(other, src) {
				return nil, fmt.Errorf("%s already holds another repository (%s)", root, other)
			}
		}
		token := strings.TrimSpace(r.Token)
		err = d.cloneRepo(ctx, root, src.URL, token)
		// No token and no access: this machine's GitHub login may reach it.
		if err != nil && token == "" && noAccess(err.Error()) && src.Host == "github.com" {
			if gh := ghAuthToken(); gh != "" {
				err = d.cloneRepo(ctx, root, src.URL, gh)
			}
		}
		if err != nil {
			if !noAccess(err.Error()) {
				return nil, err
			}
			out.NeedsToken, out.Detail = true, err.Error()
			if src.Host == "github.com" {
				out.TokenUrl = github.TemplateURL(src.Path, false)
			}
			return out, nil
		}
		d.log.Info("a project was cloned to be added", "url", src.URL, "root", root, "token", token != "")
	}
	wrote, err := cli.AutoConfig(root, cli.AutoOptions{Language: strings.TrimSpace(r.Language)})
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load(root)
	if err != nil {
		return nil, err
	}
	if problems, err := config.Validate(root); err != nil {
		return nil, err
	} else if len(problems) > 0 {
		return nil, fmt.Errorf("barnraise.yaml in %s has problems: %s", root, strings.Join(problems, "; "))
	}
	// What would stop agents here starts the project paused, with the list.
	rctx, rcancel := context.WithTimeout(ctx, 90*time.Second)
	notReady := cli.Readiness(rctx, cfg)
	rcancel()
	note := ""
	if len(notReady) > 0 {
		note = notReadyNote + ": " + notReady[0]
	}
	if err := d.registerPaused(ctx, root, note); err != nil {
		return nil, err
	}
	out.Project, out.Root, out.Configured, out.NotReady, out.Paused = cfg.Name, root, wrote, notReady, len(notReady) > 0
	return out, nil
}

// cloneOf is the origin of the clone in dir, "" when there is none.
func cloneOf(ctx context.Context, dir string) string {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return ""
	}
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "remote", "get-url", "origin").Output()
	if err != nil {
		return "(no origin)"
	}
	return strings.TrimSpace(string(out))
}

// sameRepo tells whether a clone's origin is the repository src names,
// whatever form either address takes.
func sameRepo(origin string, src repoSource) bool {
	o, err := parseRepoSource(origin)
	return err == nil && strings.EqualFold(o.Host, src.Host) && strings.EqualFold(o.Path, src.Path)
}

// notReadyNote starts the reason of a project that waits for this machine
// to be ready for its agents; resumeWhenReady lifts that pause by itself.
const notReadyNote = "waits until this machine is ready for its agents"

// resumeWhenReady checks, every two minutes, the projects that wait for
// this machine, and resumes each one as soon as nothing stops its agents:
// the owner fixes the token or the credential and the work starts, with
// no second step.
func (d *daemon) resumeWhenReady(ctx context.Context) {
	t := time.NewTicker(2 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		for _, name := range d.svc.Projects() {
			d.tryResume(ctx, name)
		}
	}
}

// tryResume resumes project when it waits for this machine and this
// machine is now ready for it.
func (d *daemon) tryResume(ctx context.Context, project string) {
	p, err := d.svc.Project(project)
	if err != nil {
		return
	}
	if snap := p.Snapshot(); !snap.Paused || !strings.HasPrefix(snap.PausedWhy, notReadyNote) {
		return
	}
	rctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	problems := cli.Readiness(rctx, p.Cfg())
	cancel()
	if len(problems) > 0 {
		return
	}
	if err := d.svc.Resume(ctx, project, app.System()); err != nil {
		d.log.Warn("resume a project that was waiting for this machine", "project", project, "err", err)
		return
	}
	d.log.Info("this machine is ready: the project that waited for it runs", "project", project)
}

// useRepoToken makes git push and fetch the project's GitHub repository
// with the token kept in the keychain, when its origin is an HTTPS address
// of it: a fresh machine (a new WSL) has no git credential of its own.
func (d *daemon) useRepoToken(ctx context.Context, project, repo string) error {
	d.mu.Lock()
	root := d.roots[project]
	d.mu.Unlock()
	if root == "" {
		return nil
	}
	origin := cloneOf(ctx, root)
	src, err := parseRepoSource(origin)
	if err != nil || !strings.HasPrefix(origin, "https://") || src.Host != "github.com" || !strings.EqualFold(src.Path, repo) {
		return nil
	}
	exe, _ := os.Executable()
	return setCredentialConfig(ctx, root, credentialConfig("github.com", exe))
}
