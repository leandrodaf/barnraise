package knowledge

import (
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// frontMatter is the YAML header of a .barnraise document.
type frontMatter struct {
	ID           string     `yaml:"id"`
	Type         string     `yaml:"type"`
	Title        string     `yaml:"title"`
	Status       string     `yaml:"status"`
	Areas        stringList `yaml:"areas"`
	Links        stringList `yaml:"links"`
	Supersedes   stringList `yaml:"supersedes"`
	SupersededBy stringList `yaml:"superseded_by"`
	Goal         string     `yaml:"goal"`
	UpdatedAt    flexTime   `yaml:"updated_at"`
	ReviewBy     flexTime   `yaml:"review_by"`
	SourceSHA    string     `yaml:"source_sha"`
}

// stringList accepts a scalar or a sequence.
type stringList []string

func (s *stringList) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		if v := strings.TrimSpace(n.Value); v != "" && n.Tag != "!!null" {
			for _, p := range strings.Split(v, ",") {
				if p = strings.TrimSpace(p); p != "" {
					*s = append(*s, p)
				}
			}
		}
		return nil
	case yaml.SequenceNode:
		for _, c := range n.Content {
			if c.Kind == yaml.ScalarNode && strings.TrimSpace(c.Value) != "" {
				*s = append(*s, strings.TrimSpace(c.Value))
			}
		}
		return nil
	}
	return fmt.Errorf("line %d: expected a string or a list", n.Line)
}

// flexTime accepts dates and timestamps in common layouts.
type flexTime struct{ time.Time }

var timeLayouts = []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05", "2006-01-02T15:04", "2006-01-02"}

func (t *flexTime) UnmarshalYAML(n *yaml.Node) error {
	v := strings.TrimSpace(n.Value)
	if v == "" || n.Tag == "!!null" {
		return nil
	}
	for _, l := range timeLayouts {
		if p, err := time.Parse(l, v); err == nil {
			t.Time = p.UTC()
			return nil
		}
	}
	return nil // unknown date formats are ignored, never fatal
}

var knownFM = map[string]bool{"id": true, "type": true, "title": true, "status": true, "areas": true, "links": true,
	"supersedes": true, "superseded_by": true, "goal": true, "updated_at": true, "review_by": true, "source_sha": true}

// splitFrontMatter separates the YAML header from the markdown body.
func splitFrontMatter(src string) (header, body string) {
	src = strings.TrimPrefix(src, "\ufeff")
	src = strings.ReplaceAll(src, "\r\n", "\n")
	if !strings.HasPrefix(src, "---\n") {
		return "", src
	}
	rest := src[4:]
	if strings.HasPrefix(rest, "---\n") {
		return "", rest[4:]
	}
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		if strings.HasSuffix(rest, "\n---") {
			return rest[:len(rest)-4], ""
		}
		return "", src
	}
	return rest[:end], rest[end+5:]
}

var (
	reDecisionFile = regexp.MustCompile(`^(?i)(D|G|M)-(\d+)`)
	reHeading      = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*#*\s*$`)
)

// docIdentity derives the default kind and id of a .barnraise file from its
// path relative to .barnraise/.
func docIdentity(rel string) (kind, id string, areas []string) {
	rel = strings.TrimPrefix(rel, "./")
	base := strings.TrimSuffix(path.Base(rel), path.Ext(rel))
	dir := path.Dir(rel)
	switch {
	case rel == "INDEX.md":
		return "index", "index", nil
	case rel == "constitution.md":
		return "constitution", "constitution", nil
	case rel == "glossary.md":
		return "glossary", "glossary", nil
	case dir == "standards":
		return "standard", "standard:" + base, []string{base}
	case dir == "roles":
		return "role", "role:" + base, nil
	case dir == "decisions", dir == "goals", dir == "memories":
		k := map[string]string{"decisions": "decision", "goals": "goal", "memories": "memory"}[dir]
		if m := reDecisionFile.FindStringSubmatch(base); m != nil {
			return k, strings.ToUpper(m[1]) + "-" + m[2], nil
		}
		return k, k + ":" + base, nil
	}
	return "doc", "doc:" + strings.TrimSuffix(rel, ".md"), nil
}

// parseDoc turns a .barnraise markdown file into a document row.
func parseDoc(rel, src string) (*docRow, error) {
	kind, id, areas := docIdentity(rel)
	header, body := splitFrontMatter(src)
	var fm frontMatter
	meta := map[string]string{}
	if header != "" {
		if err := yaml.Unmarshal([]byte(header), &fm); err != nil {
			return nil, fmt.Errorf("%s: front matter: %w", rel, err)
		}
		var raw map[string]any
		if yaml.Unmarshal([]byte(header), &raw) == nil {
			for k, v := range raw {
				if knownFM[k] {
					continue
				}
				switch v.(type) {
				case map[string]any, []any:
					if b, err := yaml.Marshal(v); err == nil {
						meta[k] = strings.TrimSpace(string(b))
					}
				default:
					meta[k] = fmt.Sprint(v)
				}
			}
		}
	}
	if fm.ID != "" {
		id = canonicalID(fm.ID)
	}
	if fm.Type != "" {
		if kind == "doc" {
			kind = strings.ToLower(fm.Type)
		} else if !strings.EqualFold(fm.Type, kind) {
			meta["type"] = fm.Type
		}
	}
	title, summary := titleAndSummary(body)
	if fm.Title != "" {
		title = fm.Title
	}
	if title == "" {
		title = summary
	}
	if title == "" {
		title = id
	}
	d := &docRow{
		ID: id, Kind: kind, Title: oneLine(title, 200), Summary: oneLine(summary, 300), Body: strings.TrimSpace(body),
		Path: path.Join(".barnraise", rel), Status: fm.Status, Areas: append(areas, fm.Areas...), Goal: fm.Goal, Meta: meta,
		Updated: fm.UpdatedAt.Time, ReviewBy: fm.ReviewBy.Time, SourceSHA: fm.SourceSHA,
	}
	if len(fm.SupersededBy) > 0 {
		d.SupersededBy = canonicalID(fm.SupersededBy[0])
	}
	for _, l := range fm.Links {
		d.Links = append(d.Links, linkRow{canonicalID(l), "links"})
	}
	for _, l := range fm.Supersedes {
		d.Links = append(d.Links, linkRow{canonicalID(l), "supersedes"})
	}
	for _, l := range fm.SupersededBy {
		d.Links = append(d.Links, linkRow{canonicalID(l), "superseded_by"})
	}
	for _, a := range normAreas(d.Areas) {
		d.Links = append(d.Links, linkRow{domain.AreaID(a), "area"})
	}
	if fm.Goal != "" {
		d.Links = append(d.Links, linkRow{canonicalID(fm.Goal), "goal"})
	}
	d.Chunks = chunkMarkdown(d.Body)
	return d, nil
}

// canonicalID upper-cases short ids ("d-12" → "D-12") and leaves others.
func canonicalID(s string) string {
	s = strings.TrimSpace(s)
	if k, n, err := domain.ParseID(s); err == nil {
		return domain.FormatID(k, n)
	}
	if len(s) > 2 && (s[:2] == "a-" || s[:2] == "A-") {
		return domain.AreaID(s[2:])
	}
	return s
}

// titleAndSummary takes the first heading as the title and the first
// non-heading line as the one-line summary (the answer).
func titleAndSummary(body string) (title, summary string) {
	inFence := false
	for _, ln := range strings.Split(body, "\n") {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			inFence = !inFence
			continue
		}
		if t == "" || inFence {
			continue
		}
		if m := reHeading.FindStringSubmatch(t); m != nil {
			if title == "" && summary == "" {
				title = m[2]
			}
			continue
		}
		summary = strings.TrimSpace(strings.TrimLeft(t, ">-* "))
		break
	}
	return title, summary
}

// chunkMarkdown cuts a document by heading.
func chunkMarkdown(body string) []chunk {
	var out []chunk
	cur := chunk{Line: 1}
	var b strings.Builder
	inFence := false
	flush := func() {
		cur.Text = strings.TrimSpace(b.String())
		if cur.Text != "" || cur.Heading != "" {
			out = append(out, cur)
		}
		b.Reset()
	}
	for i, ln := range strings.Split(body, "\n") {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			inFence = !inFence
		}
		if !inFence {
			if m := reHeading.FindStringSubmatch(t); m != nil {
				flush()
				cur = chunk{Heading: m[2], Line: i + 1}
				b.WriteString(m[2])
				b.WriteByte('\n')
				continue
			}
		}
		b.WriteString(ln)
		b.WriteByte('\n')
	}
	flush()
	return out
}

func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		cut := max
		for cut > 0 && !utf8Start(s[cut]) {
			cut--
		}
		s = s[:cut] + "…"
	}
	return s
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

// areaSpec is one entry of .barnraise/areas.yaml.
type areaSpec struct {
	Paths     stringList `yaml:"paths" json:"paths"`
	Screens   stringList `yaml:"screens" json:"screens"`
	Contracts stringList `yaml:"contracts" json:"contracts"`
	Standard  string     `yaml:"standard" json:"standard"`
}

// parseAreas reads areas.yaml: a map area → spec, optionally under a
// top-level "areas:" key.
func parseAreas(src []byte) (map[string]areaSpec, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(src, &root); err != nil {
		return nil, fmt.Errorf("areas.yaml: %w", err)
	}
	if len(root.Content) == 0 {
		return map[string]areaSpec{}, nil
	}
	n := root.Content[0]
	if n.Kind == yaml.MappingNode && len(n.Content) == 2 && n.Content[0].Value == "areas" && n.Content[1].Kind == yaml.MappingNode {
		n = n.Content[1]
	}
	out := map[string]areaSpec{}
	if n.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("areas.yaml: expected a map of areas")
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		name := domain.AreaName(strings.TrimSpace(n.Content[i].Value))
		var spec areaSpec
		switch v := n.Content[i+1]; v.Kind {
		case yaml.SequenceNode, yaml.ScalarNode: // shorthand: area: [paths...]
			var paths stringList
			if err := v.Decode(&paths); err != nil {
				return nil, fmt.Errorf("areas.yaml %s: %w", name, err)
			}
			spec.Paths = paths
		default:
			if err := v.Decode(&spec); err != nil {
				return nil, fmt.Errorf("areas.yaml %s: %w", name, err)
			}
		}
		out[name] = spec
	}
	return out, nil
}

// standardID resolves the "standard" field of an area to a document id.
func standardID(area string, spec areaSpec) string {
	s := strings.TrimSpace(spec.Standard)
	switch {
	case s == "":
		return "standard:" + area
	case strings.HasPrefix(s, "standard:"):
		return s
	case strings.HasSuffix(s, ".md") || strings.Contains(s, "/"):
		s = strings.TrimPrefix(strings.TrimPrefix(s, "./"), ".barnraise/")
		_, id, _ := docIdentity(s)
		return id
	}
	return "standard:" + s
}

// pathInArea reports whether a file or folder belongs to an area pattern.
func pathInArea(pattern, p string) bool {
	pattern = strings.TrimPrefix(strings.TrimSpace(pattern), "./")
	p = strings.TrimPrefix(p, "./")
	if pattern == "" || p == "" {
		return false
	}
	if pattern == p || domain.MatchGlob(pattern, p) {
		return true
	}
	base := strings.TrimSuffix(strings.TrimSuffix(pattern, "/**"), "/")
	if strings.ContainsAny(base, "*?[") {
		// glob folder: match the folder itself against the pattern prefix
		return domain.MatchGlob(base, strings.TrimSuffix(p, "/"))
	}
	if strings.HasPrefix(p, base+"/") || strings.TrimSuffix(p, "/") == base {
		return true
	}
	// a folder that contains the area ("app/" contains "app/cards/")
	return strings.HasSuffix(p, "/") && strings.HasPrefix(base+"/", p)
}
