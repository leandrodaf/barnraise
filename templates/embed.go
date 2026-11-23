// Package templates embeds the built-in project templates: roles, flows,
// checks and standards ready for a kind of project.
package templates

import (
	"embed"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
)

//go:embed base.yaml roles/*.md all:scaffold blank/* backend/* web/* mobile-android mobile-android/roles/* content content/roles/*
var files embed.FS

// Base returns the defaults every template starts from.
func Base() []byte {
	b, _ := files.ReadFile("base.yaml")
	return b
}

// Names lists the built-in templates.
func Names() []string {
	ents, _ := files.ReadDir(".")
	var out []string
	for _, e := range ents {
		if e.IsDir() && e.Name() != "roles" && e.Name() != "scaffold" {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// Config returns template.yaml of a built-in template.
func Config(name string) ([]byte, error) {
	if strings.Contains(name, "/") || strings.Contains(name, "..") {
		return nil, fmt.Errorf("invalid template name %q", name)
	}
	return files.ReadFile(name + "/template.yaml")
}

// Role returns the role prompt of a template, falling back to the shared roles.
func Role(template, role string) ([]byte, error) {
	if b, err := files.ReadFile(template + "/roles/" + role + ".md"); err == nil {
		return b, nil
	}
	return files.ReadFile("roles/" + role + ".md")
}

// Scaffold returns the files `barnraise init` writes into .barnraise/.
func Scaffold() map[string][]byte {
	out := map[string][]byte{}
	_ = fs.WalkDir(files, "scaffold", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, _ := files.ReadFile(p)
		if path.Base(p) == ".keep" {
			out[strings.TrimPrefix(path.Dir(p), "scaffold/")+"/"] = nil
			return nil
		}
		out[strings.TrimPrefix(p, "scaffold/")] = b
		return nil
	})
	return out
}

// RoleNames lists the shared and template-specific roles.
func RoleNames(template string) []string {
	seen := map[string]bool{}
	for _, dir := range []string{"roles", template + "/roles"} {
		ents, _ := files.ReadDir(dir)
		for _, e := range ents {
			if strings.HasSuffix(e.Name(), ".md") {
				seen[strings.TrimSuffix(e.Name(), ".md")] = true
			}
		}
	}
	var out []string
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Description returns the first comment line of a template.yaml.
func Description(name string) string {
	b, err := Config(name)
	if err != nil {
		return ""
	}
	first, _, _ := strings.Cut(string(b), "\n")
	return strings.TrimSpace(strings.TrimPrefix(first, "#"))
}
