package depscan

import (
	"bufio"
	"bytes"
	"encoding/json"
	"encoding/xml"
	"path"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Package is one resolved dependency, named as OSV names it.
type Package struct {
	Ecosystem string `json:"ecosystem"`
	Name      string `json:"name"`
	Version   string `json:"version"`
}

func (p Package) key() string     { return p.Ecosystem + "\x00" + p.Name + "\x00" + p.Version }
func (p Package) nameKey() string { return p.Ecosystem + "\x00" + p.Name }

// OSV ecosystem names.
const (
	ecoGo    = "Go"
	ecoNPM   = "npm"
	ecoPyPI  = "PyPI"
	ecoCargo = "crates.io"
	ecoGems  = "RubyGems"
	ecoMaven = "Maven"
)

// parser reads the packages pinned by one kind of file.
type parser func(data []byte) []Package

// parserFor returns the native parser of a lockfile, by its name.
func parserFor(file string) parser {
	base := path.Base(file)
	switch {
	case base == "go.mod":
		return parseGoMod
	case base == "package-lock.json" || base == "npm-shrinkwrap.json":
		return parsePackageLock
	case base == "pnpm-lock.yaml":
		return parsePnpmLock
	case base == "yarn.lock":
		return parseYarnLock
	case strings.HasPrefix(base, "requirements") && strings.HasSuffix(base, ".txt"):
		return parseRequirements
	case base == "Pipfile.lock":
		return parsePipfileLock
	case base == "poetry.lock" || base == "uv.lock":
		return tomlPackages(ecoPyPI)
	case base == "Cargo.lock":
		return tomlPackages(ecoCargo)
	case base == "Gemfile.lock":
		return parseGemfileLock
	case base == "gradle.lockfile":
		return parseGradleLock
	case base == "pom.xml":
		return parsePom
	}
	return nil
}

// lockfilesOf names the files that pin what a manifest declares: the
// manifest itself when it pins versions, otherwise its lockfiles. They are
// looked up in the manifest's folder and then in the folders above it (a
// workspace keeps one lockfile at its root).
var lockfilesOf = map[string][]string{
	"go.sum":           {"go.mod"},
	"package.json":     {"package-lock.json", "npm-shrinkwrap.json", "pnpm-lock.yaml", "yarn.lock"},
	"pyproject.toml":   {"poetry.lock", "uv.lock"},
	"Cargo.toml":       {"Cargo.lock"},
	"Gemfile":          {"Gemfile.lock"},
	"build.gradle":     {"gradle.lockfile"},
	"build.gradle.kts": {"gradle.lockfile"},
}

func sortPackages(ps []Package) []Package {
	seen := map[string]bool{}
	out := ps[:0]
	for _, p := range ps {
		if p.Name == "" || p.Version == "" || seen[p.key()] {
			continue
		}
		seen[p.key()] = true
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key() < out[j].key() })
	return out
}

// go.mod: every required module, and the standard library at the
// toolchain (or go) version. OSV writes Go versions without the "v".
func parseGoMod(data []byte) []Package {
	var out []Package
	inRequire := false
	goVersion, toolchain := "", ""
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		switch {
		case inRequire && f[0] == ")":
			inRequire = false
		case inRequire && len(f) >= 2:
			out = append(out, Package{ecoGo, f[0], strings.TrimPrefix(f[1], "v")})
		case f[0] == "require" && len(f) >= 2 && f[1] == "(":
			inRequire = true
		case f[0] == "require" && len(f) >= 3:
			out = append(out, Package{ecoGo, f[1], strings.TrimPrefix(f[2], "v")})
		case f[0] == "go" && len(f) == 2:
			goVersion = f[1]
		case f[0] == "toolchain" && len(f) == 2:
			toolchain = strings.TrimPrefix(f[1], "go")
		}
	}
	if v := def(toolchain, goVersion); v != "" {
		out = append(out, Package{ecoGo, "stdlib", v})
	}
	return sortPackages(out)
}

// package-lock.json and npm-shrinkwrap.json, lockfile versions 1 to 3.
func parsePackageLock(data []byte) []Package {
	var lock struct {
		Packages map[string]struct {
			Version string `json:"version"`
			Name    string `json:"name"`
			Link    bool   `json:"link"`
		} `json:"packages"`
		Dependencies map[string]json.RawMessage `json:"dependencies"`
	}
	if json.Unmarshal(data, &lock) != nil {
		return nil
	}
	var out []Package
	if len(lock.Packages) > 0 {
		for key, p := range lock.Packages {
			i := strings.LastIndex(key, "node_modules/")
			if key == "" || p.Link || i < 0 {
				continue // the project itself, a workspace link
			}
			name := key[i+len("node_modules/"):]
			if p.Name != "" {
				name = p.Name
			}
			out = append(out, Package{ecoNPM, name, p.Version})
		}
		return sortPackages(out)
	}
	var walk func(deps map[string]json.RawMessage)
	walk = func(deps map[string]json.RawMessage) {
		for name, raw := range deps {
			var d struct {
				Version      string                     `json:"version"`
				Dependencies map[string]json.RawMessage `json:"dependencies"`
			}
			if json.Unmarshal(raw, &d) != nil {
				continue
			}
			if !strings.Contains(d.Version, ":") { // file:, git+https:, npm: aliases
				out = append(out, Package{ecoNPM, name, d.Version})
			}
			walk(d.Dependencies)
		}
	}
	walk(lock.Dependencies)
	return sortPackages(out)
}

// pnpm-lock.yaml: the keys of `packages`, in the formats of lockfile
// versions 5 (/name/1.0.0_peer), 6 (/name@1.0.0(peer)) and 9 (name@1.0.0).
func parsePnpmLock(data []byte) []Package {
	var lock struct {
		Packages map[string]struct {
			Name    string `yaml:"name"`
			Version string `yaml:"version"`
		} `yaml:"packages"`
	}
	if yaml.Unmarshal(data, &lock) != nil {
		return nil
	}
	var out []Package
	for key, p := range lock.Packages {
		if p.Name != "" && p.Version != "" {
			out = append(out, Package{ecoNPM, p.Name, p.Version})
			continue
		}
		k := strings.TrimPrefix(key, "/")
		if i := strings.Index(k, "("); i >= 0 {
			k = k[:i]
		}
		var name, version string
		if at := strings.LastIndex(k, "@"); at > 0 {
			name, version = k[:at], k[at+1:]
		} else if sl := strings.LastIndex(k, "/"); sl > 0 {
			name, version = k[:sl], k[sl+1:]
			if i := strings.Index(version, "_"); i >= 0 {
				version = version[:i]
			}
		}
		if strings.ContainsAny(version, ":/") {
			continue // a tarball, git or file dependency
		}
		out = append(out, Package{ecoNPM, name, version})
	}
	return sortPackages(out)
}

// yarn.lock, classic (version "1.0.0") and berry (version: 1.0.0).
func parseYarnLock(data []byte) []Package {
	var out []Package
	name := ""
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if line[0] != ' ' {
			name = ""
			head := strings.TrimSuffix(strings.TrimSpace(line), ":")
			spec := strings.Trim(strings.TrimSpace(strings.Split(head, ",")[0]), `"`)
			if spec == "__metadata" {
				continue
			}
			if at := strings.LastIndex(spec, "@"); at > 0 {
				name = spec[:at]
				if i := strings.Index(name, "@npm:"); i > 0 {
					name = name[:i] // berry: name@npm:^1.0.0
				}
			}
			continue
		}
		t := strings.TrimSpace(line)
		if name == "" || !strings.HasPrefix(t, "version") {
			continue
		}
		v := strings.Trim(strings.TrimSpace(strings.TrimLeft(strings.TrimPrefix(t, "version"), ": ")), `"`)
		if v != "" && !strings.HasPrefix(v, "0.0.0-use.local") {
			out = append(out, Package{ecoNPM, name, v})
		}
		name = ""
	}
	return sortPackages(out)
}

var pyName = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9._-]*)\s*(?:\[[^\]]*\])?\s*===?\s*([^\s;#,]+)`)

// pep503 normalizes a PyPI name.
func pep503(n string) string {
	n = strings.ToLower(n)
	return strings.NewReplacer("_", "-", ".", "-").Replace(n)
}

// requirements*.txt: only exact pins (name==1.0) say which version is used.
func parseRequirements(data []byte) []Package {
	var out []Package
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if m := pyName.FindStringSubmatch(line); m != nil {
			out = append(out, Package{ecoPyPI, pep503(m[1]), m[2]})
		}
	}
	return sortPackages(out)
}

// Pipfile.lock: the default and develop sections.
func parsePipfileLock(data []byte) []Package {
	var lock map[string]json.RawMessage
	if json.Unmarshal(data, &lock) != nil {
		return nil
	}
	var out []Package
	for _, section := range []string{"default", "develop"} {
		var deps map[string]struct {
			Version string `json:"version"`
		}
		if json.Unmarshal(lock[section], &deps) != nil {
			continue
		}
		for name, d := range deps {
			if v := strings.TrimPrefix(d.Version, "=="); v != "" {
				out = append(out, Package{ecoPyPI, pep503(name), v})
			}
		}
	}
	return sortPackages(out)
}

// tomlPackages reads the [[package]] tables of poetry.lock, uv.lock and
// Cargo.lock: their name and version, skipping the project's own packages
// (a local path, an editable or virtual source; in Cargo, no source).
func tomlPackages(eco string) parser {
	return func(data []byte) []Package {
		var out []Package
		var cur *Package
		inPkg, local, hasSource := false, false, false
		flush := func() {
			if cur != nil && !local && (eco != ecoCargo || hasSource) {
				name := cur.Name
				if eco == ecoPyPI {
					name = pep503(name)
				}
				out = append(out, Package{eco, name, cur.Version})
			}
			cur, local, hasSource = nil, false, false
		}
		for _, raw := range strings.Split(string(data), "\n") {
			line := strings.TrimSpace(raw)
			if strings.HasPrefix(line, "[") {
				if line == "[[package]]" {
					flush()
					cur, inPkg = &Package{}, true
				} else {
					inPkg = false // a sub-table of the package, or another section
				}
				continue
			}
			if !inPkg || cur == nil {
				continue
			}
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			k, v = strings.TrimSpace(k), strings.TrimSpace(v)
			switch k {
			case "name":
				cur.Name = strings.Trim(v, `"'`)
			case "version":
				cur.Version = strings.Trim(v, `"'`)
			case "source":
				hasSource = true
				if strings.Contains(v, "editable") || strings.Contains(v, "virtual") || strings.Contains(v, "directory") || strings.Contains(v, "path") {
					local = true
				}
			}
		}
		flush()
		return sortPackages(out)
	}
}

// Gemfile.lock: the specs of the GEM section ("    name (1.0.0)"); a
// platform suffix (1.15.4-x86_64-linux) is not part of the version.
func parseGemfileLock(data []byte) []Package {
	var out []Package
	section, specs := "", false
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		if line[0] != ' ' {
			section, specs = strings.TrimSpace(line), false
			continue
		}
		if strings.TrimSpace(line) == "specs:" {
			specs = true
			continue
		}
		if section != "GEM" || !specs || !strings.HasPrefix(line, "    ") || strings.HasPrefix(line, "     ") {
			continue
		}
		name, rest, ok := strings.Cut(strings.TrimSpace(line), " (")
		if !ok {
			continue
		}
		v := strings.TrimSuffix(rest, ")")
		if i := strings.Index(v, "-"); i > 0 {
			v = v[:i]
		}
		out = append(out, Package{ecoGems, name, v})
	}
	return sortPackages(out)
}

// gradle.lockfile: group:artifact:version=configurations.
func parseGradleLock(data []byte) []Package {
	var out []Package
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "empty=") {
			continue
		}
		coord, _, _ := strings.Cut(line, "=")
		parts := strings.Split(coord, ":")
		if len(parts) == 3 {
			out = append(out, Package{ecoMaven, parts[0] + ":" + parts[1], parts[2]})
		}
	}
	return sortPackages(out)
}

// pom.xml: the dependencies with a version, literal or from a property of
// the same file. A version that comes from a parent or a BOM is not known
// here (osv-scanner resolves those).
func parsePom(data []byte) []Package {
	type dep struct {
		GroupID    string `xml:"groupId"`
		ArtifactID string `xml:"artifactId"`
		Version    string `xml:"version"`
	}
	var pom struct {
		Version    string `xml:"version"`
		Properties struct {
			Any []struct {
				XMLName xml.Name
				Value   string `xml:",chardata"`
			} `xml:",any"`
		} `xml:"properties"`
		Deps       []dep `xml:"dependencies>dependency"`
		ManagedDep []dep `xml:"dependencyManagement>dependencies>dependency"`
	}
	if xml.Unmarshal(data, &pom) != nil {
		return nil
	}
	props := map[string]string{"project.version": pom.Version}
	for _, p := range pom.Properties.Any {
		props[p.XMLName.Local] = strings.TrimSpace(p.Value)
	}
	var out []Package
	for _, d := range append(pom.Deps, pom.ManagedDep...) {
		v := strings.TrimSpace(d.Version)
		if strings.HasPrefix(v, "${") && strings.HasSuffix(v, "}") {
			v = props[v[2:len(v)-1]]
		}
		if v == "" || strings.ContainsAny(v, "$[(,") {
			continue // unresolved or a range
		}
		out = append(out, Package{ecoMaven, strings.TrimSpace(d.GroupID) + ":" + strings.TrimSpace(d.ArtifactID), v})
	}
	return sortPackages(out)
}

func def(v, d string) string {
	if v == "" {
		return d
	}
	return v
}
