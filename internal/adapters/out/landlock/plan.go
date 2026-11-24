package landlock

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// Ruleset is a Confinement turned into what Landlock grants: folders and
// files, read-write or read-only, and the TCP ports outbound connections
// may use. Landlock only allows; it has no "deny under an allowed folder".
// So a denied path under a granted folder is carved out: the folder is
// replaced by its entries, minus the denied one, down to the denied path.
type Ruleset struct {
	RWDirs  []string `json:"rw_dirs,omitempty"`
	RWFiles []string `json:"rw_files,omitempty"`
	RODirs  []string `json:"ro_dirs,omitempty"`
	ROFiles []string `json:"ro_files,omitempty"`
	// RestrictNet says TCP connect is limited to Connect.
	RestrictNet bool  `json:"restrict_net,omitempty"`
	Connect     []int `json:"connect,omitempty"`
}

// fsys is what planning reads of the filesystem (replaceable in tests).
type fsys interface {
	Stat(path string) (os.FileInfo, error)
	ReadDir(path string) ([]os.DirEntry, error)
}

type osFS struct{}

func (osFS) Stat(p string) (os.FileInfo, error)      { return os.Stat(p) }
func (osFS) ReadDir(p string) ([]os.DirEntry, error) { return os.ReadDir(p) }

// maxCarve bounds the entries a carve-out may add, so a deny rule under a
// huge folder cannot turn into a ruleset of thousands of paths.
const maxCarve = 4096

// Plan computes the ruleset of c. Missing paths are skipped. Work paths
// are always granted read-write, even inside a denied folder (the item's
// worktree lives under Barnraise's data folder, which is denied); store and
// read-only paths inside a denied folder are dropped, and those that hold a
// denied path are carved around it.
func Plan(c domain.Confinement, ephemeral func() (int, int)) Ruleset {
	return plan(c, osFS{}, ephemeral)
}

func plan(c domain.Confinement, f fsys, ephemeral func() (int, int)) Ruleset {
	var rs Ruleset
	deny := cleanAll(c.DenyRead)
	seen := map[string]bool{}
	add := func(p string, rw bool) {
		if seen[p] {
			return
		}
		st, err := f.Stat(p)
		if err != nil {
			return
		}
		seen[p] = true
		switch {
		case st.IsDir() && rw:
			rs.RWDirs = append(rs.RWDirs, p)
		case st.IsDir():
			rs.RODirs = append(rs.RODirs, p)
		case rw:
			rs.RWFiles = append(rs.RWFiles, p)
		default:
			rs.ROFiles = append(rs.ROFiles, p)
		}
	}
	for _, w := range cleanAll(c.Work) {
		add(w, true)
	}
	budget := maxCarve
	grant := func(p string, rw bool) {
		for _, d := range deny {
			if within(p, d) {
				return // the whole path is denied
			}
		}
		var carve func(dir string)
		carve = func(dir string) {
			var inside []string
			for _, d := range deny {
				if within(d, dir) && d != dir {
					inside = append(inside, d)
				}
			}
			if len(inside) == 0 {
				add(dir, rw)
				return
			}
			ents, err := f.ReadDir(dir)
			if err != nil {
				return // cannot list it: grant nothing rather than the denied path
			}
			for _, e := range ents {
				child := filepath.Join(dir, e.Name())
				denied := false
				holds := false
				for _, d := range inside {
					denied = denied || d == child
					holds = holds || within(d, child)
				}
				switch {
				case denied:
				case holds:
					carve(child)
				default:
					if budget <= 0 {
						return
					}
					budget--
					add(child, rw)
				}
			}
		}
		carve(p)
	}
	for _, p := range cleanAll(c.ReadWrite) {
		grant(p, true)
	}
	for _, p := range cleanAll(c.ReadOnly) {
		grant(p, false)
	}
	if c.Network != domain.NetOpen && c.Network != "" {
		rs.RestrictNet = true
		ports := map[int]bool{}
		for _, p := range c.Connect {
			if p > 0 && p < 65536 {
				ports[p] = true
			}
		}
		if c.Ephemeral && ephemeral != nil {
			lo, hi := ephemeral()
			for p := lo; p <= hi && p > 0 && p < 65536; p++ {
				ports[p] = true
			}
		}
		for p := range ports {
			rs.Connect = append(rs.Connect, p)
		}
		sort.Ints(rs.Connect)
	}
	return rs
}

func cleanAll(ps []string) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		if p == "" || !filepath.IsAbs(p) {
			continue
		}
		out = append(out, filepath.Clean(p))
	}
	return out
}

// within reports whether p is base or under it.
func within(p, base string) bool {
	if p == base {
		return true
	}
	if base == "/" {
		return strings.HasPrefix(p, "/")
	}
	return strings.HasPrefix(p, base+string(filepath.Separator))
}

// EphemeralRange reads the machine's ephemeral port range (Linux), falling
// back to the IANA range.
func EphemeralRange() (int, int) {
	data, err := os.ReadFile("/proc/sys/net/ipv4/ip_local_port_range")
	if err == nil {
		if lo, hi, ok := parseRange(string(data)); ok {
			return lo, hi
		}
	}
	return 49152, 65535
}

func parseRange(s string) (int, int, bool) {
	f := strings.Fields(s)
	if len(f) != 2 {
		return 0, 0, false
	}
	lo, err1 := strconv.Atoi(f[0])
	hi, err2 := strconv.Atoi(f[1])
	if err1 != nil || err2 != nil || lo < 1 || hi > 65535 || lo > hi {
		return 0, 0, false
	}
	return lo, hi, true
}
