package git

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// StructuralDriver is the name Barnraise registers its structural merge
// driver under (merge.<name>.driver), apart from any driver the user has.
const StructuralDriver = "barnraise-structural"

// The managed block of <common dir>/info/attributes. Lines outside it are
// the user's and are kept as they are.
const (
	attrBegin = "# >>> barnraise structural merge (managed; set merge.structural: off to remove) >>>"
	attrEnd   = "# <<< barnraise structural merge <<<"
)

// rerereManagedKey marks the rerere keys Barnraise set itself, so turning
// merge.rerere off removes only those.
const rerereManagedKey = "barnraise.rerere"

// MergeSetup is how the repository merges, from the project's merge section.
type MergeSetup struct {
	// Rerere turns git's record of conflict resolutions on (when the user has
	// not set it either way) or, false, removes what Barnraise set.
	Rerere bool
	// Structural is "auto" (use the structural driver when installed) or "off".
	Structural string
	// StructuralBin is the driver's program; empty means "mergiraf" on PATH.
	StructuralBin string
	// LookPath finds the program (exec.LookPath when nil).
	LookPath func(string) (string, error)
}

// MergeSetupReport says what the repository uses after SetupMerge.
type MergeSetupReport struct {
	Rerere     bool   // rerere.enabled in effect
	RerereNote string // why, when it is not Barnraise's doing
	Structural string // the driver program in use, "" when none
	Patterns   int    // file patterns sent to it
	Note       string // why there is no structural driver
}

// SetupMerge configures the repository's local config and shared
// info/attributes (never a tracked file) so that every worktree, the item
// worktrees and the merge queue's checkouts alike, rebases with recorded
// resolutions and, when installed and allowed, a structural merge driver.
// It is idempotent and runs at every start and reload.
func (r *Repo) SetupMerge(ctx context.Context, s MergeSetup) (MergeSetupReport, error) {
	var rep MergeSetupReport
	if err := r.setupRerere(ctx, s.Rerere, &rep); err != nil {
		return rep, fmt.Errorf("rerere: %w", err)
	}
	if err := r.setupStructural(ctx, s, &rep); err != nil {
		return rep, fmt.Errorf("structural merge: %w", err)
	}
	return rep, nil
}

// configGet returns the effective value of key and whether any scope sets it.
func (r *Repo) configGet(ctx context.Context, args ...string) (string, bool, error) {
	out, err := r.git(ctx, "", append([]string{"config"}, args...)...)
	if err != nil {
		if exitCode(err) == 1 {
			return "", false, nil
		}
		return "", false, err
	}
	return strings.TrimSpace(out), true, nil
}

func (r *Repo) setupRerere(ctx context.Context, on bool, rep *MergeSetupReport) error {
	keys := []string{"rerere.enabled", "rerere.autoupdate"}
	_, managed, err := r.configGet(ctx, "--local", "--get", rerereManagedKey)
	if err != nil {
		return err
	}
	if on {
		for _, k := range keys {
			_, set, err := r.configGet(ctx, "--get", k)
			if err != nil {
				return err
			}
			if set {
				continue // the user decided; keep it
			}
			if _, err := r.git(ctx, "", "config", "--local", k, "true"); err != nil {
				return err
			}
			if _, err := r.git(ctx, "", "config", "--local", rerereManagedKey, "true"); err != nil {
				return err
			}
		}
	} else if managed {
		for _, k := range keys {
			_, _ = r.git(ctx, "", "config", "--local", "--unset", k)
		}
		_, _ = r.git(ctx, "", "config", "--local", "--unset", rerereManagedKey)
	}
	v, set, err := r.configGet(ctx, "--bool", "--get", "rerere.enabled")
	if err != nil {
		return err
	}
	rep.Rerere = v == "true"
	switch {
	case on && !rep.Rerere:
		rep.RerereNote = "rerere.enabled is false in the git config; resolutions are not recorded"
	case !on && rep.Rerere:
		rep.RerereNote = "merge.rerere is off, but rerere.enabled is set in the git config"
	case !set && !on:
		rep.RerereNote = "off (merge.rerere: false)"
	}
	return nil
}

func (r *Repo) setupStructural(ctx context.Context, s MergeSetup, rep *MergeSetupReport) error {
	if s.Structural == "off" {
		rep.Note = "off (merge.structural: off)"
		return r.removeStructural(ctx)
	}
	bin := s.StructuralBin
	if bin == "" {
		bin = "mergiraf"
	}
	look := s.LookPath
	if look == nil {
		look = exec.LookPath
	}
	path, err := look(bin)
	if err != nil {
		rep.Note = bin + " is not installed; the normal merge is used"
		return r.removeStructural(ctx)
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	patterns, err := structuralPatterns(ctx, path)
	if err != nil || len(patterns) == 0 {
		rep.Note = fmt.Sprintf("%s did not list the files it supports (%v); the normal merge is used", bin, err)
		return r.removeStructural(ctx)
	}
	own := r.projectMergePatterns()
	var lines []string
	for _, p := range patterns {
		if !own[p] {
			lines = append(lines, p+" merge="+StructuralDriver)
		}
	}
	// Where the program is missing (an agent's container, an uninstall since
	// the daemon started) the driver falls back to git's own line merge, with
	// conflict markers, instead of failing the merge without them.
	q := shellQuote(path)
	driver := "if [ -x " + q + " ]; then exec " + q + " merge --git %O %A %B -s %S -x %X -y %Y -p %P -l %L; " +
		"else exec git merge-file --marker-size=%L -L %X -L %S -L %Y %A %O %B; fi"
	if _, err := r.git(ctx, "", "config", "--local", "merge."+StructuralDriver+".name", "structural merge set up by Barnraise"); err != nil {
		return err
	}
	if _, err := r.git(ctx, "", "config", "--local", "merge."+StructuralDriver+".driver", driver); err != nil {
		return err
	}
	if err := r.writeAttributes(ctx, lines); err != nil {
		return err
	}
	rep.Structural, rep.Patterns = path, len(lines)
	return nil
}

// removeStructural takes Barnraise's driver out: the managed block and the
// driver's config section. The user's own lines and drivers stay.
func (r *Repo) removeStructural(ctx context.Context) error {
	if err := r.writeAttributes(ctx, nil); err != nil {
		return err
	}
	if _, set, _ := r.configGet(ctx, "--local", "--get", "merge."+StructuralDriver+".driver"); set {
		_, _ = r.git(ctx, "", "config", "--local", "--remove-section", "merge."+StructuralDriver)
	}
	return nil
}

// patternRe is what a gitattributes pattern from the driver may look like.
var patternRe = regexp.MustCompile(`^[A-Za-z0-9._*?/\[\]+-]+$`)

// structuralPatterns asks the driver which files it handles, in the
// gitattributes form ("*.rs merge=mergiraf").
func structuralPatterns(ctx context.Context, bin string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "languages", "--gitattributes").Output()
	if err != nil {
		return nil, err
	}
	var patterns []string
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 2 || !strings.HasPrefix(f[1], "merge=") || !patternRe.MatchString(f[0]) {
			continue
		}
		patterns = appendNew(patterns, f[0])
	}
	if len(patterns) == 0 {
		return nil, errors.New("no patterns")
	}
	return patterns, nil
}

// projectMergePatterns lists the patterns the project's own .gitattributes
// gives a merge behaviour (merge=, -merge, !merge, binary). info/attributes
// wins over .gitattributes, so Barnraise leaves those patterns out instead
// of overriding the project's choice.
func (r *Repo) projectMergePatterns() map[string]bool {
	out := map[string]bool{}
	data, err := os.ReadFile(filepath.Join(r.root, ".gitattributes"))
	if err != nil {
		return out
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || strings.HasPrefix(f[0], "#") {
			continue
		}
		for _, a := range f[1:] {
			if a == "merge" || a == "-merge" || a == "!merge" || a == "binary" || strings.HasPrefix(a, "merge=") {
				out[f[0]] = true
			}
		}
	}
	return out
}

// writeAttributes replaces the managed block of info/attributes with lines
// (none removes it), keeping the rest of the file.
func (r *Repo) writeAttributes(ctx context.Context, lines []string) error {
	common, err := r.CommonDir(ctx)
	if err != nil {
		return err
	}
	path := filepath.Join(common, "info", "attributes")
	cur, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	next := replaceBlock(string(cur), lines)
	if next == string(cur) {
		return nil
	}
	if next == "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".barnraise.tmp"
	if err := os.WriteFile(tmp, []byte(next), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// replaceBlock returns cur without its managed block, plus a new block with
// lines at the end when there are any.
func replaceBlock(cur string, lines []string) string {
	var kept []string
	in := false
	for line := range strings.SplitSeq(strings.TrimRight(cur, "\n"), "\n") {
		switch {
		case line == attrBegin:
			in = true
		case line == attrEnd:
			in = false
		case !in:
			kept = append(kept, line)
		}
	}
	for len(kept) > 0 && strings.TrimSpace(kept[len(kept)-1]) == "" {
		kept = kept[:len(kept)-1]
	}
	if len(lines) > 0 {
		if len(kept) > 0 {
			kept = append(kept, "")
		}
		kept = append(kept, attrBegin)
		kept = append(kept, lines...)
		kept = append(kept, attrEnd)
	}
	if len(kept) == 0 {
		return ""
	}
	return strings.Join(kept, "\n") + "\n"
}

// shellQuote quotes s for the shell git runs merge drivers with.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
