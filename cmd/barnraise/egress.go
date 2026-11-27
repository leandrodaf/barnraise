package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/leandrodaf/barnraise/internal/adapters/out/egress"
	"github.com/leandrodaf/barnraise/internal/app"
	"github.com/leandrodaf/barnraise/internal/config"
	"github.com/leandrodaf/barnraise/internal/domain"
)

// startEgress runs the allow-list proxy on addr for the machine's projects:
// host allow-list, method/path rules (the project's own and, by default,
// writes to the code host only toward the project's repository), the key
// broker for key-based providers, inspection and signed receipts.
func (d *daemon) startEgress(ctx context.Context, svc *app.Service, roots []string, addr string) error {
	log := d.log
	allow := append([]string(nil), egress.DefaultAllow...)
	var apiHosts, inspect []string
	for _, root := range roots {
		cfg, err := config.Load(root)
		if err != nil {
			continue
		}
		allow = append(allow, egressEntries(cfg, remoteURL(cfg), log)...)
		apiHosts = append(apiHosts, config.APIHosts(cfg)...)
		inspect = append(inspect, cfg.Egress.Inspect...)
	}
	allow = append(allow, apiHosts...)
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("egress proxy: %w", err)
	}
	px := egress.New(allow, log)
	// The MCP servers the owner lets agents use change while the daemon
	// runs: their hosts are asked for at each request.
	px.Extra = svc.AgentMCPHosts
	dir := filepath.Join(d.dataDir, "egress")
	switch ca, err := egress.LoadOrCreateCA(dir, nil); {
	case err != nil:
		log.Warn("egress: no local CA, TLS is not terminated: provider keys reach sessions as before and path rules apply to plain HTTP only", "err", err)
	default:
		bundle, err := ca.TrustBundle()
		if err != nil {
			log.Warn("egress: TLS is not terminated: provider keys reach sessions as before and path rules apply to plain HTTP only", "err", err)
			break
		}
		br := egress.NewBroker(apiHosts)
		br.SetCABundle(bundle)
		px.CA, px.Broker, px.Inspect = ca, br, inspect
		svc.Runtime().KeyBroker = br
	}
	if rs, err := egress.OpenReceipts(dir, nil); err != nil {
		log.Warn("egress: receipts are not recorded", "err", err)
	} else {
		px.Receipts = rs
		go func() { <-ctx.Done(); _ = rs.Close() }()
	}
	px.OnBlock = func(host string) {
		for _, name := range svc.Projects() {
			_ = svc.RecordViolation(context.Background(), name, "agent", "", "network", "egress", "outbound connection to "+host+" blocked", host)
		}
	}
	px.OnDeny = func(r egress.Receipt) {
		reason := fmt.Sprintf("outbound %s %s%s blocked (%s)", r.Method, r.Host, r.Path, r.Rule)
		if r.Detail != "" {
			reason += ": " + r.Detail
		}
		for _, name := range svc.Projects() {
			_ = svc.RecordViolation(context.Background(), name, "agent", "", "network", "egress-"+ruleKind(r.Rule), reason, r.Host+r.Path)
		}
	}
	go func() { _ = px.Serve(ctx, l) }()
	svc.Runtime().EgressProxy = l.Addr().String()
	log.Info("egress allow-list proxy", "listen", l.Addr().String(), "hosts", len(px.Rules().Hosts), "rules", len(px.Rules().Rules),
		"terminates_tls", px.CA != nil, "receipts", filepath.Join(dir, egress.ReceiptsLog))
	return nil
}

// ruleKind names a refusal for the violation event: path-rule, secret,
// sealed-key-foreign-host.
func ruleKind(rule string) string {
	k, _, _ := strings.Cut(rule, ":")
	return k
}

// egressEntries are a project's allow_hosts entries with {project-repo}
// expanded, plus the default rules that keep writes to the code host
// inside the project's repository (policy.egress.repo_scope).
func egressEntries(cfg *domain.ProjectConfig, remote string, log *slog.Logger) []string {
	kind, host, repo := codeHostOf(cfg, remote)
	var out []string
	for _, e := range cfg.Policy.AllowHosts {
		x, ok := domain.ExpandProjectRepo(e, repo)
		if !ok {
			log.Warn("egress: allow_hosts entry needs the project's repository, which is unknown", "project", cfg.Name, "entry", e)
			continue
		}
		out = append(out, x)
	}
	if !cfg.Egress.RepoScope {
		return out
	}
	plain, err := domain.CompileEgress(append(append([]string(nil), egress.DefaultAllow...), out...))
	if err != nil {
		return out
	}
	// Only for code hosts that are reachable anyway: the rules narrow what
	// is allowed, they never open a host.
	for _, r := range domain.RepoScopeRules(kind, host, repo) {
		if hr, _, err := domain.ParseAllowEntry(r); err == nil && plain.PlainAllowed(hr.Host) {
			out = append(out, r)
		}
	}
	return out
}

// codeHostOf finds the code host type, web host and owner/repo of a
// project: from code_host when set, else from the git remote's URL.
func codeHostOf(cfg *domain.ProjectConfig, remote string) (kind, host, repo string) {
	kind, repo = cfg.CodeHost.Type, cfg.CodeHost.Repo
	if u, err := url.Parse(cfg.CodeHost.URL); err == nil && u.Hostname() != "" {
		host = u.Hostname()
	}
	rHost, rRepo := parseRemote(remote)
	if repo == "" {
		repo = rRepo
	}
	if host == "" && (kind == "github" || kind == "gitlab") && rHost != "" {
		host = rHost
	}
	if kind != "github" && kind != "gitlab" {
		switch rHost {
		case "github.com":
			kind, host = "github", rHost
		case "gitlab.com":
			kind, host = "gitlab", rHost
		}
	}
	return kind, host, repo
}

// parseRemote reads host and owner/repo from a git remote URL
// (https://host/o/r.git, ssh://git@host/o/r, git@host:o/r.git).
func parseRemote(remote string) (host, repo string) {
	remote = strings.TrimSpace(remote)
	if remote == "" {
		return "", ""
	}
	if !strings.Contains(remote, "://") {
		if at, rest, ok := strings.Cut(remote, ":"); ok {
			remote = "ssh://" + at + "/" + rest
		}
	}
	u, err := url.Parse(remote)
	if err != nil || u.Hostname() == "" {
		return "", ""
	}
	repo = strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git")
	if !strings.Contains(repo, "/") {
		return u.Hostname(), ""
	}
	return strings.ToLower(u.Hostname()), repo
}

func remoteURL(cfg *domain.ProjectConfig) string {
	remote := cfg.CodeHost.Remote
	if remote == "" {
		remote = "origin"
	}
	out, err := exec.Command("git", "-C", cfg.Root, "remote", "get-url", remote).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
