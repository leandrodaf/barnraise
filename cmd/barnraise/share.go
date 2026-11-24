package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"

	pb "github.com/leandrodaf/barnraise/gen/barnraise/v1"
	natsx "github.com/leandrodaf/barnraise/internal/adapters/out/nats"
	"github.com/leandrodaf/barnraise/internal/domain"
)

// peerShareTimeout bounds a peer's clone and registration of a project.
const peerShareTimeout = 15 * time.Minute

// remoteProject asks a peer to clone a project's repository and register it.
type remoteProject struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	Paused bool   `json:"paused,omitempty"`
	From   string `json:"from,omitempty"`
	// Token is kept in the peer's keychain for this repository only.
	Token string `json:"token,omitempty"`
}

var projectNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

// shareProject asks the named machines to work on a project too.
func (d *daemon) shareProject(ctx context.Context, r *pb.ShareProjectRequest) (*pb.ShareProjectResponse, error) {
	d.mu.Lock()
	root := d.roots[r.Project]
	d.mu.Unlock()
	if root == "" {
		return nil, fmt.Errorf("%s is not a project of this machine", r.Project)
	}
	out, err := exec.CommandContext(ctx, "git", "-C", root, "remote", "get-url", "origin").Output()
	if err != nil {
		return nil, fmt.Errorf("%s has no origin remote to clone from: %w", r.Project, err)
	}
	url := strings.TrimSpace(string(out))
	_, peers := d.targets(r.Machines, r.All)
	body, _ := json.Marshal(remoteProject{Name: r.Project, URL: url, Paused: r.Paused, From: d.df.Machine, Token: strings.TrimSpace(r.Token)})
	res := make([]*pb.PeerResult, len(peers))
	var wg sync.WaitGroup
	for i, name := range peers {
		wg.Go(func() {
			res[i] = &pb.PeerResult{Name: name}
			if d.repl == nil {
				res[i].Error = "this daemon has no connection to other machines"
				return
			}
			_, err := d.repl.Request(ctx, name, natsx.RPCSubject(name, "project"), body, peerShareTimeout, 0)
			switch {
			case err == nil:
				res[i].Ok = true
			case errors.Is(err, nats.ErrNoResponders):
				res[i].Error = peerError(err, "projects shared from another machine")
			default:
				res[i].Error = err.Error()
			}
		})
	}
	wg.Wait()
	return &pb.ShareProjectResponse{Machines: res}, nil
}

// scpURL is git's short SSH form: git@host:owner/repo.git.
var scpURL = regexp.MustCompile(`^[A-Za-z0-9._-]+@([A-Za-z0-9.-]+):([^/].*)$`)

// cloneURLs are the addresses to try for a repository: as given, then the
// HTTPS form of an SSH address.
func cloneURLs(u string) []string {
	out := []string{u}
	switch {
	case scpURL.MatchString(u):
		m := scpURL.FindStringSubmatch(u)
		out = append(out, "https://"+m[1]+"/"+m[2])
	case strings.HasPrefix(u, "ssh://"):
		rest := strings.TrimPrefix(u, "ssh://")
		if i := strings.Index(rest, "@"); i >= 0 {
			rest = rest[i+1:]
		}
		if host, path, ok := strings.Cut(rest, "/"); ok {
			host, _, _ = strings.Cut(host, ":")
			out = append(out, "https://"+host+"/"+path)
		}
	}
	return out
}

// httpsHostPath splits an HTTPS repository address into host and path.
func httpsHostPath(u string) (host, path string, ok bool) {
	rest, found := strings.CutPrefix(u, "https://")
	if !found {
		return "", "", false
	}
	if i := strings.Index(rest, "@"); i >= 0 && i < strings.Index(rest+"/", "/") {
		rest = rest[i+1:]
	}
	host, path, ok = strings.Cut(rest, "/")
	return host, path, ok && host != "" && path != ""
}

// credentialConfig are git's -c options that make the host's credentials
// come from barnraise's helper alone: the empty helper first drops the
// ones configured for the user, and the path lets the helper pick the
// repository's token.
func credentialConfig(host, exe string) []string {
	key := "credential.https://" + host
	return []string{"-c", key + ".helper=", "-c", key + ".helper=!" + exe + " git-credential", "-c", key + ".useHttpPath=true"}
}

// setCredentialConfig writes the same options into the repository's own
// configuration, so every fetch and push of the project and its worktrees
// uses them.
func setCredentialConfig(ctx context.Context, dir string, args []string) error {
	for i := 0; i+1 < len(args); i += 2 {
		k, v, _ := strings.Cut(args[i+1], "=")
		mode := "--add"
		if strings.HasSuffix(k, ".useHttpPath") {
			mode = "--replace-all"
		}
		if v == "" {
			// The reset comes first: drop what was there for the key.
			_ = exec.CommandContext(ctx, "git", "-C", dir, "config", "--local", "--unset-all", k).Run()
		}
		if out, err := exec.CommandContext(ctx, "git", "-C", dir, "config", "--local", mode, k, v).CombinedOutput(); err != nil {
			return fmt.Errorf("git config %s in %s: %v: %s", k, dir, err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// serveRemoteProject clones and registers a project a paired machine shares.
// The repository goes to <data dir>/projects/<name>; a clone already there
// is kept. Git never asks a question: missing credentials are an error.
func (d *daemon) serveRemoteProject(ctx context.Context) {
	b := d.bus.v.Load()
	if b == nil {
		return
	}
	_ = natsx.Serve(ctx, b.Conn(), natsx.RPCSubject(d.df.Machine, "project"), func(ctx context.Context, data []byte) ([]byte, error) {
		var rp remoteProject
		if err := json.Unmarshal(data, &rp); err != nil {
			return nil, err
		}
		if !projectNameRe.MatchString(rp.Name) || rp.URL == "" || strings.HasPrefix(rp.URL, "-") {
			return nil, errors.New("a project name and a repository address are needed")
		}
		d.log.Info("a paired machine shared a project with this machine", "from", rp.From, "project", rp.Name, "url", rp.URL, "token", rp.Token != "")
		dir := filepath.Join(d.dataDir, "projects", rp.Name)
		// With a token, git reaches the repository over HTTPS with it, read
		// from this machine's keychain by the helper, for this repository only.
		var gitArgs []string
		if rp.Token != "" {
			u := cloneURLs(rp.URL)
			https := u[len(u)-1]
			host, path, ok := httpsHostPath(https)
			if !ok {
				return nil, fmt.Errorf("a token needs an HTTPS address for %s", rp.URL)
			}
			if err := d.secrets.Set(domain.GitTokenSecret(host, path), rp.Token); err != nil {
				return nil, fmt.Errorf("the token was not kept in the keychain of %s: %w", d.df.Machine, err)
			}
			exe, _ := os.Executable()
			gitArgs = credentialConfig(host, exe)
			rp.URL = https
		}
		if p, err := d.svc.Project(rp.Name); err == nil {
			// Already here: bring its checkout to the latest main and read
			// its configuration again.
			d.mu.Lock()
			root := d.roots[p.Name]
			d.mu.Unlock()
			if len(gitArgs) > 0 {
				if err := setCredentialConfig(ctx, root, gitArgs); err != nil {
					return nil, err
				}
			}
			pull := exec.CommandContext(ctx, "git", "-C", root, "pull", "--ff-only", "--quiet")
			pull.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_SSH_COMMAND=ssh -o BatchMode=yes")
			if out, err := pull.CombinedOutput(); err != nil {
				return nil, fmt.Errorf("git pull in %s on %s: %v: %s", root, d.df.Machine, err, strings.TrimSpace(string(out)))
			}
			if err := d.reloadRoot(ctx, root); err != nil {
				return nil, err
			}
			return []byte("updated"), nil
		}
		cctx, cancel := context.WithTimeout(ctx, peerShareTimeout-time.Minute)
		defer cancel()
		if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
			if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
				return nil, err
			}
			// The address as the sharing machine has it, then its HTTPS form:
			// a machine may reach the host over HTTPS only (a credential
			// helper, no SSH key).
			var errs []string
			cloned := false
			urls := cloneURLs(rp.URL)
			if len(gitArgs) > 0 {
				urls = []string{rp.URL}
			}
			for _, u := range urls {
				cmd := exec.CommandContext(cctx, "git", append(append([]string{}, gitArgs...), "clone", "--quiet", "--", u, dir)...)
				cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_SSH_COMMAND=ssh -o BatchMode=yes")
				out, err := cmd.CombinedOutput()
				if err == nil {
					cloned = true
					break
				}
				_ = os.RemoveAll(dir)
				errs = append(errs, fmt.Sprintf("%s: %v: %s", u, err, strings.TrimSpace(string(out))))
			}
			if !cloned {
				return nil, fmt.Errorf("git clone on %s: %s", d.df.Machine, strings.Join(errs, " · "))
			}
		}
		if len(gitArgs) > 0 {
			if err := setCredentialConfig(cctx, dir, gitArgs); err != nil {
				return nil, err
			}
		}
		if err := d.register(cctx, dir, rp.Paused); err != nil {
			return nil, fmt.Errorf("%s was cloned to %s but not registered: %w", rp.Name, dir, err)
		}
		return []byte(dir), nil
	})
}
