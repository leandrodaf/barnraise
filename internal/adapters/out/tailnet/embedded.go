//go:build tsnet

package tailnet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"sync/atomic"
	"time"

	"tailscale.com/envknob"
	"tailscale.com/tsnet"
)

// Embeddable reports whether this binary carries the embedded Tailscale.
const Embeddable = true

// Embedded is this process joined to the tailnet as its own device, with
// tsnet. Its addresses exist only inside the process: peers reach the
// listeners it returns, and connections to peers go through Dial.
type Embedded struct {
	srv    *tsnet.Server
	dir    string
	unlock func()
	closed atomic.Bool
}

var _ Tailnet = (*Embedded)(nil)

func openEmbedded(ctx context.Context, c Config) (Tailnet, error) {
	e, err := OpenEmbedded(ctx, c)
	if err != nil {
		return nil, err
	}
	return e, nil
}

// OpenEmbedded brings the embedded device up: it logs in with the auth key
// the first time (or prints a login URL through Logf when there is none),
// and reuses the keys kept in StateDir afterwards.
func OpenEmbedded(ctx context.Context, c Config) (*Embedded, error) {
	if c.StateDir == "" {
		return nil, errors.New("tailnet: the embedded Tailscale needs a state directory")
	}
	if c.Hostname == "" {
		return nil, errors.New("tailnet: the embedded Tailscale needs a hostname (the machine name)")
	}
	if err := os.MkdirAll(c.StateDir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(c.StateDir, 0o700); err != nil {
		return nil, err
	}
	unlock, err := lockDir(c.StateDir)
	if err != nil {
		return nil, err
	}
	// Barnraise sends nothing to Tailscale's log service; with Headscale
	// nothing at all leaves for Tailscale Inc.
	envknob.SetNoLogsNoSupport()
	key := c.authKey()
	srv := &tsnet.Server{
		Dir:           c.StateDir,
		Hostname:      c.Hostname,
		ControlURL:    c.ControlURL,
		AuthKey:       key,
		AdvertiseTags: c.tags(),
		Ephemeral:     c.Ephemeral,
		UserLogf:      redactor(key, c.Logf),
		Logf:          func(string, ...any) {},
	}
	timeout := c.UpTimeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	uctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if _, err := srv.Up(uctx); err != nil {
		_ = srv.Close()
		unlock()
		if uctx.Err() != nil && key == "" && !HasState(c.StateDir) {
			return nil, fmt.Errorf("tailnet: the embedded Tailscale did not log in within %s: give it an auth key (`barnraise machine network-key` or BARNRAISE_TS_AUTHKEY): %w", timeout, err)
		}
		return nil, fmt.Errorf("tailnet: embedded Tailscale: %w", err)
	}
	e := &Embedded{srv: srv, dir: c.StateDir, unlock: unlock}
	if self, _, err := e.Status(ctx); err == nil {
		_ = WriteSelf(c.StateDir, self)
	}
	return e, nil
}

// Mode is ModeTsnet.
func (e *Embedded) Mode() Mode { return ModeTsnet }

// Available reports whether the device is up.
func (e *Embedded) Available() bool {
	if e.closed.Load() {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	self, _, err := e.Status(ctx)
	return err == nil && len(self.IPs) > 0
}

// Status reads the device's view of the tailnet, in the same shape as
// `tailscale status --json`.
func (e *Embedded) Status(ctx context.Context) (Self, []Peer, error) {
	if e.closed.Load() {
		return Self{}, nil, ErrUnavailable
	}
	lc, err := e.srv.LocalClient()
	if err != nil {
		return Self{}, nil, err
	}
	st, err := lc.Status(ctx)
	if err != nil {
		return Self{}, nil, err
	}
	b, err := json.Marshal(st)
	if err != nil {
		return Self{}, nil, err
	}
	return ParseStatus(b)
}

// Listen listens on the device's addresses. Only the port of addr counts:
// the device has no other interface.
func (e *Embedded) Listen(_ context.Context, addr string) (net.Listener, error) {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	return e.srv.Listen("tcp", ":"+port)
}

// Dial connects through the device: tailnet addresses and names go over
// the tailnet, anything else through the system.
func (e *Embedded) Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	return e.srv.Dial(ctx, network, addr)
}

// Close leaves the tailnet (an ephemeral device is forgotten by the control
// server) and frees the state directory.
func (e *Embedded) Close() error {
	if e.closed.Swap(true) {
		return nil
	}
	err := e.srv.Close()
	e.unlock()
	return err
}
