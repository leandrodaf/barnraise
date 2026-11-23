package cli

import (
	"encoding/json"
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

var remoteRe = regexp.MustCompile(`(github\.com|gitlab\.com)[:/]([^/]+/[^/]+?)(\.git)?$`)

func parseRemote(url string) (host, repo string) {
	m := remoteRe.FindStringSubmatch(url)
	if m == nil {
		return "", ""
	}
	return strings.TrimSuffix(m[1], ".com"), m[2]
}
