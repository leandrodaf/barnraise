package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/leandrodaf/barnraise/internal/adapters/in/cli"
	"github.com/leandrodaf/barnraise/internal/adapters/out/egress"
	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/paths"
)

func TestDaemonStartsTheEgressProxy(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a daemon")
	}
	startHarnessWith(t, demoYAML, cli.DaemonOptions{Fake: true, Egress: "127.0.0.1:0"})
	dir := filepath.Join(paths.DataDir(), "egress")
	for _, f := range []string{egress.ReceiptsKeyFile, egress.ReceiptsPubFile, egress.CACertFile, egress.CAKeyFile} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestParseRemote(t *testing.T) {
	for in, want := range map[string][2]string{
		"https://github.com/o/r.git":      {"github.com", "o/r"},
		"git@github.com:o/r.git":          {"github.com", "o/r"},
		"ssh://git@GitLab.com/g/sub/r":    {"gitlab.com", "g/sub/r"},
		"https://example.com/only":        {"example.com", ""},
		"":                                {"", ""},
		"/local/path":                     {"", ""},
		"https://git.corp:8443/team/app/": {"git.corp", "team/app"},
	} {
		h, r := parseRemote(in)
		if h != want[0] || r != want[1] {
			t.Errorf("%q: %q %q", in, h, r)
		}
	}
}

func TestEgressEntries(t *testing.T) {
	cfg := &domain.ProjectConfig{Name: "demo", Policy: domain.DefaultPolicy(), Egress: domain.EgressDef{RepoScope: true},
		CodeHost: domain.CodeHostDef{Type: "git"}}
	cfg.Policy.AllowHosts = append(cfg.Policy.AllowHosts, "GET docs.example/{project-repo}/**")
	got := egressEntries(cfg, "git@github.com:o/r.git", slog.Default())
	for _, want := range []string{"GET docs.example/o/r/**", "POST,PUT,PATCH,DELETE github.com/o/r.git/**", "POST,PUT,PATCH,DELETE api.github.com/repos/o/r/**"} {
		if !slices.Contains(got, want) {
			t.Errorf("missing %q in %v", want, got)
		}
	}
	rules, err := domain.CompileEgress(got)
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := rules.Allow("POST", "github.com", "/evil/x.git/git-receive-pack"); ok {
		t.Fatal("push to another repository allowed")
	}
	if ok, _ := rules.Allow("POST", "github.com", "/o/r.git/git-receive-pack"); !ok {
		t.Fatal("push to the project's repository refused")
	}
	// Opted out: no default rules; unknown repository: {project-repo} entries dropped.
	cfg.Egress.RepoScope = false
	got = egressEntries(cfg, "", slog.Default())
	if slices.ContainsFunc(got, func(s string) bool { return s == "GET docs.example/o/r/**" }) || len(got) != len(cfg.Policy.AllowHosts)-1 {
		t.Fatal(got)
	}
	// GitLab on its own instance, not allowed by default: no rules open it.
	cfg.Egress.RepoScope = true
	cfg.CodeHost = domain.CodeHostDef{Type: "gitlab", Repo: "g/r", URL: "https://gl.corp"}
	for _, e := range egressEntries(cfg, "", slog.Default()) {
		if r, isRule, _ := domain.ParseAllowEntry(e); isRule && r.Host == "gl.corp" {
			t.Fatal("rule for a host that is not allowed", e)
		}
	}
	if k, h, r := codeHostOf(&domain.ProjectConfig{CodeHost: domain.CodeHostDef{Type: "gitlab"}}, "https://gitlab.com/g/r"); k != "gitlab" || h != "gitlab.com" || r != "g/r" {
		t.Fatal(k, h, r)
	}
	if k, _, _ := codeHostOf(&domain.ProjectConfig{CodeHost: domain.CodeHostDef{Type: "git"}}, "https://gitlab.com/g/r"); k != "gitlab" {
		t.Fatal(k)
	}
	if ruleKind("secret:github-token") != "secret" || ruleKind("path-rule") != "path-rule" {
		t.Fatal("rule kind")
	}
}
