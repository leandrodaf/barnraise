package domain

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Build is what a barnraise binary says of itself: its version (a release
// like 0.2.0; a build after it as `git describe` prints it, 0.2.0-3-g1a2b3c4;
// or a commit without tags; any of them maybe with -dirty) and the time of
// the commit it was built from.
type Build struct {
	Version string    `json:"version"`
	Time    time.Time `json:"time,omitzero"`
}

// Newer reports whether a is a later build than b. Two releases compare as
// versions: a pre-release sorts before its release, and a build made N
// commits after a release (0.2.0-3-g1a2b3c4) after it. Otherwise the commit
// times decide. When neither can order them (two commits without times),
// a is not newer: a machine never takes a binary it cannot place, and a
// development build (-dirty) is never replaced by a peer's older commit.
func (a Build) Newer(b Build) bool {
	if va, ok := release(a.Version); ok {
		if vb, ok := release(b.Version); ok {
			for i := range 3 {
				if va.core[i] != vb.core[i] {
					return va.core[i] > vb.core[i]
				}
			}
			if va.pre != vb.pre {
				return va.pre == "" // the release after its pre-release
			}
			if va.after != vb.after {
				return va.after > vb.after // more commits after the same release
			}
			return a.Time.After(b.Time)
		}
	}
	if !a.Time.IsZero() && !b.Time.IsZero() {
		return a.Time.After(b.Time)
	}
	return false
}

type releaseVersion struct {
	core  [3]int
	pre   string
	after int // commits after the release, as git describe counts them
}

// describeRe is what git describe adds after a tag: commits since it and
// the abbreviated commit.
var describeRe = regexp.MustCompile(`^(\d+)-g[0-9a-f]{4,}$`)

// release reads "v1.2.3", "1.2.3-dev" or "1.2.3-4-g1a2b3c4", each maybe with
// -dirty (which does not change the order); a bare commit hash is not one.
func release(v string) (releaseVersion, bool) {
	v = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(v), "v"), "-dirty")
	core, pre, _ := strings.Cut(v, "-")
	parts := strings.Split(core, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return releaseVersion{}, false
	}
	var r releaseVersion
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return releaseVersion{}, false
		}
		r.core[i] = n
	}
	if m := describeRe.FindStringSubmatch(pre); m != nil {
		r.after, _ = strconv.Atoi(m[1])
		pre = ""
	}
	r.pre = pre
	return r, true
}

// LimitedModel reports whether a limit message names the model itself (its
// own cap, "You've hit your Fable limit") rather than the account's window
// ("5-hour", "weekly", "usage"): then only that model waits, and the
// account still runs the others.
func LimitedModel(text, model string) bool {
	t, m := strings.ToLower(text), strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return false
	}
	if i := strings.IndexAny(m, "@-:"); i > 0 {
		m = m[:i]
	}
	return strings.Contains(t, m+" limit") || strings.Contains(t, m+" weekly limit") || strings.Contains(t, m+" usage limit")
}
