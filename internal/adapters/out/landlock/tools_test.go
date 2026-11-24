package landlock

import (
	"testing"

	"github.com/leandrodaf/barnraise/internal/domain"
)

func TestPlanGrantsInstalledToolsInsideTheDeniedDataFolder(t *testing.T) {
	f := fakeFS{"/d": true, "/d/tools": true, "/d/wt": true}
	rs := plan(domain.Confinement{Work: []string{"/d/wt"}, Tools: []string{"/d/tools", "/missing"}, DenyRead: []string{"/d"}}, f, nil)
	if !domain.Contains(rs.RODirs, "/d/tools") || domain.Contains(rs.RWDirs, "/d/tools") || len(rs.RODirs) != 1 {
		t.Fatalf("tools: %+v", rs)
	}
}
