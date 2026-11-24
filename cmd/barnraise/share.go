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
)

// peerShareTimeout bounds a peer's clone and registration of a project.
const peerShareTimeout = 15 * time.Minute

// remoteProject asks a peer to clone a project's repository and register it.
type remoteProject struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	Paused bool   `json:"paused,omitempty"`
	From   string `json:"from,omitempty"`
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
	body, _ := json.Marshal(remoteProject{Name: r.Project, URL: url, Paused: r.Paused, From: d.df.Machine})
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
		d.log.Info("a paired machine shared a project with this machine", "from", rp.From, "project", rp.Name, "url", rp.URL)
		if _, err := d.svc.Project(rp.Name); err == nil {
			return []byte("already here"), nil
		}
		dir := filepath.Join(d.dataDir, "projects", rp.Name)
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
			for _, u := range cloneURLs(rp.URL) {
				cmd := exec.CommandContext(cctx, "git", "clone", "--quiet", "--", u, dir)
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
		if err := d.register(cctx, dir, rp.Paused); err != nil {
			return nil, fmt.Errorf("%s was cloned to %s but not registered: %w", rp.Name, dir, err)
		}
		return []byte(dir), nil
	})
}
