package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Detected is what `init` finds in a repository.
type Detected struct {
	Languages []string `json:"languages"`
	Template  string   `json:"template"`
	Check     string   `json:"check"`
	Lint      string   `json:"lint,omitempty"`
	CI        []string `json:"ci,omitempty"`
	Sensitive []string `json:"sensitive,omitempty"`
	Remote    string   `json:"remote,omitempty"` // owner/name on GitHub or GitLab
	Host      string   `json:"host,omitempty"`   // github, gitlab
	Mockup    string   `json:"mockup,omitempty"`
	Readme    string   `json:"readme,omitempty"`
}

// Detect inspects a repository: languages, test commands (CI, Makefile,
// Gradle, package.json), sensitive folders and the git remote.
func Detect(root string) Detected {
	d := Detected{}
	has := func(p string) bool { _, err := os.Stat(filepath.Join(root, p)); return err == nil }
	read := func(p string) string { b, _ := os.ReadFile(filepath.Join(root, p)); return string(b) }
	if has("go.mod") {
		d.Languages = append(d.Languages, "go")
		d.Check = "go test ./..."
		d.Lint = "go vet ./..."
	}
	if has("build.gradle") || has("build.gradle.kts") || has("settings.gradle.kts") || has("settings.gradle") {
		d.Languages = append(d.Languages, "kotlin")
		if has("app/src/main/AndroidManifest.xml") || strings.Contains(read("app/build.gradle.kts")+read("app/build.gradle"), "com.android.application") {
			d.Template = "mobile-android"
			d.Check = "./gradlew testDebugUnitTest --console=plain"
		} else if d.Check == "" {
			d.Check = "./gradlew test --console=plain"
		}
	}
	if has("package.json") {
		d.Languages = append(d.Languages, "javascript")
		var pkg struct {
			Scripts map[string]string `json:"scripts"`
		}
		_ = json.Unmarshal([]byte(read("package.json")), &pkg)
		if _, ok := pkg.Scripts["test"]; ok && d.Check == "" {
			d.Check = "npm test --silent"
		}
		if _, ok := pkg.Scripts["lint"]; ok && d.Lint == "" {
			d.Lint = "npm run lint --silent"
		}
		if d.Template == "" {
			d.Template = "web"
		}
	}
	if has("pyproject.toml") || has("requirements.txt") || has("setup.py") {
		d.Languages = append(d.Languages, "python")
		if d.Check == "" {
			d.Check = "python -m pytest -q"
		}
	}
	if has("Cargo.toml") {
		d.Languages = append(d.Languages, "rust")
		if d.Check == "" {
			d.Check = "cargo test"
		}
	}
	if mk := read("Makefile"); mk != "" {
		if regexp.MustCompile(`(?m)^test:`).MatchString(mk) {
			d.Check = "make test"
		}
		if regexp.MustCompile(`(?m)^lint:`).MatchString(mk) {
			d.Lint = "make lint"
		}
	}
	if d.Template == "" {
		switch {
		case has("content") || has("posts") || has("_posts"):
			d.Template = "content"
		case len(d.Languages) > 0:
			d.Template = "backend"
		default:
			d.Template = "blank"
		}
	}
	if d.Check == "" {
		d.Check = "make test"
	}
	if ents, err := os.ReadDir(filepath.Join(root, ".github", "workflows")); err == nil {
		for _, e := range ents {
			d.CI = append(d.CI, ".github/workflows/"+e.Name())
		}
	}
	if has(".gitlab-ci.yml") {
		d.CI = append(d.CI, ".gitlab-ci.yml")
	}
	_ = filepath.WalkDir(root, func(p string, de os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := de.Name()
		if de.IsDir() && (name == ".git" || name == "node_modules" || name == "build" || name == "vendor" || name == ".gradle" || name == "dist") {
			return filepath.SkipDir
		}
		rel, _ := filepath.Rel(root, p)
		if !de.IsDir() && (strings.HasSuffix(name, ".keystore") || strings.HasSuffix(name, ".jks") || name == ".env" || strings.HasPrefix(name, ".env.") || name == "google-services.json") {
			d.Sensitive = append(d.Sensitive, rel)
		}
		if de.IsDir() && (strings.Contains(strings.ToLower(name), "mockup") || strings.Contains(strings.ToLower(name), "prototipo") || strings.Contains(strings.ToLower(name), "prototype")) && d.Mockup == "" {
			d.Mockup = rel
		}
		return nil
	})
	sort.Strings(d.Sensitive)
	if out, err := exec.Command("git", "-C", root, "remote", "get-url", "origin").Output(); err == nil {
		d.Host, d.Remote = parseRemote(strings.TrimSpace(string(out)))
	}
	for _, r := range []string{"README.md", "README", "readme.md"} {
		if s := read(r); s != "" {
			if len(s) > 4000 {
				s = s[:4000]
			}
			d.Readme = s
			break
		}
	}
	return d
}

// Part is one part of a repository that holds several: a backend, a web
// app, a mobile app, each in its own folder.
type Part struct {
	Dir    string            `json:"dir"`
	Stack  string            `json:"stack"`
	About  string            `json:"about"`
	Checks map[string]string `json:"checks"`
}

// DetectParts looks for folders that are projects of their own (a go.mod, a
// package.json, a Gradle build, a pyproject, a Cargo.toml), one or two
// levels under the root.
func DetectParts(root string) []Part {
	var parts []Part
	seen := map[string]bool{}
	skip := map[string]bool{"node_modules": true, "vendor": true, "dist": true, "build": true, "target": true, "Pods": true}
	var scan func(rel string, depth int)
	scan = func(rel string, depth int) {
		ents, err := os.ReadDir(filepath.Join(root, rel))
		if err != nil {
			return
		}
		for _, e := range ents {
			name := e.Name()
			if !e.IsDir() || strings.HasPrefix(name, ".") || skip[name] {
				continue
			}
			dir := filepath.ToSlash(filepath.Join(rel, name))
			if p, ok := detectPart(root, dir); ok {
				if !seen[dir] {
					seen[dir] = true
					parts = append(parts, p)
				}
				continue // a part is not searched for parts inside it
			}
			if depth < 2 {
				scan(dir, depth+1)
			}
		}
	}
	scan("", 1)
	sort.Slice(parts, func(i, j int) bool { return parts[i].Dir < parts[j].Dir })
	return parts
}

func detectPart(root, dir string) (Part, bool) {
	has := func(p string) bool { _, err := os.Stat(filepath.Join(root, dir, p)); return err == nil }
	read := func(p string) string { b, _ := os.ReadFile(filepath.Join(root, dir, p)); return string(b) }
	p := Part{Dir: dir, Checks: map[string]string{}}
	switch {
	case has("go.mod"):
		p.Stack, p.About = "go", "Go module"
		p.Checks["test"], p.Checks["vet"] = "go test ./...", "go vet ./..."
	case has("package.json"):
		var pkg struct {
			Scripts map[string]string `json:"scripts"`
			Deps    map[string]string `json:"dependencies"`
			Dev     map[string]string `json:"devDependencies"`
		}
		_ = json.Unmarshal([]byte(read("package.json")), &pkg)
		dep := func(n string) bool { _, a := pkg.Deps[n]; _, b := pkg.Dev[n]; return a || b }
		switch {
		case dep("react-native") || dep("expo"):
			p.Stack, p.About = "react-native", "Mobile app (React Native)"
		case dep("next"):
			p.Stack, p.About = "next", "Web app (Next.js)"
		case dep("react"):
			p.Stack, p.About = "react", "Web app (React)"
		case dep("vue"):
			p.Stack, p.About = "vue", "Web app (Vue)"
		default:
			p.Stack, p.About = "javascript", "JavaScript package"
		}
		pm := "npm"
		switch {
		case has("pnpm-lock.yaml"):
			pm = "pnpm"
		case has("yarn.lock"):
			pm = "yarn"
		case has("bun.lockb") || has("bun.lock"):
			pm = "bun"
		}
		for _, sname := range []string{"lint", "typecheck", "test"} {
			if _, ok := pkg.Scripts[sname]; ok {
				p.Checks[sname] = pm + " run " + sname
			}
		}
	case has("build.gradle") || has("build.gradle.kts") || has("settings.gradle") || has("settings.gradle.kts"):
		p.Stack, p.About = "kotlin", "Gradle project"
		p.Checks["test"] = "./gradlew test --console=plain"
		if has("app/src/main/AndroidManifest.xml") {
			p.Stack, p.About = "android", "Android app"
			p.Checks["test"] = "./gradlew testDebugUnitTest --console=plain"
		}
	case has("pyproject.toml") || has("requirements.txt") || has("setup.py"):
		p.Stack, p.About = "python", "Python project"
		p.Checks["test"] = "python -m pytest -q"
	case has("Cargo.toml"):
		p.Stack, p.About = "rust", "Rust crate"
		p.Checks["test"] = "cargo test"
	default:
		return Part{}, false
	}
	return p, true
}

// renderPart writes the area file of a part.
func renderPart(p Part) string {
	var b strings.Builder
	b.WriteString("# One part of the repository. Barnraise finds this file by itself: its checks\n")
	b.WriteString("# run inside this folder, and only for changes that touch it.\n")
	fmt.Fprintf(&b, "about: %s\nstack: %s\n", yamlStr(p.About), p.Stack)
	if len(p.Checks) > 0 {
		b.WriteString("checks:\n")
		names := make([]string, 0, len(p.Checks))
		for n := range p.Checks {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			fmt.Fprintf(&b, "  %s: %s\n", n, yamlStr(p.Checks[n]))
		}
	}
	return b.String()
}

var remoteRe = regexp.MustCompile(`(github\.com|gitlab\.com)[:/]([^/]+/[^/]+?)(\.git)?$`)

func parseRemote(url string) (host, repo string) {
	m := remoteRe.FindStringSubmatch(url)
	if m == nil {
		return "", ""
	}
	return strings.TrimSuffix(m[1], ".com"), m[2]
}
