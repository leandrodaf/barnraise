package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadEgressYAML(t *testing.T, policy string) (string, error) {
	t.Helper()
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, FileName), []byte("project: demo\npolicy:\n"+policy), 0o644)
	cfg, err := Load(root)
	if err != nil {
		return "", err
	}
	return strings.Join(cfg.Egress.Inspect, ",") + "|" + map[bool]string{true: "scoped", false: "open"}[cfg.Egress.RepoScope] + "|" +
		strings.Join(cfg.Policy.AllowHosts[len(cfg.Policy.AllowHosts)-1:], ","), nil
}

func TestPolicyEgress(t *testing.T) {
	got, err := loadEgressYAML(t, "  allow_hosts: [example.com]\n")
	if err != nil || got != "|scoped|example.com" {
		t.Fatal(got, err)
	}
	got, err = loadEgressYAML(t, "  allow_hosts: [\"POST api.github.com/repos/{project-repo}/**\"]\n  egress:\n    inspect: [paste.example, \"*.hooks.example\"]\n    repo_scope: false\n")
	if err != nil || got != "paste.example,*.hooks.example|open|POST api.github.com/repos/{project-repo}/**" {
		t.Fatal(got, err)
	}
	if _, err := loadEgressYAML(t, "  allow_hosts: [\"BREW x.example/y\"]\n"); err == nil || !strings.Contains(err.Error(), "policy.allow_hosts") {
		t.Fatal(err)
	}
	if _, err := loadEgressYAML(t, "  egress:\n    inspect: [\"GET x.example/y\"]\n"); err == nil || !strings.Contains(err.Error(), "policy.egress.inspect") {
		t.Fatal(err)
	}
}
