package domain

import (
	"fmt"
	"path"
	"strings"
)

// EgressDef configures what the egress proxy does beyond the host
// allow-list (policy.egress in barnraise.yaml).
type EgressDef struct {
	// Inspect are extra hosts whose HTTPS traffic the proxy terminates and
	// scans for secrets (the providers' hosts and the hosts with path rules
	// are always terminated). Opt-in: empty by default.
	Inspect []string `json:"inspect,omitempty"`
	// RepoScope limits writes to the code host (GitHub, GitLab) to the
	// project's own repository. On by default.
	RepoScope bool `json:"repo_scope"`
}

// ProjectRepoVar is replaced by the project's owner/repo in allow_hosts rules.
const ProjectRepoVar = "{project-repo}"

// HostRule is an allow_hosts entry with a method and a path:
// "GET github.com/owner/repo/**", "POST,PUT api.github.com/repos/{project-repo}/**".
// A plain host ("example.com") is not a rule: it allows the whole host.
type HostRule struct {
	Methods []string `json:"methods,omitempty"` // upper case; empty means any method
	Host    string   `json:"host"`              // lower case; "*.x" also matches subdomains
	Path    string   `json:"path"`              // slash glob: * is one segment, ** any number
}

var httpMethods = map[string]bool{"GET": true, "HEAD": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true, "OPTIONS": true, "*": true}

// WriteMethods are the HTTP methods that change something on the server.
var WriteMethods = []string{"POST", "PUT", "PATCH", "DELETE"}

// ParseAllowEntry reads one allow_hosts entry. A plain host returns
// isRule false and the host; anything with a method or a path is a rule.
func ParseAllowEntry(s string) (rule HostRule, isRule bool, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return HostRule{}, false, fmt.Errorf("empty allow_hosts entry")
	}
	var methods []string
	target := s
	if m, rest, ok := strings.Cut(s, " "); ok {
		target = strings.TrimSpace(rest)
		for x := range strings.SplitSeq(strings.ToUpper(m), ",") {
			x = strings.TrimSpace(x)
			if !httpMethods[x] {
				return HostRule{}, false, fmt.Errorf("allow_hosts %q: %q is not an HTTP method (GET, HEAD, POST, PUT, PATCH, DELETE, OPTIONS or *)", s, x)
			}
			if x == "*" {
				methods = nil
				break
			}
			methods = append(methods, x)
		}
	}
	target = strings.TrimPrefix(strings.TrimPrefix(target, "https://"), "http://")
	host, p, hasPath := strings.Cut(target, "/")
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "" || strings.ContainsAny(host, " \t") {
		return HostRule{}, false, fmt.Errorf("allow_hosts %q: no host", s)
	}
	if !hasPath && methods == nil && !strings.Contains(s, " ") {
		return HostRule{Host: host}, false, nil
	}
	if !hasPath || p == "" {
		p = "**"
	}
	return HostRule{Methods: methods, Host: host, Path: strings.Trim(p, "/")}, true, nil
}

// SplitAllowHosts separates plain hosts from rules. The hosts of the rules
// are not added to the plain hosts: a host only reached through rules
// allows only what the rules say.
func SplitAllowHosts(entries []string) (hosts []string, rules []HostRule, err error) {
	for _, e := range entries {
		r, isRule, perr := ParseAllowEntry(e)
		if perr != nil {
			return nil, nil, perr
		}
		if isRule {
			rules = append(rules, r)
		} else {
			hosts = append(hosts, r.Host)
		}
	}
	return hosts, rules, nil
}

// AllowHostNames returns the host of every entry (plain or rule), without
// duplicates, for allow-lists that only know hosts (the CLI sandbox, the
// proxy's CONNECT check). Unparsable entries are skipped.
func AllowHostNames(entries []string) []string {
	var out []string
	for _, e := range entries {
		r, _, err := ParseAllowEntry(e)
		if err != nil || Contains(out, r.Host) {
			continue
		}
		out = append(out, r.Host)
	}
	return out
}

// MatchesHost reports whether the rule's host covers host (no port).
func (r HostRule) MatchesHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if base, ok := strings.CutPrefix(r.Host, "*."); ok {
		return host == base || strings.HasSuffix(host, "."+base)
	}
	return host == r.Host
}

// MatchesMethod reports whether the rule covers the method.
func (r HostRule) MatchesMethod(method string) bool {
	return len(r.Methods) == 0 || Contains(r.Methods, strings.ToUpper(method))
}

// MatchesPath reports whether the URL path matches the rule's glob.
func (r HostRule) MatchesPath(p string) bool {
	return MatchPathGlob(r.Path, strings.Trim(p, "/"))
}

// String gives the entry back in allow_hosts form.
func (r HostRule) String() string {
	m := "*"
	if len(r.Methods) > 0 {
		m = strings.Join(r.Methods, ",")
	}
	return m + " " + r.Host + "/" + r.Path
}

// MatchPathGlob matches a slash path against a glob where * (and the
// other path.Match patterns) stay within one segment and ** matches any
// number of segments, none included.
func MatchPathGlob(pattern, p string) bool {
	var pat, segs []string
	if pattern != "" {
		pat = strings.Split(pattern, "/")
	}
	if p != "" {
		segs = strings.Split(p, "/")
	}
	return matchSegs(pat, segs)
}

func matchSegs(pat, segs []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			for i := 0; i <= len(segs); i++ {
				if matchSegs(pat[1:], segs[i:]) {
					return true
				}
			}
			return false
		}
		if len(segs) == 0 {
			return false
		}
		if ok, err := path.Match(pat[0], segs[0]); err != nil || !ok {
			return false
		}
		pat, segs = pat[1:], segs[1:]
	}
	return len(segs) == 0
}

// ExpandProjectRepo replaces {project-repo} with owner/repo. An entry that
// needs it while the project has no repository returns ok false.
func ExpandProjectRepo(entry, repo string) (string, bool) {
	if !strings.Contains(entry, ProjectRepoVar) {
		return entry, true
	}
	repo = strings.Trim(repo, "/")
	if repo == "" {
		return "", false
	}
	return strings.ReplaceAll(entry, ProjectRepoVar, repo), true
}

// RepoScopeRules are the default rules that keep an agent's writes to the
// code host inside the project's own repository: pushes and API writes
// only there, fetches (git-upload-pack) from any repository, so reading
// public dependencies keeps working. kind is the code host type (github,
// gitlab); host is the web host (github.com, gitlab.com or the instance).
func RepoScopeRules(kind, host, repo string) []string {
	repo = strings.Trim(repo, "/")
	if repo == "" {
		return nil
	}
	w := strings.Join(WriteMethods, ",")
	switch kind {
	case "github":
		if host == "" {
			host = "github.com"
		}
		api := "api." + host
		if host != "github.com" {
			api = host + "/api/v3" // GitHub Enterprise Server
		}
		return []string{
			w + " " + host + "/" + repo + "/**",
			w + " " + host + "/" + repo + ".git/**",
			"POST " + host + "/**/git-upload-pack",
			w + " " + api + "/repos/" + repo + "/**",
			"POST " + api + "/graphql",
		}
	case "gitlab":
		if host == "" {
			host = "gitlab.com"
		}
		return []string{
			w + " " + host + "/" + repo + "/**",
			w + " " + host + "/" + repo + ".git/**",
			"POST " + host + "/**/git-upload-pack",
			w + " " + host + "/api/v4/projects/" + repo + "/**",
			"POST " + host + "/api/graphql",
		}
	}
	return nil
}

// EgressRules is a compiled allow_hosts list: plain hosts (exact or any
// subdomain) and method/host/path rules.
type EgressRules struct {
	Hosts []string   `json:"hosts"`
	Rules []HostRule `json:"rules,omitempty"`
}

// CompileEgress compiles allow_hosts entries ("*." prefixes of plain hosts
// are dropped: a plain host always covers its subdomains).
func CompileEgress(entries []string) (EgressRules, error) {
	hosts, rules, err := SplitAllowHosts(entries)
	if err != nil {
		return EgressRules{}, err
	}
	var out EgressRules
	for _, h := range hosts {
		h = strings.TrimPrefix(h, "*.")
		if !Contains(out.Hosts, h) {
			out.Hosts = append(out.Hosts, h)
		}
	}
	out.Rules = rules
	return out, nil
}

// HostOnly lowers a host and drops its port and trailing dot.
func HostOnly(host string) string {
	host = strings.ToLower(host)
	if i := strings.LastIndex(host, ":"); i >= 0 && !strings.Contains(host[i:], "]") && strings.Count(host, ":") == 1 {
		host = host[:i]
	} else if strings.HasPrefix(host, "[") {
		if j := strings.Index(host, "]"); j > 0 {
			host = host[1:j]
		}
	}
	return strings.TrimSuffix(host, ".")
}

// PlainAllowed reports whether a plain host entry covers host.
func (e EgressRules) PlainAllowed(host string) bool {
	host = HostOnly(host)
	for _, a := range e.Hosts {
		if host == a || strings.HasSuffix(host, "."+a) {
			return true
		}
	}
	return false
}

// HostAllowed reports whether host may be reached at all: a plain entry or
// a rule names it (what a CONNECT tunnel can be checked against).
func (e EgressRules) HostAllowed(host string) bool {
	return e.PlainAllowed(host) || e.HasRules(host)
}

// HasRules reports whether some rule names host.
func (e EgressRules) HasRules(host string) bool {
	host = HostOnly(host)
	for _, r := range e.Rules {
		if r.MatchesHost(host) {
			return true
		}
	}
	return false
}

// Allow decides a request the proxy can see. The rules naming the host and
// the method decide when there are any (one must match the path);
// otherwise the plain host entries do. rule names what decided.
func (e EgressRules) Allow(method, host, urlPath string) (bool, string) {
	host = HostOnly(host)
	matched := false
	for _, r := range e.Rules {
		if !r.MatchesHost(host) || !r.MatchesMethod(method) {
			continue
		}
		matched = true
		if r.MatchesPath(urlPath) {
			return true, r.String()
		}
	}
	if matched {
		return false, "path-rule"
	}
	if e.PlainAllowed(host) {
		return true, "allow-host"
	}
	return false, "not-allowed"
}
