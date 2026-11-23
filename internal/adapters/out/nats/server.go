// Package nats implements the messaging adapters on top of an embedded
// NATS server with JetStream: the event bus, the replication between
// machines, request/reply helpers and the content-addressed blob store.
//
// Every machine runs its own server and writes only to its own stream.
// Machines do not form a NATS cluster: each one connects as a plain client
// to the servers of the others and pulls their events. There is no quorum,
// so two machines are enough and any of them can stop at any time.
package nats

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"
)

// UserPerm is a machine allowed to connect, identified by its nkey public key.
// Empty allow lists mean no restriction in that direction.
type UserPerm struct {
	PublicKey string
	Publish   []string
	Subscribe []string
}

// ServerOptions configures the embedded server.
type ServerOptions struct {
	// Name is the machine name; it is used as the server name and JetStream domain.
	Name string
	// StoreDir is where JetStream keeps its files.
	StoreDir string
	// Host to listen on (default 127.0.0.1).
	Host string
	// Port to listen on; 0 means the NATS default (4222), -1 a random port.
	Port int
	// Users enables nkey authentication when not empty.
	Users []UserPerm
	// MaxMemory and MaxStore bound JetStream resources (0 = server defaults).
	MaxMemory int64
	MaxStore  int64
	// Debug turns on the server log on stderr.
	Debug bool
}

// Server is a running embedded NATS server.
type Server struct {
	srv      *server.Server
	opts     ServerOptions
	localKey nkeys.KeyPair // full-permission identity for in-process connections, when auth is on
}

// StartServer starts an embedded NATS server with JetStream.
func StartServer(opts ServerOptions) (*Server, error) {
	if opts.Name == "" {
		return nil, errors.New("nats: server name (machine name) is required")
	}
	if opts.StoreDir == "" {
		return nil, errors.New("nats: store dir is required")
	}
	if opts.Host == "" {
		opts.Host = "127.0.0.1"
	}
	name := Token(opts.Name)
	so := &server.Options{
		ServerName:         name,
		Host:               opts.Host,
		Port:               opts.Port,
		JetStream:          true,
		JetStreamDomain:    name,
		StoreDir:           opts.StoreDir,
		NoSigs:             true,
		NoLog:              !opts.Debug,
		JetStreamMaxMemory: opts.MaxMemory,
		JetStreamMaxStore:  opts.MaxStore,
	}
	s := &Server{opts: opts}
	if len(opts.Users) > 0 {
		kp, err := nkeys.CreateUser()
		if err != nil {
			return nil, fmt.Errorf("nats: local key: %w", err)
		}
		s.localKey = kp
		pub, _ := kp.PublicKey()
		so.Nkeys = append(so.Nkeys, &server.NkeyUser{Nkey: pub})
		for _, u := range opts.Users {
			if !nkeys.IsValidPublicUserKey(u.PublicKey) {
				return nil, fmt.Errorf("nats: invalid user public key %q", u.PublicKey)
			}
			nu := &server.NkeyUser{Nkey: u.PublicKey}
			if len(u.Publish) > 0 || len(u.Subscribe) > 0 {
				p := &server.Permissions{}
				if len(u.Publish) > 0 {
					p.Publish = &server.SubjectPermission{Allow: u.Publish}
				}
				if len(u.Subscribe) > 0 {
					p.Subscribe = &server.SubjectPermission{Allow: u.Subscribe}
				}
				// Replies to requests the user receives are always allowed.
				p.Response = &server.ResponsePermission{MaxMsgs: 1, Expires: time.Minute}
				nu.Permissions = p
			}
			so.Nkeys = append(so.Nkeys, nu)
		}
	}
	srv, err := server.NewServer(so)
	if err != nil {
		return nil, fmt.Errorf("nats: new server: %w", err)
	}
	if opts.Debug {
		srv.ConfigureLogger()
	}
	go srv.Start()
	if !srv.ReadyForConnections(10 * time.Second) {
		srv.Shutdown()
		return nil, errors.New("nats: server not ready in time")
	}
	s.srv = srv
	return s, nil
}

// ClientURL is the URL other machines and local clients connect to.
func (s *Server) ClientURL() string { return s.srv.ClientURL() }

// Name is the server (machine) name.
func (s *Server) Name() string { return Token(s.opts.Name) }

// Connect opens an in-process connection with full permissions.
func (s *Server) Connect(opts ...nats.Option) (*nats.Conn, error) {
	all := []nats.Option{nats.InProcessServer(s.srv), nats.Name("barnraise-local-" + s.Name()), nats.MaxReconnects(-1)}
	if s.localKey != nil {
		pub, _ := s.localKey.PublicKey()
		all = append(all, nats.Nkey(pub, s.localKey.Sign))
	}
	return nats.Connect("", append(all, opts...)...)
}

// ServeListener hands every connection accepted on ln to the server, as if
// it had arrived on the server's own port, until ln is closed. It is how
// peers reach this server through a listener the server does not own, such
// as the embedded Tailscale device: authentication and permissions apply
// exactly as on the TCP port.
func (s *Server) ServeListener(ln net.Listener) error {
	for {
		c, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		ic, err := s.srv.InProcessConn()
		if err != nil {
			c.Close()
			continue
		}
		go splice(c, ic)
	}
}

// splice copies both ways and closes both connections when either side ends.
func splice(a, b net.Conn) {
	var once sync.Once
	closeBoth := func() { a.Close(); b.Close() }
	go func() { _, _ = io.Copy(a, b); once.Do(closeBoth) }()
	_, _ = io.Copy(b, a)
	once.Do(closeBoth)
}

// DialFunc dials a connection, like net.Dialer.DialContext.
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

type customDialer struct{ dial DialFunc }

func (d customDialer) Dial(network, addr string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return d.dial(ctx, network, addr)
}

// DialerOption makes a client connect through dial instead of the system
// dialer: peers on the embedded Tailscale are reachable only through it.
func DialerOption(dial DialFunc) nats.Option {
	return nats.SetCustomDialer(customDialer{dial: dial})
}

// Shutdown stops the server and waits for it to finish.
func (s *Server) Shutdown() {
	if s.srv == nil {
		return
	}
	s.srv.Shutdown()
	s.srv.WaitForShutdown()
}

// NewMachineKey creates an nkey user identity for a machine. The seed is a
// secret (keep it in the keychain); the public key goes into the peers' Users.
func NewMachineKey() (seed, publicKey string, err error) {
	kp, err := nkeys.CreateUser()
	if err != nil {
		return "", "", err
	}
	sd, err := kp.Seed()
	if err != nil {
		return "", "", err
	}
	pub, err := kp.PublicKey()
	if err != nil {
		return "", "", err
	}
	return string(sd), pub, nil
}

// NkeyOption authenticates a client connection with a machine seed.
func NkeyOption(seed string) (nats.Option, error) {
	kp, err := nkeys.FromSeed([]byte(seed))
	if err != nil {
		return nil, fmt.Errorf("nats: bad seed: %w", err)
	}
	pub, err := kp.PublicKey()
	if err != nil {
		return nil, err
	}
	return nats.Nkey(pub, kp.Sign), nil
}

// PeerPermissions is the recommended permission set for another machine on
// this server: it may read the event stream and the blob bucket, call the
// RPC subjects addressed to this machine, and nothing else. In particular a
// peer cannot publish events into this machine's stream.
func PeerPermissions(publicKey, localMachine string) UserPerm {
	var pub []string
	for _, st := range []string{StreamName, LiveStreamName, blobStream} {
		pub = append(pub,
			"$JS.API.STREAM.INFO."+st,
			"$JS.API.CONSUMER.CREATE."+st,
			"$JS.API.CONSUMER.CREATE."+st+".>",
			"$JS.API.CONSUMER.INFO."+st+".>",
			"$JS.API.CONSUMER.DELETE."+st+".>",
			"$JS.API.CONSUMER.MSG.NEXT."+st+".>",
			"$JS.API.DIRECT.GET."+st,
			"$JS.API.DIRECT.GET."+st+".>",
			"$JS.API.STREAM.MSG.GET."+st,
			"$JS.ACK."+st+".>",
		)
	}
	pub = append(pub, "$JS.API.INFO", "$JS.FC.>", RPCSubject(localMachine, ">"))
	return UserPerm{PublicKey: publicKey, Publish: pub, Subscribe: []string{"_INBOX.>"}}
}

// Token makes a string safe as one subject token: no spaces, dots or wildcards.
func Token(s string) string {
	if s == "" {
		return "_"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '.' || r == '*' || r == '>' || r == ' ' || r == '\t' || r == '\r' || r == '\n' || r == '$' || r < 0x20 || r == 0x7f:
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
