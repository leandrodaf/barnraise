package domain

import (
	"testing"
	"time"
)

func TestBuildNewer(t *testing.T) {
	day1 := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	day2 := day1.Add(24 * time.Hour)
	cases := []struct {
		name string
		a, b Build
		want bool
	}{
		{"a later release", Build{Version: "0.2.0"}, Build{Version: "0.1.9"}, true},
		{"numbers, not text", Build{Version: "0.1.10"}, Build{Version: "0.1.9"}, true},
		{"the release after its pre-release", Build{Version: "0.1.0"}, Build{Version: "0.1.0-dev"}, true},
		{"the pre-release before its release", Build{Version: "0.1.0-dev"}, Build{Version: "0.1.0"}, false},
		{"a v prefix", Build{Version: "v1.0.0"}, Build{Version: "0.9.9"}, true},
		{"the same release", Build{Version: "0.1.0"}, Build{Version: "0.1.0"}, false},
		// Commits without tags: the commit time orders them.
		{"a later commit", Build{Version: "9a64ec7", Time: day2}, Build{Version: "8cbf79a", Time: day1}, true},
		{"an earlier commit never replaces a dev build", Build{Version: "8cbf79a", Time: day1}, Build{Version: "9a64ec7-dirty", Time: day2}, false},
		{"the same commit, clean over dirty, is not newer", Build{Version: "9a64ec7", Time: day2}, Build{Version: "9a64ec7-dirty", Time: day2}, false},
		{"commits without times cannot be ordered", Build{Version: "8cbf79a"}, Build{Version: "9a64ec7-dirty"}, false},
		{"a release and a commit, by time", Build{Version: "0.2.0", Time: day2}, Build{Version: "8cbf79a", Time: day1}, true},
		// Builds after a release, as git describe names them.
		{"a build after its release", Build{Version: "0.1.0-1-gebe2f36"}, Build{Version: "0.1.0"}, true},
		{"the release is not newer than a build after it", Build{Version: "0.1.0", Time: day2}, Build{Version: "0.1.0-1-gebe2f36", Time: day1}, false},
		{"more commits after the same release", Build{Version: "0.1.0-12-g1234567"}, Build{Version: "0.1.0-3-gabcdef0"}, true},
		{"a build after a release is older than the next release", Build{Version: "0.1.0-30-g1234567"}, Build{Version: "0.2.0"}, false},
		{"dirty changes nothing in the order", Build{Version: "0.1.0-dirty", Time: day2}, Build{Version: "0.1.0", Time: day2}, false},
		{"a dirty build after a release", Build{Version: "0.1.0-2-g1234567-dirty"}, Build{Version: "0.1.0-1-gebe2f36"}, true},
	}
	for _, c := range cases {
		if got := c.a.Newer(c.b); got != c.want {
			t.Errorf("%s: %+v newer than %+v = %v", c.name, c.a, c.b, got)
		}
	}
}
