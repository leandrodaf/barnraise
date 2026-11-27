package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"

	pb "github.com/leandrodaf/barnraise/gen/barnraise/v1"
	"github.com/leandrodaf/barnraise/internal/adapters/out/github"
	natsx "github.com/leandrodaf/barnraise/internal/adapters/out/nats"
	"github.com/leandrodaf/barnraise/internal/domain"
)

// The GitHub token of a project, from the panel: a link to GitHub's form
// with every permission filled in, then the token pasted back, tested
// permission by permission on each machine (from its own network) and kept
// in each machine's keychain. Before, a token missing one permission was
// found hours later, when something stopped with a 403 nobody saw.

const peerGitHubTokenTimeout = 2 * time.Minute

// remoteGitHubToken asks a peer to test and keep a project's GitHub token.
type remoteGitHubToken struct {
	Project      string `json:"project"`
	Repo         string `json:"repo"`
	ProjectBoard int    `json:"project_board,omitempty"`
	Token        string `json:"token"`
	TestOnly     bool   `json:"test_only,omitempty"`
}

// githubRepoOf is the GitHub repository of a project, and its Project
// board number when the tracker uses one.
func (d *daemon) githubRepoOf(project string) (*domain.ProjectConfig, string, int, error) {
	p, err := d.svc.Project(project)
	if err != nil {
		return nil, "", 0, err
	}
	cfg := p.Cfg()
	repo := ""
	if cfg.CodeHost.Type == "github" {
		repo = cfg.CodeHost.Repo
	}
	if repo == "" && cfg.Tracker.Type == "github" {
		repo = cfg.Tracker.Repo
	}
	if repo == "" {
		return nil, "", 0, fmt.Errorf("%s does not use GitHub (code_host and tracker)", project)
	}
	board := 0
	if cfg.Tracker.Type == "github" {
		board, _ = strconv.Atoi(cfg.Tracker.Project)
	}
	return cfg, repo, board, nil
}

// githubTokenSource says where this machine takes the project's token from.
func (d *daemon) githubTokenSource(cfg *domain.ProjectConfig, repo string) string {
	for _, ref := range []string{cfg.CodeHost.Token, cfg.Tracker.Token} {
		if ref != "" {
			return "barnraise.yaml names it (" + ref + "): it wins over the token installed here"
		}
	}
	if d.secrets != nil {
		if v, _ := d.secrets.Get(domain.GitTokenSecret("github.com", repo)); v != "" {
			return "installed by Barnraise in this machine's keychain"
		}
	}
	if os.Getenv("GITHUB_TOKEN") != "" {
		return "the GITHUB_TOKEN variable of the daemon"
	}
	if ghAuthToken() != "" {
		return "the GitHub CLI's login (gh auth)"
	}
	return "none: Barnraise cannot reach GitHub's API for this project"
}

// gitHubTokenSetup answers GetGitHubTokenSetup.
func (d *daemon) gitHubTokenSetup(_ context.Context, project string) (*pb.GitHubTokenSetup, error) {
	cfg, repo, board, err := d.githubRepoOf(project)
	if err != nil {
		return nil, err
	}
	out := &pb.GitHubTokenSetup{Project: project, Repo: repo, Url: github.TemplateURL(repo, board > 0), Current: d.githubTokenSource(cfg, repo)}
	for _, p := range github.Permissions(board > 0) {
		out.Permissions = append(out.Permissions, &pb.GitHubPermission{Name: p.Name, Access: p.Access, Why: p.Why})
	}
	return out, nil
}

// installGitHubToken tests the token here; when every permission passes it
// keeps it here and on every connected machine, each testing it first.
func (d *daemon) installGitHubToken(ctx context.Context, r *pb.GitHubTokenRequest) (*pb.GitHubTokenResponse, error) {
	_, repo, board, err := d.githubRepoOf(r.Project)
	if err != nil {
		return nil, err
	}
	tok := strings.TrimSpace(r.Token)
	if tok == "" {
		return nil, errors.New("paste the token GitHub showed")
	}
	req := remoteGitHubToken{Project: r.Project, Repo: repo, ProjectBoard: board, Token: tok, TestOnly: r.TestOnly}
	here := d.applyGitHubToken(ctx, req)
	out := &pb.GitHubTokenResponse{Machines: []*pb.GitHubTokenMachine{here}}
	if here.Error != "" || r.TestOnly {
		return out, nil
	}
	_, peers := d.targets(nil, true)
	body, _ := json.Marshal(req)
	res := make([]*pb.GitHubTokenMachine, len(peers))
	var wg sync.WaitGroup
	for i, name := range peers {
		wg.Go(func() {
			res[i] = &pb.GitHubTokenMachine{Machine: name}
			if d.repl == nil {
				res[i].Error = "this daemon has no connection to other machines"
				return
			}
			data, err := d.repl.Request(ctx, name, natsx.RPCSubject(name, "github-token"), body, peerGitHubTokenTimeout, 0)
			if err != nil {
				if errors.Is(err, nats.ErrNoResponders) {
					res[i].Error = peerError(err, "the GitHub token install")
				} else {
					res[i].Error = err.Error()
				}
				return
			}
			var m pb.GitHubTokenMachine
			if err := json.Unmarshal(data, &m); err != nil {
				res[i].Error = "unreadable answer: " + err.Error()
				return
			}
			m.Machine = name
			res[i] = &m
		})
	}
	wg.Wait()
	out.Machines = append(out.Machines, res...)
	out.Installed = true
	for _, m := range out.Machines {
		if !m.Installed {
			out.Installed = false
		}
	}
	return out, nil
}

// applyGitHubToken tests the token from this machine and, unless only a
// test is asked or a permission fails, keeps it and reloads the project.
func (d *daemon) applyGitHubToken(ctx context.Context, r remoteGitHubToken) *pb.GitHubTokenMachine {
	m := &pb.GitHubTokenMachine{Machine: d.df.Machine}
	cctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	checks, sum := github.Probe(cctx, github.ProbeOptions{Token: r.Token, Repo: r.Repo, ProjectBoard: r.ProjectBoard})
	m.Kind, m.Expires = sum.Kind, sum.Expires
	var missing []string
	for _, c := range checks {
		m.Checks = append(m.Checks, &pb.GitHubTokenCheck{Name: c.Name, Access: c.Access, Why: c.Why, Ok: c.OK, Testable: c.Testable, Detail: c.Detail})
		if c.Testable && !c.OK {
			missing = append(missing, c.Name+" ("+c.Access+")")
		}
	}
	if len(missing) > 0 {
		m.Error = "the token lacks " + strings.Join(missing, ", ") + ": nothing was installed"
		return m
	}
	if r.TestOnly {
		return m
	}
	if d.secrets == nil {
		m.Error = "this machine has no keychain"
		return m
	}
	if err := d.secrets.Set(domain.GitTokenSecret("github.com", r.Repo), r.Token); err != nil {
		m.Error = "the token was not kept in the keychain: " + err.Error()
		return m
	}
	m.Installed = true
	d.repairGHLogin(r.Token, r.Repo)
	if _, err := d.svc.Project(r.Project); err == nil {
		if err := d.reloadProject(ctx, r.Project); err != nil {
			m.Error = "kept, but the project did not reload: " + err.Error()
		}
	}
	d.log.Info("GitHub token installed", "project", r.Project, "repo", r.Repo, "kind", sum.Kind)
	return m
}

// serveRemoteGitHubToken tests and keeps a token another machine sends.
func (d *daemon) serveRemoteGitHubToken(ctx context.Context) {
	b := d.bus.v.Load()
	if b == nil {
		return
	}
	_ = natsx.Serve(ctx, b.Conn(), natsx.RPCSubject(d.df.Machine, "github-token"), func(ctx context.Context, data []byte) ([]byte, error) {
		var r remoteGitHubToken
		if err := json.Unmarshal(data, &r); err != nil {
			return nil, err
		}
		if r.Token == "" || !strings.Contains(r.Repo, "/") {
			return nil, errors.New("a repository and a token are needed")
		}
		return json.Marshal(d.applyGitHubToken(ctx, r))
	})
}

// ghRepair runs the GitHub CLI's repair one at a time; ghRepairOn is off
// in tests, which must never touch the machine's own gh login.
var (
	ghRepair   sync.Mutex
	ghRepairOn atomic.Bool
)

func init() { ghRepairOn.Store(true) }

// repairGHLogin gives the GitHub CLI of this machine the installed token
// when its own login is missing or no longer works (a deleted token): one
// token, the panel's, reaches GitHub everywhere on the machine. A working
// login is left alone, and so is a GH_TOKEN the environment sets.
func (d *daemon) repairGHLogin(token, repo string) {
	if !ghRepairOn.Load() || token == "" || os.Getenv("GH_TOKEN") != "" || os.Getenv("GITHUB_TOKEN") != "" {
		return
	}
	if _, err := exec.LookPath("gh"); err != nil {
		return
	}
	ghRepair.Lock()
	defer ghRepair.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	env := append(os.Environ(), "GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1")
	status := exec.CommandContext(ctx, "gh", "auth", "status", "-h", "github.com")
	status.Env = env
	if status.Run() == nil {
		return // its own login works
	}
	login := exec.CommandContext(ctx, "gh", "auth", "login", "-h", "github.com", "--git-protocol", "https", "--with-token")
	login.Env = env
	login.Stdin = strings.NewReader(token + "\n")
	if out, err := login.CombinedOutput(); err != nil {
		d.log.Warn("the GitHub CLI login could not be repaired with the installed token", "repo", repo, "err", err, "out", strings.TrimSpace(string(out)))
		return
	}
	d.log.Info("the GitHub CLI had no working login: it now uses the installed token", "repo", repo)
}
