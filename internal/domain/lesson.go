package domain

import (
	"fmt"
	"hash/fnv"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
)

// LessonsDef configures the lessons Barnraise distills from rework.
type LessonsDef struct {
	Enabled bool `json:"enabled"`
	// Max bounds the lessons given to one step's prompt.
	Max int `json:"max"`
}

// DefaultLessons is on, with at most five lessons per prompt.
func DefaultLessons() LessonsDef { return LessonsDef{Enabled: true, Max: 5} }

// Lesson is what one or more resolved rework rounds taught: why the work
// was sent back and what fixed it, tagged by the files, areas and checks it
// concerns, so items that touch the same places hear it before they repeat it.
type Lesson struct {
	ID      string   `json:"id"`
	Text    string   `json:"text"`
	Reason  string   `json:"reason"`            // why the work came back, short
	Step    string   `json:"step"`              // the step that sent it back
	Outcome string   `json:"outcome,omitempty"` // fail, reject, rework
	Checks  []string `json:"checks,omitempty"`
	Areas   []string `json:"areas,omitempty"`
	Files   []string `json:"files,omitempty"` // the files of the fix
	// Sources are the rework rounds it came from ("I-42#2").
	Sources   []string  `json:"sources"`
	Count     int       `json:"count"` // how many rounds taught it
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"created_at,omitzero"`
	UpdatedAt time.Time `json:"updated_at,omitzero"`
}

// Clone copies a lesson.
func (l *Lesson) Clone() *Lesson {
	c := *l
	c.Checks = append([]string(nil), l.Checks...)
	c.Areas = append([]string(nil), l.Areas...)
	c.Files = append([]string(nil), l.Files...)
	c.Sources = append([]string(nil), l.Sources...)
	return &c
}

// lessonFiles bounds the files a lesson keeps.
const lessonFiles = 12

// LessonSource names one rework round of an item ("I-42#review@1767225600").
func LessonSource(itemID string, rr ReworkRecord) string {
	return fmt.Sprintf("%s#%s@%d", itemID, rr.From, rr.At.Unix())
}

// LessonID is derived from the round it was first learned from, so the
// same round never gives two lessons, whichever machine distills it.
func LessonID(source string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(source))
	return fmt.Sprintf("L-%08x", h.Sum32())
}

// IsLessonID reports whether s looks like a lesson id.
func IsLessonID(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) != 10 || !strings.HasPrefix(strings.ToUpper(s), "L-") {
		return false
	}
	for _, r := range strings.ToLower(s[2:]) {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// NormalizeLessonID accepts "l-3fa9c1d2" and returns "L-3fa9c1d2".
func NormalizeLessonID(s string) string {
	s = strings.TrimSpace(s)
	if len(s) < 2 {
		return s
	}
	return "L-" + strings.ToLower(s[2:])
}

// DistillLesson turns a resolved rework round into a lesson, without any
// model: the reason the work came back and the files the fix changed.
// checks are the project checks the reason names.
func DistillLesson(it *Item, rr ReworkRecord, fixFiles, checks []string, now time.Time) *Lesson {
	src := LessonSource(it.ID, rr)
	l := &Lesson{ID: LessonID(src), Reason: shortReason(SanitizeExternal(rr.Reason)), Step: rr.From, Outcome: rr.Outcome,
		Checks: dedupe(checks), Files: boundFiles(fixFiles), Sources: []string{src}, Count: 1, Version: 1, CreatedAt: now, UpdatedAt: now}
	if it.Area != "" {
		l.Areas = []string{AreaName(it.Area)}
	}
	l.Areas = dedupe(append(l.Areas, it.Anchors.Areas...))
	l.Text = l.Render()
	return l
}

// Render is the lesson as one line for a prompt.
func (l *Lesson) Render() string {
	var b strings.Builder
	where := "this code"
	if len(l.Areas) > 0 {
		where = "area " + strings.Join(l.Areas, ", ")
	}
	by := "step " + l.Step
	if len(l.Checks) > 0 {
		by = "check " + strings.Join(l.Checks, ", ")
	}
	fmt.Fprintf(&b, "In %s, %s sent the work back: %s", where, by, l.Reason)
	if !strings.HasSuffix(l.Reason, ".") {
		b.WriteString(".")
	}
	if len(l.Files) > 0 {
		shown := l.Files
		if len(shown) > 4 {
			shown = shown[:4]
		}
		fmt.Fprintf(&b, " The fix changed %s", strings.Join(shown, ", "))
		if n := len(l.Files) - len(shown); n > 0 {
			fmt.Fprintf(&b, " and %d more", n)
		}
		b.WriteString(".")
	}
	if l.Count > 1 {
		fmt.Fprintf(&b, " (seen %d times)", l.Count)
	}
	return b.String()
}

// shortReason keeps the first meaningful lines of a reason, up to 240 characters.
func shortReason(r string) string {
	var parts []string
	n := 0
	fenced := false // logs and code blocks are not the reason
	for line := range strings.SplitSeq(r, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "-*•"))
		if strings.HasPrefix(line, "```") {
			fenced = !fenced
			continue
		}
		if line == "" || fenced {
			continue
		}
		parts = append(parts, line)
		n += len(line)
		if n >= 240 {
			break
		}
	}
	s := strings.Join(parts, " ")
	if len(s) > 240 {
		s = strings.TrimSpace(s[:240]) + "…"
	}
	if s == "" {
		s = "no reason given"
	}
	return s
}

func boundFiles(files []string) []string {
	files = dedupe(files)
	sort.Strings(files)
	if len(files) > lessonFiles {
		files = files[:lessonFiles]
	}
	return files
}

var checkNameRe = regexp.MustCompile(`\bcheck ([A-Za-z0-9_.:-]+)`)

// ChecksNamed returns the project checks a rework reason names ("check
// unit failed: …"), in the order of known.
func ChecksNamed(reason string, known []string) []string {
	named := map[string]bool{}
	for _, m := range checkNameRe.FindAllStringSubmatch(reason, -1) {
		named[strings.TrimRight(m[1], ".:")] = true
	}
	var out []string
	for _, k := range known {
		if named[k] {
			out = append(out, k)
		}
	}
	return out
}

// stopWords are left out when comparing reasons.
var stopWords = map[string]bool{"the": true, "and": true, "for": true, "with": true, "that": true, "this": true, "not": true,
	"was": true, "are": true, "from": true, "but": true, "has": true, "have": true, "when": true, "should": true, "must": true,
	"failed": true, "fails": true, "check": true, "work": true, "back": true, "sent": true}

func reasonWords(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_'
	}) {
		if len(w) >= 3 && !stopWords[w] && strings.Trim(w, "0123456789") != "" {
			out[w] = true
		}
	}
	return out
}

// Similar reports whether two lessons say the same thing: the same step
// and checks, close reasons, about the same places.
func (l *Lesson) Similar(o *Lesson) bool {
	if l.Step != o.Step || strings.Join(l.Checks, ",") != strings.Join(o.Checks, ",") {
		return false
	}
	a, b := reasonWords(l.Reason), reasonWords(o.Reason)
	inter := 0
	for w := range a {
		if b[w] {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 || float64(inter)/float64(union) < 0.5 {
		return false
	}
	if overlap(l.Areas, o.Areas) || overlap(l.Files, o.Files) || overlap(dirsOf(l.Files), dirsOf(o.Files)) {
		return true
	}
	return len(l.Areas) == 0 && len(o.Areas) == 0 && len(l.Files) == 0 && len(o.Files) == 0
}

// Absorb merges a similar lesson into this one.
func (l *Lesson) Absorb(o *Lesson, now time.Time) {
	l.Files = boundFiles(append(l.Files, o.Files...))
	l.Areas = dedupe(append(l.Areas, o.Areas...))
	l.Sources = dedupe(append(l.Sources, o.Sources...))
	l.Count += max(o.Count, 1)
	l.Version++
	l.UpdatedAt = now
	l.Text = l.Render()
}

func overlap(a, b []string) bool {
	for _, x := range a {
		if x != "" && contains(b, x) {
			return true
		}
	}
	return false
}

func dirsOf(files []string) []string {
	var out []string
	for _, f := range files {
		if d := path.Dir(f); d != "." && d != "/" {
			out = append(out, d)
		}
	}
	return dedupe(out)
}

// RelevantLessons picks the lessons that concern an item: the files it
// touches or is anchored to, their folders and its areas. The most relevant
// first, at most limit; lessons that concern none of it are left out.
func RelevantLessons(lessons []*Lesson, files, areas []string, limit int) []*Lesson {
	if limit <= 0 {
		return nil
	}
	dirs := dirsOf(files)
	var areaNames []string
	for _, a := range areas {
		if a != "" {
			areaNames = append(areaNames, AreaName(a))
		}
	}
	type scored struct {
		l     *Lesson
		score int
	}
	var out []scored
	for _, l := range lessons {
		score := 0
		for _, f := range l.Files {
			if contains(files, f) {
				score += 3
			}
		}
		for _, d := range dirsOf(l.Files) {
			if contains(dirs, d) {
				score++
			}
		}
		for _, a := range l.Areas {
			if contains(areaNames, a) {
				score += 2
			}
		}
		if score > 0 {
			out = append(out, scored{l, score})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.score != b.score {
			return a.score > b.score
		}
		if a.l.Count != b.l.Count {
			return a.l.Count > b.l.Count
		}
		if !a.l.UpdatedAt.Equal(b.l.UpdatedAt) {
			return a.l.UpdatedAt.After(b.l.UpdatedAt)
		}
		return a.l.ID < b.l.ID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	res := make([]*Lesson, len(out))
	for i, s := range out {
		res[i] = s.l
	}
	return res
}

// LessonList returns the lessons, the most taught first.
func (s *State) LessonList() []*Lesson {
	out := make([]*Lesson, 0, len(s.Lessons))
	for _, l := range s.Lessons {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// applyLesson folds a lesson event into the state.
func (s *State) applyLesson(typ string, l *Lesson) {
	if s.Lessons == nil {
		s.Lessons = map[string]*Lesson{}
	}
	if typ == EvLessonDeleted {
		delete(s.Lessons, l.ID)
		return
	}
	if cur := s.Lessons[l.ID]; cur == nil || l.Version >= cur.Version {
		s.Lessons[l.ID] = l
	}
}

// Source is the rework round the lesson was first learned from.
func (l *Lesson) Source() string {
	if len(l.Sources) == 0 {
		return ""
	}
	return l.Sources[0]
}
