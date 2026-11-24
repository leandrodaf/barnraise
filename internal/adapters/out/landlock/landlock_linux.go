//go:build linux

package landlock

import (
	"fmt"
	"strings"
	"syscall"

	ll "github.com/landlock-lsm/go-landlock/landlock"
	llsys "github.com/landlock-lsm/go-landlock/landlock/syscall"
)

func abiVersion() (int, error) {
	v, err := llsys.LandlockGetABIVersion()
	if err != nil {
		// ENOSYS or EOPNOTSUPP: built without Landlock, or not enabled at boot.
		return 0, nil
	}
	return v, nil
}

// restrict applies rs. The filesystem rights are those of ABI v8 (without
// the v9 restriction on UNIX socket paths, which would cut agents off from
// the daemon's socket); the network rights are TCP connect only: binding a
// test server stays allowed, and UDP (DNS) is not touched. BestEffort keeps
// what the running kernel supports and drops the rest.
func restrict(rs Ruleset, abi int) error {
	cfg := ll.Config{HandledAccessFS: ll.V8.HandledAccessFS}
	if rs.RestrictNet && abi >= MinNetABI {
		cfg.HandledAccessNet = ll.AccessNetSet(llsys.AccessNetConnectTCP)
	}
	cfg = cfg.BestEffort()
	var rules []ll.Rule
	for _, d := range rs.RWDirs {
		r := ll.RWDirs(d).WithRefer().IgnoreIfMissing()
		if d == "/dev" || strings.HasPrefix(d, "/dev/") {
			r = r.WithIoctlDev()
		}
		rules = append(rules, r)
	}
	for _, f := range rs.RWFiles {
		r := ll.RWFiles(f).IgnoreIfMissing()
		if strings.HasPrefix(f, "/dev/") {
			r = r.WithIoctlDev()
		}
		rules = append(rules, r)
	}
	if len(rs.RODirs) > 0 {
		rules = append(rules, ll.RODirs(rs.RODirs...).IgnoreIfMissing())
	}
	if len(rs.ROFiles) > 0 {
		rules = append(rules, ll.ROFiles(rs.ROFiles...).IgnoreIfMissing())
	}
	if cfg.HandledAccessNet != 0 {
		for _, p := range rs.Connect {
			rules = append(rules, ll.ConnectTCP(uint16(p)))
		}
	}
	if err := cfg.Restrict(rules...); err != nil {
		return fmt.Errorf("landlock: %w", err)
	}
	return nil
}

func execve(prog string, argv, env []string) error {
	return syscall.Exec(prog, argv, env)
}
