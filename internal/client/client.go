// Package client talks to the local daemon over its unix socket (or a URL)
// with the same Connect API every interface uses.
package client

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/leandrodaf/barnraise/gen/barnraise/v1/barnraisev1connect"
	"github.com/leandrodaf/barnraise/internal/paths"
)

// Client wraps the generated client with the token and transport.
type Client struct {
	barnraisev1connect.BarnraiseServiceClient
	HTTP    *http.Client
	BaseURL string
	Token   string
}

// Options choose how to reach the daemon.
type Options struct {
	URL     string // http(s)://host:port; empty means the unix socket
	Socket  string
	Token   string
	Timeout time.Duration
}

// FromEnv builds options from BARNRAISE_URL, BARNRAISE_SOCKET and BARNRAISE_TOKEN,
// falling back to the owner token file.
func FromEnv() Options {
	o := Options{URL: os.Getenv("BARNRAISE_URL"), Socket: paths.Socket(), Token: os.Getenv("BARNRAISE_TOKEN")}
	if o.Token == "" {
		if b, err := os.ReadFile(paths.OwnerTokenFile()); err == nil {
			o.Token = strings.TrimSpace(string(b))
		}
	}
	return o
}

// New creates a client.
func New(o Options) *Client {
	if o.Timeout == 0 {
		o.Timeout = 2 * time.Minute
	}
	hc := &http.Client{Timeout: o.Timeout}
	base := o.URL
	if base == "" {
		sock := o.Socket
		if sock == "" {
			sock = paths.Socket()
		}
		hc.Transport = &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		}}
		base = "http://barnraise"
	}
	c := &Client{HTTP: hc, BaseURL: strings.TrimSuffix(base, "/"), Token: o.Token}
	c.BarnraiseServiceClient = barnraisev1connect.NewBarnraiseServiceClient(hc, c.BaseURL, connect.WithInterceptors(authInterceptor(o.Token)))
	return c
}

func authInterceptor(token string) connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if token != "" {
				req.Header().Set("Authorization", "Bearer "+token)
			}
			return next(ctx, req)
		}
	}
}

// Friendly turns transport errors into plain sentences.
func Friendly(err error) error {
	if err == nil {
		return nil
	}
	var ce *connect.Error
	if errors.As(err, &ce) {
		if ce.Code() == connect.CodeUnavailable && strings.Contains(ce.Message(), "connect") ||
			strings.Contains(err.Error(), "no such file") || strings.Contains(err.Error(), "connection refused") {
			return errors.New("the Barnraise daemon is not running: start it with `barnraise up` (or `barnraise daemon`)")
		}
		if ce.Code() == connect.CodeUnauthenticated {
			return errors.New("not authenticated: the owner token is missing (run `barnraise up`) or BARNRAISE_TOKEN is invalid")
		}
		return fmt.Errorf("%s", ce.Message())
	}
	if strings.Contains(err.Error(), "no such file") || strings.Contains(err.Error(), "connection refused") {
		return errors.New("the Barnraise daemon is not running: start it with `barnraise up` (or `barnraise daemon`)")
	}
	return err
}
