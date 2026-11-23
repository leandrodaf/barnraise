package provcommon

import (
	"regexp"
	"strconv"
)

var reVersion = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

// ParseVersion finds the first x.y.z in s.
func ParseVersion(s string) (v [3]int, text string, ok bool) {
	m := reVersion.FindStringSubmatch(s)
	if m == nil {
		return v, "", false
	}
	for i := range 3 {
		v[i], _ = strconv.Atoi(m[i+1])
	}
	return v, m[0], true
}

// AtLeast reports whether v >= min.
func AtLeast(v [3]int, major, minor, patch int) bool {
	want := [3]int{major, minor, patch}
	for i := range 3 {
		if v[i] != want[i] {
			return v[i] > want[i]
		}
	}
	return true
}
