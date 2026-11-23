package tailnet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/leandrodaf/barnraise/internal/adapters/out/plugin"
)

// Mode is how this machine reaches the tailnet (`network:` in daemon.yaml).
type Mode string

const (
	// ModeAuto picks the installed tailscale, then the embedded one when it
	// can log in without a person, then the local network.
	ModeAuto Mode = ""
	// ModeTailscale drives the installed `tailscale` command.
	ModeTailscale Mode = "tailscale"
	// ModeTsnet joins the tailnet from inside the process (tsnet build tag).
	ModeTsnet Mode = "tsnet"
	// ModeLAN uses no tailnet: the local network or a direct address.
	ModeLAN Mode = "lan"
	// ModePlugin asks a network plugin (network_plugin in daemon.yaml) for
	// the addresses of any other overlay network. Never chosen by auto.
	ModePlugin Mode = "plugin"
)

// Errors of the selection and of the embedded tailnet.
var (
	// ErrNotBuiltIn means the binary was built without the tsnet build tag.
	ErrNotBuiltIn = errors.New("tailnet: this binary has no embedded Tailscale (build with -tags tsnet, as the releases are)")
	// ErrBusy means another Barnraise process (usually the daemon) holds the
	// embedded tailnet's state directory.
	ErrBusy = errors.New("tailnet: the embedded Tailscale is in use by another barnraise process")
)

// AuthKeySecret is the keychain entry of the embedded Tailscale's auth key.
const AuthKeySecret = "tailscale/authkey"

// AuthKeyEnv are the environment variables read for the auth key, in order.
var AuthKeyEnv = []string{"BARNRAISE_TS_AUTHKEY", "TS_AUTHKEY", "TS_AUTH_KEY"}

// ParseMode validates the `network:` value of daemon.yaml.
func ParseMode(s string) (Mode, error) {
	switch m := Mode(strings.ToLower(strings.TrimSpace(s))); m {
	case ModeAuto, ModeTailscale, ModeTsnet, ModeLAN, ModePlugin:
		return m, nil
	case "auto":
		return ModeAuto, nil
	default:
		return "", fmt.Errorf("tailnet: unknown network %q (use tailscale, tsnet, plugin or lan)", s)
	}
}

// Config says how to reach the tailnet. Nothing in it is logged: the auth
// key is only read, through AuthKey, when the embedded tailnet logs in.
type Config struct {
	// Mode is the configured network; ModeAuto chooses.
	Mode Mode
	// Disabled turns every tailnet off (BARNRAISE_NO_TAILSCALE).
	Disabled bool
	// Hostname is the device name on the tailnet: the machine name.
	Hostname string
	// StateDir keeps the embedded tailnet's keys and state (created 0700).
	StateDir string
	// ControlURL is the coordination server; empty means Tailscale's own.
	// Point it at a Headscale server to self-host.
	ControlURL string
	// Tags are advertised by the embedded device (default tag:barnraise).
	Tags []string
	// AuthKey returns the auth key that logs a new device in, or "".
	AuthKey func() string
	// Ephemeral registers a device the control server forgets when it goes
	// away (used for the short pairing device while the daemon runs).
	Ephemeral bool
	// UpTimeout bounds the login (default 60s).
	UpTimeout time.Duration
	// Logf receives the embedded tailnet's messages meant for people (the
	// login URL); the auth key is removed from them. Nil discards them.
	Logf func(format string, args ...any)
	// LookPath finds the tailscale command; tests replace it.
	LookPath func(string) (string, error)
	// Bin is the tailscale command (default "tailscale").
	Bin string
	// Plugin runs the network plugin of `network: plugin` (network_plugin
	// in daemon.yaml); Plugin.Def.Command is empty when none is declared.
	Plugin plugin.Options
	// PluginErr says why network_plugin cannot be used (a bad definition).
	PluginErr error
}

func (c Config) tags() []string {
	if len(c.Tags) == 0 {
		return []string{Tag}
	}
	return c.Tags
}

func (c Config) authKey() string {
	if c.AuthKey == nil {
		return ""
	}
	return strings.TrimSpace(c.AuthKey())
}

func (c Config) bin() string {
	if c.Bin != "" {
		return c.Bin
	}
	return "tailscale"
}

// HasState reports whether the embedded tailnet logged in before in dir.
func HasState(dir string) bool {
	st, err := os.Stat(filepath.Join(dir, "tailscaled.state"))
	return err == nil && st.Size() > 0
}

// Resolve picks the mode. An explicit mode is kept (tsnet fails when the
// binary lacks it, plugin when no valid plugin is declared). Auto prefers
// the installed tailscale command, then the embedded tailnet when it can
// come up on its own (an auth key, or a state from an earlier login), and
// otherwise the local network; it never picks the plugin.
func (c Config) Resolve() (Mode, error) {
	if c.Disabled {
		return ModeLAN, nil
	}
	switch c.Mode {
	case ModeTailscale, ModeLAN:
		return c.Mode, nil
	case ModeTsnet:
		if !Embeddable {
			return "", ErrNotBuiltIn
		}
		return ModeTsnet, nil
	case ModePlugin:
		if c.PluginErr != nil {
			return "", fmt.Errorf("tailnet: %w", c.PluginErr)
		}
		if strings.TrimSpace(c.Plugin.Def.Command) == "" {
			return "", ErrNoPlugin
		}
		return ModePlugin, nil
	case ModeAuto:
	default:
		return "", fmt.Errorf("tailnet: unknown network %q", c.Mode)
	}
	look := c.LookPath
	if look == nil {
		look = exec.LookPath
	}
	if _, err := look(c.bin()); err == nil {
		return ModeTailscale, nil
	}
	if Embeddable && (c.authKey() != "" || (c.StateDir != "" && HasState(c.StateDir))) {
		return ModeTsnet, nil
	}
	return ModeLAN, nil
}

// Open joins the tailnet the way Resolve says. It returns nil and ModeLAN
// when no tailnet is used. A Network is returned even when the installed
// Tailscale is not running: callers check Available. A network plugin that
// cannot start or does not answer network.status is an error, with the
// mode: callers carry on with the local network.
func Open(ctx context.Context, c Config) (Tailnet, Mode, error) {
	m, err := c.Resolve()
	if err != nil {
		return nil, "", err
	}
	switch m {
	case ModeTailscale:
		return &Network{Bin: c.Bin}, m, nil
	case ModeTsnet:
		t, err := openEmbedded(ctx, c)
		if err != nil {
			return nil, m, err
		}
		return t, m, nil
	case ModePlugin:
		t, err := openPlugin(ctx, c)
		if err != nil {
			return nil, m, err
		}
		return t, m, nil
	default:
		return nil, ModeLAN, nil
	}
}

// LookupAuthKey reads the auth key from the environment (AuthKeyEnv), then
// from the keychain entry AuthKeySecret. get may be nil.
func LookupAuthKey(getenv func(string) string, get func(string) (string, error)) string {
	for _, k := range AuthKeyEnv {
		if v := strings.TrimSpace(getenv(k)); v != "" {
			return v
		}
	}
	if get != nil {
		if v, err := get(AuthKeySecret); err == nil {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// redactor removes a secret from log lines.
func redactor(secret string, logf func(string, ...any)) func(string, ...any) {
	return func(format string, args ...any) {
		if logf == nil {
			return
		}
		msg := fmt.Sprintf(format, args...)
		if secret != "" {
			msg = strings.ReplaceAll(msg, secret, "[auth key]")
		}
		logf("%s", msg)
	}
}

// selfFile is where the process that holds the embedded tailnet writes
// this machine's tailnet identity, for the commands that cannot open it.
const selfFile = "self.json"

// WriteSelf records this machine's tailnet identity in dir (0600).
func WriteSelf(dir string, s Self) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, selfFile+".tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, selfFile))
}

// ReadSelf reads what WriteSelf recorded.
func ReadSelf(dir string) (Self, error) {
	var s Self
	b, err := os.ReadFile(filepath.Join(dir, selfFile))
	if err != nil {
		return s, err
	}
	return s, json.Unmarshal(b, &s)
}

// Forward accepts connections on ln and relays each one to target (a local
// address), until ln is closed or ctx ends. The daemon uses it to carry
// pairing connections that arrive on the embedded tailnet to the
// `machine invite` command listening on loopback: the bytes stay end-to-end
// encrypted by the pairing protocol.
func Forward(ctx context.Context, ln net.Listener, target string) error {
	stop := context.AfterFunc(ctx, func() { ln.Close() })
	defer stop()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go func() {
			var d net.Dialer
			dctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			up, err := d.DialContext(dctx, "tcp", target)
			cancel()
			if err != nil {
				c.Close()
				return
			}
			Splice(c, up)
		}()
	}
}

// Splice copies both ways between a and b and closes both when either side ends.
func Splice(a, b net.Conn) {
	var once sync.Once
	closeBoth := func() { a.Close(); b.Close() }
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = io.Copy(a, b); once.Do(closeBoth) }()
	go func() { defer wg.Done(); _, _ = io.Copy(b, a); once.Do(closeBoth) }()
	wg.Wait()
}
