package domain

import (
	"strconv"
	"strings"
	"time"
)

// Build is what a barnraise binary says of itself: its version (a release
// like 0.2.0, or a commit as `git describe` prints it without tags, maybe
// with -dirty) and the time of the commit it was built from.
type Build struct {
	Version string    `json:"version"`
	Time    time.Time `json:"time,omitzero"`
}

// Newer reports whether a is a later build than b. Two releases compare as
// versions (a pre-release sorts before its release). Otherwise the commit
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
			return a.Time.After(b.Time)
		}
	}
	if !a.Time.IsZero() && !b.Time.IsZero() {
		return a.Time.After(b.Time)
	}
	return false
}

type releaseVersion struct {
	core [3]int
	pre  string
}

// release reads "v1.2.3" or "1.2.3-dev"; a bare commit hash is not one.
func release(v string) (releaseVersion, bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
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
	r.pre = pre
	return r, true
}
