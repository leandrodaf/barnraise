package toolinstall

import (
	"os"
	"path/filepath"
	"slices"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// Root is the tools folder of a data folder.
func Root(dataDir string) string { return filepath.Join(dataDir, "tools") }

// UsePATH puts <root>/bin first on this process's PATH, so every lookup
// of Barnraise and every program it starts (setup, checks, hooks, agent
// sessions, plugins) finds the tools it installed, and names root in
// BARNRAISE_TOOLS_DIR for the sandboxes that must give it back read-only.
func UsePATH(root string) {
	bin := BinDir(root)
	cur := os.Getenv("PATH")
	if !slices.Contains(filepath.SplitList(cur), bin) {
		if cur == "" {
			_ = os.Setenv("PATH", bin)
		} else {
			_ = os.Setenv("PATH", bin+string(os.PathListSeparator)+cur)
		}
	}
	_ = os.Setenv(domain.EnvToolsDir, root)
}
