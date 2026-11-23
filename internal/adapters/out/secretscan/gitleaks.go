package secretscan

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Scanner runs the built-in rules and, when configured, gitleaks.
type Scanner struct {
	gitleaks string
	timeout  time.Duration
}

// New returns a scanner with built-in rules only.
func New() *Scanner { return &Scanner{timeout: 2 * time.Minute} }

// UseGitleaks enables gitleaks at path; an empty path looks it up in PATH.
// If the binary is not found, the scanner keeps using the built-in rules
// only. It returns the scanner for chaining.
func (s *Scanner) UseGitleaks(path string) *Scanner {
	if path == "" {
		path = "gitleaks"
	}
	if p, err := exec.LookPath(path); err == nil {
		s.gitleaks = p
	}
	return s
}

// Gitleaks returns the resolved binary, or "" when disabled.
func (s *Scanner) Gitleaks() string { return s.gitleaks }

// Scan runs the built-in rules and gitleaks (if enabled) on files.
// A gitleaks failure is returned with the built-in findings intact.
func (s *Scanner) Scan(ctx context.Context, files map[string][]byte) ([]Finding, error) {
	out := Scan(files)
	if s.gitleaks == "" || len(files) == 0 {
		return out, nil
	}
	gl, err := s.runGitleaks(ctx, files)
	if err != nil {
		return out, err
	}
	return merge(out, gl), nil
}

// ScanDiff runs the built-in rules on the added lines and gitleaks on a
// reconstruction of those lines at their new-file line numbers.
func (s *Scanner) ScanDiff(ctx context.Context, diff string) ([]Finding, error) {
	out := ScanDiff(diff)
	if s.gitleaks == "" {
		return out, nil
	}
	added, _ := addedLines(diff)
	files := map[string][]byte{}
	for name, lines := range added {
		if IsKeystore(name) {
			continue
		}
		maxLine := 0
		for n := range lines {
			maxLine = max(maxLine, n)
		}
		buf := make([]string, maxLine)
		for n, t := range lines {
			buf[n-1] = t
		}
		files[name] = []byte(strings.Join(buf, "\n"))
	}
	if len(files) == 0 {
		return out, nil
	}
	gl, err := s.runGitleaks(ctx, files)
	if err != nil {
		return out, err
	}
	return merge(out, gl), nil
}

// ScanDir runs gitleaks on a directory as is (no built-in rules).
func (s *Scanner) ScanDir(ctx context.Context, dir string) ([]Finding, error) {
	if s.gitleaks == "" {
		return nil, fmt.Errorf("gitleaks is not enabled")
	}
	return s.gitleaksDir(ctx, dir)
}

func merge(a, b []Finding) []Finding {
	// Built-in findings come first so they win over gitleaks on the same line.
	all := dedupe(append(append([]Finding(nil), a...), b...))
	sortFindings(all)
	return all
}

type gitleaksFinding struct {
	RuleID    string `json:"RuleID"`
	File      string `json:"File"`
	StartLine int    `json:"StartLine"`
	Secret    string `json:"Secret"`
	Match     string `json:"Match"`
}

func (s *Scanner) runGitleaks(ctx context.Context, files map[string][]byte) ([]Finding, error) {
	dir, err := os.MkdirTemp("", "barnraise-scan-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	src := filepath.Join(dir, "src")
	for name, data := range files {
		clean := filepath.Clean("/" + name) // no escaping the temp dir
		p := filepath.Join(src, clean)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(p, data, 0o600); err != nil {
			return nil, err
		}
	}
	return s.gitleaksDir(ctx, src)
}

func (s *Scanner) gitleaksDir(ctx context.Context, src string) ([]Finding, error) {
	rep, err := os.CreateTemp("", "barnraise-gitleaks-*.json")
	if err != nil {
		return nil, err
	}
	rep.Close()
	defer os.Remove(rep.Name())
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.gitleaks, "detect", "--no-git", "--source", src,
		"--report-format", "json", "--report-path", rep.Name(), "--exit-code", "0", "--no-banner", "--redact")
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("gitleaks: %v: %s", err, lastLine(out))
	}
	data, err := os.ReadFile(rep.Name())
	if err != nil {
		return nil, err
	}
	var raw []gitleaksFinding
	if len(strings.TrimSpace(string(data))) > 0 {
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil, fmt.Errorf("gitleaks report: %w", err)
		}
	}
	out := make([]Finding, 0, len(raw))
	for _, f := range raw {
		rel, err := filepath.Rel(src, f.File)
		if err != nil || strings.HasPrefix(rel, "..") {
			rel = f.File
		}
		// --redact already masks the secret; keep only a short prefix of
		// whatever came back to be safe.
		red := Redact(f.Secret)
		if f.Secret == "" || strings.Contains(f.Secret, "REDACTED") {
			red = "REDACTED"
		}
		out = append(out, Finding{File: filepath.ToSlash(rel), Line: f.StartLine, Rule: "gitleaks:" + f.RuleID, Redacted: red})
	}
	return out, nil
}

func lastLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
