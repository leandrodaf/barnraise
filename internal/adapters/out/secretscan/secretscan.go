// Package secretscan finds secrets in files and diffs before a push. It has
// built-in rules and can merge findings from gitleaks when the binary is
// installed. Findings never carry the full secret.
package secretscan

import (
	"math"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Finding is one detected secret. Redacted shows at most the first four
// characters of the match.
type Finding struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Rule     string `json:"rule"`
	Redacted string `json:"redacted"`
}

type lineRule struct {
	id string
	re *regexp.Regexp
	// group of the secret within the match (0: whole match).
	group int
	// entropy, when > 0, is the minimum Shannon entropy of the secret.
	entropy float64
	// noPlaceholder skips the placeholder allowlist (PEM headers).
	noPlaceholder bool
}

var lineRules = []lineRule{
	{id: "aws-access-key", re: regexp.MustCompile(`\b((?:AKIA|ASIA|ABIA|ACCA)[0-9A-Z]{16})\b`), group: 1},
	{id: "aws-secret-key", re: regexp.MustCompile(`(?i)aws.{0,20}?(?:secret|sk).{0,20}?[:=]\s*["']?([A-Za-z0-9/+=]{40})\b`), group: 1},
	{id: "github-token", re: regexp.MustCompile(`\b((?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{36,})\b`), group: 1},
	{id: "github-pat", re: regexp.MustCompile(`\b(github_pat_[A-Za-z0-9_]{22,})\b`), group: 1},
	{id: "slack-token", re: regexp.MustCompile(`\b(xox[abposr]-[A-Za-z0-9-]{10,})`), group: 1},
	{id: "slack-webhook", re: regexp.MustCompile(`(https://hooks\.slack\.com/services/T[A-Za-z0-9_]+/B[A-Za-z0-9_]+/[A-Za-z0-9_]+)`), group: 1},
	{id: "stripe-live-key", re: regexp.MustCompile(`\b((?:sk|rk)_live_[A-Za-z0-9]{16,})\b`), group: 1},
	{id: "google-api-key", re: regexp.MustCompile(`\b(AIza[0-9A-Za-z_\-]{35})`), group: 1},
	{id: "private-key", re: regexp.MustCompile(`-----BEGIN (?:RSA |EC |DSA |OPENSSH |PGP |ENCRYPTED )?PRIVATE KEY(?: BLOCK)?-----`), noPlaceholder: true},
	{id: "jwt", re: regexp.MustCompile(`[:=]\s*["'\x60]?(?:Bearer\s+)?(eyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,})`), group: 1},
	{
		id:      "generic-secret",
		re:      regexp.MustCompile(`(?i)\b[\w.-]*(?:secret|token|passwd|password|pwd|api[_-]?key|apikey|access[_-]?key|private[_-]?key|auth[_-]?key|credential)[\w.-]*["']?\s*(?::=|=>|=|:)\s*["'\x60]?([^\s"'\x60,;]{8,})`),
		group:   1,
		entropy: 3.5,
	},
}

// Keystore and certificate bundles that should never be committed.
var keystoreExt = map[string]bool{".jks": true, ".keystore": true, ".p12": true, ".pfx": true}

var placeholders = []string{
	"xxx", "example", "changeme", "change_me", "change-me", "placeholder", "your_", "your-", "yourkey",
	"dummy", "redacted", "sample", "fake", "<", "${", "{{", "%(", "process.env", "os.getenv", "getenv(", "env(",
	"secrets.", "****", "...", "todo", "replace",
}

// IsPlaceholder reports whether a value is an obvious placeholder.
func IsPlaceholder(v string) bool {
	l := strings.ToLower(v)
	for _, p := range placeholders {
		if strings.Contains(l, p) {
			return true
		}
	}
	// One repeated character (aaaaaaaa, 00000000).
	if len(l) > 0 && strings.Count(l, l[:1]) == len(l) {
		return true
	}
	return false
}

// Entropy is the Shannon entropy in bits per character.
func Entropy(s string) float64 {
	if s == "" {
		return 0
	}
	freq := map[rune]float64{}
	n := 0.0
	for _, r := range s {
		freq[r]++
		n++
	}
	var h float64
	for _, c := range freq {
		p := c / n
		h -= p * math.Log2(p)
	}
	return h
}

// Redact keeps the first four characters and masks the rest.
func Redact(s string) string {
	r := []rune(s)
	if len(r) <= 4 {
		return strings.Repeat("*", len(r))
	}
	return string(r[:4]) + strings.Repeat("*", min(len(r)-4, 8)) + "(" + strconv.Itoa(len(r)) + " chars)"
}

// IsKeystore reports whether a file name is a keystore by extension.
func IsKeystore(name string) bool { return keystoreExt[strings.ToLower(path.Ext(name))] }

// IsEnvFile reports whether a file is a dotenv file with real values
// (.env, .env.local, prod.env), excluding templates.
func IsEnvFile(name string) bool {
	b := strings.ToLower(path.Base(name))
	if strings.HasSuffix(b, ".example") || strings.HasSuffix(b, ".sample") || strings.HasSuffix(b, ".template") || strings.HasSuffix(b, ".dist") {
		return false
	}
	return b == ".env" || strings.HasPrefix(b, ".env.") || strings.HasSuffix(b, ".env")
}

var envLine = regexp.MustCompile(`^\s*(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.*)$`)

func scanLine(file string, n int, line string, env bool) []Finding {
	var out []Finding
	hit := false
	for _, r := range lineRules {
		for _, m := range r.re.FindAllStringSubmatch(line, -1) {
			secret := m[r.group]
			if !r.noPlaceholder && IsPlaceholder(secret) {
				continue
			}
			if r.entropy > 0 && Entropy(secret) < r.entropy {
				continue
			}
			if r.id == "generic-secret" && hit {
				continue // a specific rule already explains this line
			}
			out = append(out, Finding{File: file, Line: n, Rule: r.id, Redacted: Redact(secret)})
			hit = true
		}
	}
	if env && !hit {
		if m := envLine.FindStringSubmatch(line); m != nil {
			v := strings.Trim(strings.TrimSpace(stripComment(m[2])), `"'`)
			if v != "" && !IsPlaceholder(v) && !isBoringEnvValue(v) {
				out = append(out, Finding{File: file, Line: n, Rule: "env-file", Redacted: m[1] + "=" + Redact(v)})
			}
		}
	}
	return out
}

func stripComment(v string) string {
	if strings.HasPrefix(strings.TrimSpace(v), `"`) || strings.HasPrefix(strings.TrimSpace(v), `'`) {
		return v
	}
	if before, _, ok := strings.Cut(v, " #"); ok {
		return before
	}
	return v
}

// isBoringEnvValue skips flags and small settings (true, 3000, debug).
func isBoringEnvValue(v string) bool {
	switch strings.ToLower(v) {
	case "true", "false", "yes", "no", "on", "off", "debug", "info", "warn", "error", "development", "production", "staging", "test", "localhost":
		return true
	}
	if _, err := strconv.ParseFloat(v, 64); err == nil {
		return true
	}
	return false
}

// Scan checks file contents. Keys are paths (slash-separated).
func Scan(files map[string][]byte) []Finding {
	var out []Finding
	for name, data := range files {
		out = append(out, scanFile(name, data)...)
	}
	sortFindings(out)
	return out
}

func scanFile(name string, data []byte) []Finding {
	if IsKeystore(name) {
		return []Finding{{File: name, Line: 0, Rule: "keystore-file", Redacted: path.Base(name)}}
	}
	if isBinary(data) {
		return nil
	}
	env := IsEnvFile(name)
	var out []Finding
	for i, l := range strings.Split(string(data), "\n") {
		out = append(out, scanLine(name, i+1, strings.TrimRight(l, "\r"), env)...)
	}
	return out
}

func isBinary(b []byte) bool {
	if len(b) > 8000 {
		b = b[:8000]
	}
	return slices.Contains(b, 0)
}

var hunkHeader = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

// addedLines parses a unified diff into file -> line number -> added text,
// plus binary or new files seen only by name.
func addedLines(diff string) (map[string]map[int]string, []string) {
	files := map[string]map[int]string{}
	var named []string
	file, line := "", 0
	inHunk := false
	for l := range strings.SplitSeq(diff, "\n") {
		switch {
		case strings.HasPrefix(l, "diff --git "):
			inHunk = false
			file = ""
			if parts := strings.SplitN(l, " b/", 2); len(parts) == 2 {
				file = parts[1]
				named = append(named, file)
			}
		case !inHunk && strings.HasPrefix(l, "+++ "):
			p := strings.TrimPrefix(l, "+++ ")
			if p == "/dev/null" {
				file = ""
			} else {
				file = strings.TrimPrefix(p, "b/")
			}
		case !inHunk && strings.HasPrefix(l, "--- "):
		case strings.HasPrefix(l, "@@"):
			if m := hunkHeader.FindStringSubmatch(l); m != nil {
				line, _ = strconv.Atoi(m[1])
				inHunk = true
			}
		case inHunk && strings.HasPrefix(l, "+"):
			if file != "" {
				if files[file] == nil {
					files[file] = map[int]string{}
				}
				files[file][line] = strings.TrimRight(l[1:], "\r")
			}
			line++
		case inHunk && strings.HasPrefix(l, "-"):
		case inHunk && strings.HasPrefix(l, `\`):
		case inHunk:
			line++
		}
	}
	return files, named
}

// ScanDiff checks only the added lines of a unified diff (git diff output),
// with line numbers of the new file.
func ScanDiff(diff string) []Finding {
	files, named := addedLines(diff)
	var out []Finding
	seen := map[string]bool{}
	for _, n := range named {
		if IsKeystore(n) && !seen[n] {
			seen[n] = true
			out = append(out, Finding{File: n, Rule: "keystore-file", Redacted: path.Base(n)})
		}
	}
	for name, lines := range files {
		if IsKeystore(name) {
			continue
		}
		env := IsEnvFile(name)
		for n, text := range lines {
			out = append(out, scanLine(name, n, text, env)...)
		}
	}
	sortFindings(out)
	return out
}

func sortFindings(f []Finding) {
	sort.Slice(f, func(i, j int) bool {
		if f[i].File != f[j].File {
			return f[i].File < f[j].File
		}
		if f[i].Line != f[j].Line {
			return f[i].Line < f[j].Line
		}
		return f[i].Rule < f[j].Rule
	})
}

func dedupe(f []Finding) []Finding {
	seen := map[[2]string]bool{}
	out := f[:0]
	for _, x := range f {
		k := [2]string{x.File, strconv.Itoa(x.Line)}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, x)
	}
	return out
}
