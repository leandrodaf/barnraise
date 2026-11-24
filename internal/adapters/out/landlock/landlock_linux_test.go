//go:build linux

package landlock

import (
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/leandrodaf/barnraise/internal/domain"
)

const helperVar = "BARNRAISE_LANDLOCK_HELPER"

// TestMain lets the test binary act as a confined child: Landlock cannot be
// undone, so the checks run in a process of their own.
func TestMain(m *testing.M) {
	if spec := os.Getenv(helperVar); spec != "" {
		os.Exit(helper(spec))
	}
	os.Exit(m.Run())
}

type probeResult struct {
	WorkWrite, OtherWrite, DeniedRead, ROWrite, AllowedConnect, DeniedConnect bool
	Err                                                                       string
}

func helper(spec string) int {
	var conf domain.Confinement
	var res probeResult
	if err := json.Unmarshal([]byte(spec), &conf); err != nil {
		res.Err = err.Error()
	} else if err := Restrict(conf); err != nil {
		res.Err = err.Error()
	} else {
		res.WorkWrite = os.WriteFile(filepath.Join(conf.Work[0], "ok"), []byte("x"), 0o600) == nil
		res.OtherWrite = os.WriteFile(filepath.Join(os.Getenv("OTHER"), "no"), []byte("x"), 0o600) == nil
		_, err := os.ReadFile(os.Getenv("SECRET"))
		res.DeniedRead = err == nil
		res.ROWrite = os.WriteFile(filepath.Join(conf.ReadOnly[0], "no"), []byte("x"), 0o600) == nil
		if c, err := net.Dial("tcp", os.Getenv("ALLOWED")); err == nil {
			res.AllowedConnect = true
			c.Close()
		}
		if c, err := net.Dial("tcp", os.Getenv("DENIED")); err == nil {
			res.DeniedConnect = true
			c.Close()
		}
	}
	_ = json.NewEncoder(os.Stdout).Encode(res)
	return 0
}

// The rules hold on the real kernel: the worktree is writable, the rest of
// the filesystem is not, a denied folder under a granted one stays closed,
// and TCP connect reaches only the allowed ports.
func TestRestrictOnThisKernel(t *testing.T) {
	abi, _ := abiVersion()
	if abi <= 0 {
		t.Skip("this kernel has no Landlock")
	}
	base := t.TempDir()
	work, other, ro, store := filepath.Join(base, "work"), filepath.Join(base, "other"), filepath.Join(base, "ro"), filepath.Join(base, "store")
	secretDir := filepath.Join(store, "secret")
	for _, d := range []string{work, other, ro, secretDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	secret := filepath.Join(secretDir, "key")
	if err := os.WriteFile(secret, []byte("s"), 0o600); err != nil {
		t.Fatal(err)
	}
	listen := func() (net.Listener, int) {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		go func() {
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				c.Close()
			}
		}()
		t.Cleanup(func() { l.Close() })
		return l, l.Addr().(*net.TCPAddr).Port
	}
	allowed, aport := listen()
	denied, _ := listen()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	conf := domain.Confinement{Profile: "check", Work: []string{work}, ReadWrite: []string{store}, ReadOnly: []string{ro, filepath.Dir(exe), "/proc"},
		DenyRead: []string{secretDir}, Network: domain.NetLocal, Connect: []int{aport}}
	spec, _ := json.Marshal(conf)
	cmd := exec.Command(exe, "-test.run=^$")
	cmd.Env = append(os.Environ(), helperVar+"="+string(spec), "OTHER="+other, "SECRET="+secret,
		"ALLOWED="+allowed.Addr().String(), "DENIED="+denied.Addr().String())
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("helper: %v %s", err, out)
	}
	var res probeResult
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if res.Err != "" {
		t.Fatal(res.Err)
	}
	if !res.WorkWrite || res.OtherWrite || res.DeniedRead || res.ROWrite {
		t.Fatalf("filesystem: %+v", res)
	}
	if abi >= MinNetABI {
		if !res.AllowedConnect || res.DeniedConnect {
			t.Fatalf("network (ABI %d, allowed port %s): %+v", abi, strconv.Itoa(aport), res)
		}
	}
}
