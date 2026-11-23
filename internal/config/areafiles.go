package config

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// A repository often holds several parts side by side: a backend, a web
// app, a mobile app. Each part may carry its own barnraise.yaml in its
// folder, saying what it is and how it is checked. Barnraise finds these
// files by itself and turns each one into an area: the folder is the
// area's paths, and its checks run inside the folder, only for changes
// that touch it.

// areaFileDepth is how deep under the root area files are looked for.
const areaFileDepth = 3

// skipDirs are never searched for area files.
var skipDirs = map[string]bool{"node_modules": true, "vendor": true, "dist": true, "build": true, "target": true, "Pods": true, "worktrees": true}

type areaFile struct {
	Area      string                `yaml:"area"`
	About     string                `yaml:"about"`
	Stack     string                `yaml:"stack"`
	Standard  string                `yaml:"standard"`
	Screens   []string              `yaml:"screens"`
	Contracts []string              `yaml:"contracts"`
	AlsoWhen  []string              `yaml:"also_when"`
	Checks    map[string]checkField `yaml:"checks"`
	Kinds     map[string]string     `yaml:"kinds"`
	// A sub-folder file is not a project file: these keys belong in the root.
	Project  string    `yaml:"project"`
	Template string    `yaml:"template"`
	Flows    yaml.Node `yaml:"flows"`
	Roles    yaml.Node `yaml:"roles"`
}

// AreaFiles lists the folders (relative, slash-separated, sorted) that hold
// an area file under root.
func AreaFiles(root string) []string {
	var out []string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if d.IsDir() {
			name := d.Name()
			if rel != "." && (strings.HasPrefix(name, ".") || skipDirs[name]) {
				return filepath.SkipDir
			}
			if rel != "." && strings.Count(filepath.ToSlash(rel), "/") >= areaFileDepth {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() == FileName && filepath.Dir(rel) != "." {
			out = append(out, filepath.ToSlash(filepath.Dir(rel)))
		}
		return nil
	})
	sort.Strings(out)
	return out
}

// loadAreaFiles merges the area files into the compiled config. userChecks
// are the checks the root file declares itself.
func loadAreaFiles(root string, c *domain.ProjectConfig, userChecks map[string]bool) error {
	added := 0
	fromTemplate := map[string]bool{}
	for n := range c.Checks {
		if !userChecks[n] {
			fromTemplate[n] = true
		}
	}
	for _, dir := range AreaFiles(root) {
		file := dir + "/" + FileName
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
		if err != nil {
			return fmt.Errorf("%s: %w", file, err)
		}
		var af areaFile
		if err := yaml.Unmarshal(data, &af); err != nil {
			return fmt.Errorf("%s: %w", file, friendlyYAML(err))
		}
		if af.Project != "" || af.Template != "" || af.Flows.Kind != 0 || af.Roles.Kind != 0 {
			return fmt.Errorf("%s describes one part of the repository: it may set area, about, stack, standard, screens, contracts, also_when, kinds and checks; project, template, flows and roles belong in the %s at the root", file, FileName)
		}
		name := domain.AreaName(def(af.Area, filepath.Base(dir)))
		if prev, ok := c.Areas[name]; ok {
			where := FileName
			if prev.Dir != "" {
				where = prev.Dir + "/" + FileName
			}
			return fmt.Errorf("area %s is declared in %s and in %s; keep one", name, where, file)
		}
		standard := af.Standard
		if standard != "" {
			// Relative to the part's folder; it may point at the shared
			// .barnraise/standards of the root ("../.barnraise/standards/x.md").
			standard = path.Clean(dir + "/" + standard)
			if strings.HasPrefix(standard, "../") || standard == ".." {
				return fmt.Errorf("%s: standard %q points outside the repository", file, af.Standard)
			}
		}
		c.Areas[name] = domain.AreaDef{Name: name, Paths: []string{dir + "/**"}, Screens: af.Screens, Contracts: af.Contracts,
			Standard: standard, Dir: dir, About: strings.TrimSpace(af.About), Stack: strings.TrimSpace(af.Stack), KindFlows: af.Kinds}
		for cname, cf := range af.Checks {
			full := name + "-" + cname
			if _, ok := c.Checks[full]; ok {
				return fmt.Errorf("%s: check %s already exists; rename it here or in the root", file, full)
			}
			// Inside an area file, checks name each other by their short names.
			var covers []string
			for _, o := range cf.Covers {
				if _, local := af.Checks[o]; local {
					o = name + "-" + o
				}
				covers = append(covers, o)
			}
			d := domain.CheckDef{Name: full, Covers: covers, Order: cf.Order, NoOffload: cf.Offload != nil && !*cf.Offload, Run: cf.Run, RunsOn: def(cf.RunsOn, "local"), Workflow: cf.Workflow, URL: cf.URL,
				Scope: def(cf.Scope, "touched"), Areas: []string{name}, Paths: append(append([]string{}, af.AlsoWhen...), cf.Paths...),
				Dir: dir, FlakyRetries: 3, Timeout: 30 * time.Minute}
			if cf.FlakyRetries != nil {
				d.FlakyRetries = *cf.FlakyRetries
			}
			if cf.Timeout != "" {
				t, err := domain.ParseDuration(cf.Timeout)
				if err != nil {
					return fmt.Errorf("%s: checks.%s.timeout: %w", file, cname, err)
				}
				d.Timeout = t
			}
			c.Checks[full] = d
			added++
		}
	}
	if added == 0 {
		return nil
	}
	// The parts bring their own checks: the template's guesses (a single
	// `make test` for the whole repository) give way, and the flow steps
	// that pointed at them run every check that applies to what the item
	// touched. Checks the root file declares itself are kept as they are.
	for n := range fromTemplate {
		delete(c.Checks, n)
	}
	for _, f := range c.Flows {
		for i := range f.Steps {
			if f.Steps[i].Type == domain.StepCheck && fromTemplate[domain.TrimChecks(f.Steps[i].Run)] {
				f.Steps[i].Run = "checks"
			}
		}
	}
	return nil
}
