package egress

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
)

// SealedPrefix starts every placeholder the broker hands out.
const SealedPrefix = "barnraise-sealed-"

const sealedHexLen = 48

// Broker keeps the real API keys of key-based providers out of the agents'
// sessions. A session gets a random placeholder bound to one host; only
// the proxy, on a request to that host it terminated itself, puts the real
// key in its place. The placeholder is worthless anywhere else, and a
// request carrying it toward another host is refused.
type Broker struct {
	mu     sync.Mutex
	sealed map[string]sealedKey // placeholder -> key and host
	guards map[string]bool      // provider hosts the proxy terminates
	bundle []byte
}

type sealedKey struct{ host, key string }

// NewBroker returns a broker for the provider hosts (no port).
func NewBroker(hosts []string) *Broker {
	b := &Broker{sealed: map[string]sealedKey{}, guards: map[string]bool{}}
	for _, h := range hosts {
		b.guards[strings.ToLower(h)] = true
	}
	return b
}

// SetCABundle sets the PEM bundle sessions must trust (system roots and
// the proxy's CA).
func (b *Broker) SetCABundle(pem []byte) {
	b.mu.Lock()
	b.bundle = append([]byte(nil), pem...)
	b.mu.Unlock()
}

// CABundle implements ports.KeyBroker.
func (b *Broker) CABundle() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.bundle
}

// Guards reports whether host is one whose key the broker injects.
func (b *Broker) Guards(host string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.guards[strings.ToLower(host)]
}

// Hosts returns the guarded hosts.
func (b *Broker) Hosts() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for h := range b.guards {
		out = append(out, h)
	}
	return out
}

// Seal implements ports.KeyBroker: it returns a placeholder for key that
// the proxy only accepts toward host, and a function that forgets it. A
// host the broker does not guard returns ok false (the caller keeps the
// key as before).
func (b *Broker) Seal(host, key string) (string, func(), bool) {
	host = strings.ToLower(host)
	if !b.Guards(host) || key == "" {
		return "", func() {}, false
	}
	buf := make([]byte, sealedHexLen/2)
	_, _ = rand.Read(buf)
	ph := SealedPrefix + hex.EncodeToString(buf)
	b.mu.Lock()
	b.sealed[ph] = sealedKey{host: host, key: key}
	b.mu.Unlock()
	var once sync.Once
	return ph, func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.sealed, ph)
			b.mu.Unlock()
		})
	}, true
}

// placeholders returns the live placeholders found in data.
func (b *Broker) placeholders(data []byte) map[string]sealedKey {
	var out map[string]sealedKey
	for {
		i := bytes.Index(data, []byte(SealedPrefix))
		if i < 0 {
			return out
		}
		data = data[i:]
		end := len(SealedPrefix) + sealedHexLen
		if len(data) < end {
			return out
		}
		ph := string(data[:end])
		data = data[len(SealedPrefix):]
		b.mu.Lock()
		s, ok := b.sealed[ph]
		b.mu.Unlock()
		if ok {
			if out == nil {
				out = map[string]sealedKey{}
			}
			out[ph] = s
		}
	}
}

// Foreign reports whether any of the parts carries a live placeholder
// bound to a host other than host.
func (b *Broker) Foreign(host string, parts ...[]byte) bool {
	host = strings.ToLower(host)
	for _, p := range parts {
		for _, s := range b.placeholders(p) {
			if s.host != host {
				return true
			}
		}
	}
	return false
}

// Swap replaces, in the request headers, the placeholders bound to host
// with the real key. It returns how many were replaced.
func (b *Broker) Swap(host string, h http.Header) int {
	host = strings.ToLower(host)
	n := 0
	for name, vs := range h {
		for i, v := range vs {
			if !strings.Contains(v, SealedPrefix) {
				continue
			}
			for ph, s := range b.placeholders([]byte(v)) {
				if s.host == host {
					v = strings.ReplaceAll(v, ph, s.key)
					n++
				}
			}
			h[name][i] = v
		}
	}
	return n
}

func headerBytes(h http.Header) []byte {
	var buf bytes.Buffer
	for k, vs := range h {
		for _, v := range vs {
			buf.WriteString(k)
			buf.WriteString(": ")
			buf.WriteString(v)
			buf.WriteByte('\n')
		}
	}
	return buf.Bytes()
}
