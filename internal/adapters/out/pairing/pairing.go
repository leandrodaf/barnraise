// Package pairing joins a new machine to the cluster with a 6-digit code.
//
// The inviting machine shows a code ("482 913") and listens on TCP. The
// joining machine dials it and both run CPace, a password-authenticated key
// exchange, with the code as the password. Each side derives its keys with
// HKDF and the payloads (cluster information one way, machine information
// the other) travel sealed with ChaCha20-Poly1305. A wrong code produces
// different keys, so the first sealed message fails to open: the attempt
// fails cleanly without revealing anything about the code. An eavesdropper
// learns nothing and an active attacker gets one guess per attempt; the
// inviter allows only a few failed attempts and the code expires.
package pairing

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"strings"
	"syscall"
	"time"

	"filippo.io/cpace"
	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
)

const (
	// Context binds the exchange to this protocol and version.
	Context = "barnraise-pairing-v1"
	// MaxFailedAttempts before the inviter closes.
	MaxFailedAttempts = 3
	// DefaultTimeout is how long a code stays valid.
	DefaultTimeout = 10 * time.Minute
	// DefaultBeaconPort is the UDP port of LAN discovery beacons.
	DefaultBeaconPort = 47913
	// MaxPayload bounds a frame.
	MaxPayload = 1 << 20

	idJoiner  = "barnraise-join"
	idInviter = "barnraise-invite"
	version   = 1

	frameHello   = 0x01
	frameReply   = 0x02
	frameJoiner  = 0x03
	frameOK      = 0x04
	frameFail    = 0x05
	failWrong    = 0x01
	failLocked   = 0x02
	stepDeadline = 30 * time.Second
)

// Errors.
var (
	ErrWrongCode         = errors.New("pairing: wrong code")
	ErrTooManyAttempts   = errors.New("pairing: too many failed attempts; invite closed")
	ErrExpired           = errors.New("pairing: code expired")
	ErrBadCode           = errors.New("pairing: the code must have 6 digits")
	ErrNoInviteFound     = errors.New("pairing: no invite found on the local network")
	ErrProtocol          = errors.New("pairing: protocol error")
	errClosedBeforeReply = errors.New("pairing: invite closed")
)

// NewCode returns 6 random digits.
func NewCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

// FormatCode shows a code as "482 913".
func FormatCode(code string) string {
	c, err := NormalizeCode(code)
	if err != nil {
		return code
	}
	return c[:3] + " " + c[3:]
}

// NormalizeCode accepts "482913", "482 913" or "482-913".
func NormalizeCode(code string) (string, error) {
	var b strings.Builder
	for _, r := range code {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '\t':
		default:
			return "", ErrBadCode
		}
	}
	if b.Len() != 6 {
		return "", ErrBadCode
	}
	return b.String(), nil
}

// InviteOptions configures an invite.
type InviteOptions struct {
	// ListenAddr is the TCP address to listen on (default ":0").
	ListenAddr string
	// Listeners are more listeners served by the same single-use invite,
	// such as one on the embedded Tailscale device. Invite closes them.
	Listeners []net.Listener
	// Code is the pairing code; a random one is generated when empty.
	Code string
	// Payload is sent to the joining machine (cluster information).
	Payload []byte
	// Timeout is how long the code is valid (default 10 min).
	Timeout time.Duration
	// OnJoined receives the information sent by the joining machine.
	OnJoined func(peerInfo []byte)
	// OnFailed is told about each failed attempt.
	OnFailed func(err error)
	// BeaconPort enables LAN discovery beacons on this UDP port (0 = off).
	BeaconPort int
	// BeaconAddr is where beacons go (default 255.255.255.255).
	BeaconAddr string
	// BeaconEvery is the beacon interval (default 1s).
	BeaconEvery time.Duration
}

// Invite starts listening for one joining machine. It returns the code, the
// address actually listened on and a wait function that blocks until a
// machine joined (returning its information), the code expired, too many
// attempts failed or ctx ended. The invite is single use.
func Invite(ctx context.Context, opts InviteOptions) (code, addr string, wait func() ([]byte, error), err error) {
	if opts.Code == "" {
		if opts.Code, err = NewCode(); err != nil {
			return "", "", nil, err
		}
	}
	if code, err = NormalizeCode(opts.Code); err != nil {
		return "", "", nil, err
	}
	if len(opts.Payload) > MaxPayload-64 {
		return "", "", nil, errors.New("pairing: payload too large")
	}
	if opts.ListenAddr == "" {
		opts.ListenAddr = ":0"
	}
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultTimeout
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", opts.ListenAddr)
	if err != nil {
		for _, l := range opts.Listeners {
			l.Close()
		}
		return "", "", nil, err
	}
	addr = ln.Addr().String()
	lns := append([]net.Listener{ln}, opts.Listeners...)
	inv := &inviter{code: code, opts: opts, lns: lns, expires: time.Now().Add(opts.Timeout), done: make(chan struct{})}
	ictx, cancel := context.WithDeadline(ctx, inv.expires)
	context.AfterFunc(ictx, inv.closeListeners)
	if opts.BeaconPort > 0 {
		go beacon(ictx, opts, addr)
	}
	go func() {
		defer cancel()
		inv.serve(ictx)
	}()
	wait = func() ([]byte, error) {
		<-inv.done
		return inv.peerInfo, inv.err
	}
	return code, addr, wait, nil
}

type inviter struct {
	code    string
	opts    InviteOptions
	lns     []net.Listener
	expires time.Time

	failed   int
	done     chan struct{}
	peerInfo []byte
	err      error
}

func (inv *inviter) closeListeners() {
	for _, l := range inv.lns {
		l.Close()
	}
}

func (inv *inviter) finish(info []byte, err error) {
	inv.peerInfo, inv.err = info, err
	inv.closeListeners()
	close(inv.done)
}

type accepted struct {
	conn net.Conn
	err  error
}

// accept merges the listeners into one channel; the first listener error
// ends the invite.
func (inv *inviter) accept(ctx context.Context) <-chan accepted {
	ch := make(chan accepted)
	for _, l := range inv.lns {
		go func() {
			for {
				c, err := l.Accept()
				select {
				case ch <- accepted{c, err}:
				case <-ctx.Done():
					if c != nil {
						c.Close()
					}
					return
				}
				if err != nil {
					return
				}
			}
		}()
	}
	return ch
}

// serve handles one connection at a time, across all listeners, so
// concurrent attackers cannot make more than MaxFailedAttempts guesses in
// total.
func (inv *inviter) serve(ctx context.Context) {
	actx, stop := context.WithCancel(context.Background())
	defer stop()
	conns := inv.accept(actx)
	for {
		a := <-conns
		conn, err := a.conn, a.err
		if err != nil {
			switch {
			case time.Now().After(inv.expires) || errors.Is(ctx.Err(), context.DeadlineExceeded):
				inv.finish(nil, ErrExpired)
			case ctx.Err() != nil:
				inv.finish(nil, ctx.Err())
			default:
				inv.finish(nil, err)
			}
			return
		}
		info, attempted, err := inv.handle(ctx, conn)
		conn.Close()
		if err == nil {
			if inv.opts.OnJoined != nil {
				inv.opts.OnJoined(info)
			}
			inv.finish(info, nil)
			return
		}
		if !attempted {
			continue
		}
		inv.failed++
		if inv.opts.OnFailed != nil {
			inv.opts.OnFailed(err)
		}
		if inv.failed >= MaxFailedAttempts {
			inv.finish(nil, ErrTooManyAttempts)
			return
		}
	}
}

// handle runs the exchange; attempted is true once a guess was consumed.
func (inv *inviter) handle(ctx context.Context, conn net.Conn) (info []byte, attempted bool, err error) {
	deadline := time.Now().Add(stepDeadline)
	if inv.expires.Before(deadline) {
		deadline = inv.expires
	}
	_ = conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()

	typ, hello, err := readFrame(conn)
	if err != nil || typ != frameHello || len(hello) < 2 || hello[0] != version {
		return nil, false, ErrProtocol
	}
	msgA := hello[1:]
	msgB, key, err := cpace.Exchange(inv.code, cpace.NewContextInfo(idJoiner, idInviter, []byte(Context)), msgA)
	if err != nil {
		return nil, false, ErrProtocol
	}
	if err := writeFrame(conn, frameReply, msgB); err != nil {
		return nil, true, err
	}
	toInviter, toJoiner, ad := deriveKeys(key, msgA, msgB)
	typ, sealed, err := readFrame(conn)
	if err != nil || typ != frameJoiner {
		return nil, true, ErrProtocol
	}
	info, err = open(toInviter, ad, sealed)
	if err != nil {
		reason := byte(failWrong)
		if inv.failed+1 >= MaxFailedAttempts {
			reason = failLocked
		}
		_ = writeFrame(conn, frameFail, []byte{reason})
		return nil, true, ErrWrongCode
	}
	out, err := seal(toJoiner, ad, inv.opts.Payload)
	if err != nil {
		return nil, true, err
	}
	if err := writeFrame(conn, frameOK, out); err != nil {
		return nil, true, err
	}
	return info, true, nil
}

// Join dials the inviter at addr (a host:port, typically a Tailscale name
// or IP) with the code and exchanges information. With addr "" it looks
// for invite beacons on the local network for a few seconds.
func Join(ctx context.Context, addr, code string, myInfo []byte) ([]byte, error) {
	return JoinWith(ctx, JoinOptions{Addr: addr, Code: code, Info: myInfo})
}

// JoinOptions configures Join.
type JoinOptions struct {
	Addr string
	Code string
	Info []byte
	// Candidates are tried in order when Addr is empty, before the LAN
	// discovery: the online Barnraise machines of the tailnet.
	Candidates []string
	// Dial connects to Addr and Candidates (default the system dialer);
	// the embedded Tailscale device passes its own.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// BeaconPort for discovery when Addr is empty (default DefaultBeaconPort).
	BeaconPort int
	// DiscoverFor bounds the discovery (default 5s).
	DiscoverFor time.Duration
}

// JoinWith is Join with options.
func JoinWith(ctx context.Context, o JoinOptions) ([]byte, error) {
	code, err := NormalizeCode(o.Code)
	if err != nil {
		return nil, err
	}
	if len(o.Info) > MaxPayload-64 {
		return nil, errors.New("pairing: info too large")
	}
	if o.Addr != "" {
		return join(ctx, o.Dial, o.Addr, code, o.Info)
	}
	var last error
	for _, a := range o.Candidates {
		out, err := join(ctx, o.Dial, a, code, o.Info)
		if err == nil || !(errors.Is(err, errDial) || errors.Is(err, errClosedBeforeReply)) {
			// A machine with an invite answered: right or wrong, that is the answer.
			return out, err
		}
		// Nothing there, or a daemon that relays the pairing port while no
		// invite runs behind it: try the next machine.
		last = err
	}
	if o.BeaconPort == 0 {
		o.BeaconPort = DefaultBeaconPort
	}
	if o.DiscoverFor <= 0 {
		o.DiscoverFor = 5 * time.Second
	}
	addrs, err := Discover(ctx, o.BeaconPort, o.DiscoverFor)
	if err != nil {
		if last != nil && errors.Is(err, ErrNoInviteFound) {
			return nil, fmt.Errorf("%w; no machine of the tailnet answered either (%v)", err, last)
		}
		return nil, err
	}
	for _, a := range addrs {
		out, err := join(ctx, nil, a, code, o.Info)
		if err == nil {
			return out, nil
		}
		last = err
	}
	return nil, last
}

// errDial marks a join that could not reach anything at the address.
var errDial = errors.New("pairing: dial")

func join(ctx context.Context, dial func(context.Context, string, string) (net.Conn, error), addr, code string, info []byte) ([]byte, error) {
	if dial == nil {
		var d net.Dialer
		dial = d.DialContext
	}
	dctx, cancel := context.WithTimeout(ctx, stepDeadline)
	defer cancel()
	conn, err := dial(dctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("%w %s: %w", errDial, addr, err)
	}
	defer conn.Close()
	deadline := time.Now().Add(stepDeadline)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()

	msgA, st, err := cpace.Start(code, cpace.NewContextInfo(idJoiner, idInviter, []byte(Context)))
	if err != nil {
		return nil, err
	}
	if err := writeFrame(conn, frameHello, append([]byte{version}, msgA...)); err != nil {
		return nil, fmt.Errorf("%w: %w", errClosedBeforeReply, err)
	}
	typ, msgB, err := readFrame(conn)
	if err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) {
			return nil, errClosedBeforeReply
		}
		return nil, err
	}
	if typ != frameReply {
		return nil, ErrProtocol
	}
	key, err := st.Finish(msgB)
	if err != nil {
		return nil, ErrProtocol
	}
	toInviter, toJoiner, ad := deriveKeys(key, msgA, msgB)
	sealed, err := seal(toInviter, ad, info)
	if err != nil {
		return nil, err
	}
	if err := writeFrame(conn, frameJoiner, sealed); err != nil {
		return nil, err
	}
	typ, body, err := readFrame(conn)
	if err != nil {
		return nil, closedErr(err)
	}
	switch typ {
	case frameFail:
		if len(body) == 1 && body[0] == failLocked {
			return nil, fmt.Errorf("%w (the invite is now closed after too many attempts)", ErrWrongCode)
		}
		return nil, ErrWrongCode
	case frameOK:
		out, err := open(toJoiner, ad, body)
		if err != nil {
			return nil, ErrWrongCode
		}
		return out, nil
	default:
		return nil, ErrProtocol
	}
}

func closedErr(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return errClosedBeforeReply
	}
	return err
}

// deriveKeys splits the PAKE key into one key per direction, bound to the transcript.
func deriveKeys(key, msgA, msgB []byte) (toInviter, toJoiner, ad []byte) {
	h := sha256.New()
	h.Write([]byte(Context))
	h.Write(msgA)
	h.Write(msgB)
	ad = h.Sum(nil)
	expand := func(label string) []byte {
		k := make([]byte, chacha20poly1305.KeySize)
		r := hkdf.New(sha256.New, key, ad, []byte(Context+" "+label))
		if _, err := io.ReadFull(r, k); err != nil {
			panic(err) // cannot happen for 32 bytes
		}
		return k
	}
	return expand("joiner to inviter"), expand("inviter to joiner"), ad
}

// Each key seals exactly one message, so a fixed nonce is safe.
func seal(key, ad, plain []byte) ([]byte, error) {
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	return aead.Seal(nil, nonce, plain, ad), nil
}

func open(key, ad, sealed []byte) ([]byte, error) {
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	return aead.Open(nil, nonce, sealed, ad)
}

// Frames: 4-byte big-endian length, then a type byte and the body.
func writeFrame(w io.Writer, typ byte, body []byte) error {
	if len(body)+1 > MaxPayload {
		return errors.New("pairing: frame too large")
	}
	buf := make([]byte, 5+len(body))
	binary.BigEndian.PutUint32(buf, uint32(len(body)+1))
	buf[4] = typ
	copy(buf[5:], body)
	_, err := w.Write(buf)
	return err
}

func readFrame(r io.Reader) (byte, []byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n == 0 || n > MaxPayload {
		return 0, nil, ErrProtocol
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return 0, nil, err
	}
	return buf[0], buf[1:], nil
}

// Beacons say only "barnraise-invite <host:port>": nothing about the code.
const beaconPrefix = "barnraise-invite "

func beacon(ctx context.Context, opts InviteOptions, listenAddr string) {
	dst := opts.BeaconAddr
	if dst == "" {
		dst = "255.255.255.255"
	}
	every := opts.BeaconEvery
	if every <= 0 {
		every = time.Second
	}
	conn, err := net.Dial("udp", net.JoinHostPort(dst, fmt.Sprint(opts.BeaconPort)))
	if err != nil {
		return
	}
	defer conn.Close()
	msg := []byte(beaconPrefix + listenAddr)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		_, _ = conn.Write(msg)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Discover listens for invite beacons on a UDP port for up to within and
// returns the advertised addresses. It returns shortly after the first one.
func Discover(ctx context.Context, port int, within time.Duration) ([]string, error) {
	pc, err := net.ListenPacket("udp", fmt.Sprintf(":%d", port))
	if err != nil {
		return nil, fmt.Errorf("pairing: listen for beacons: %w", err)
	}
	defer pc.Close()
	end := time.Now().Add(within)
	stop := context.AfterFunc(ctx, func() { _ = pc.SetReadDeadline(time.Now()) })
	defer stop()
	seen := map[string]bool{}
	var out []string
	buf := make([]byte, 512)
	for ctx.Err() == nil {
		_ = pc.SetReadDeadline(end)
		n, from, err := pc.ReadFrom(buf)
		if err != nil {
			break
		}
		a, ok := parseBeacon(string(buf[:n]), from)
		if !ok || seen[a] {
			continue
		}
		seen[a] = true
		out = append(out, a)
		if grace := time.Now().Add(300 * time.Millisecond); grace.Before(end) {
			end = grace
		}
	}
	if len(out) == 0 {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrNoInviteFound
	}
	return out, nil
}

// parseBeacon reads a beacon; an unspecified host is replaced by the sender's IP.
func parseBeacon(msg string, from net.Addr) (string, bool) {
	rest, ok := strings.CutPrefix(msg, beaconPrefix)
	if !ok {
		return "", false
	}
	host, port, err := net.SplitHostPort(strings.TrimSpace(rest))
	if err != nil || port == "" {
		return "", false
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		ua, ok := from.(*net.UDPAddr)
		if !ok {
			return "", false
		}
		host = ua.IP.String()
	}
	return net.JoinHostPort(host, port), true
}
