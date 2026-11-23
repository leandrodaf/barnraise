// Package keychain implements ports.Secrets with the system keychain
// (Secret Service on Linux, Keychain on macOS, Credential Manager on
// Windows) and an encrypted file store as fallback for headless machines.
//
// Threat model of the file fallback, stated plainly: secrets are sealed
// with XChaCha20-Poly1305 under a random 256-bit key kept in a separate
// file (key, mode 0600) next to the sealed store (secrets.enc, mode 0600)
// inside a 0700 directory. This keeps secrets out of casual reads (grep,
// cat of the data dir, log collectors, editors) and out of backups or
// syncs that copy the store without the key file, and it detects
// tampering. It does NOT protect against root, against anyone running as
// the same user, or against a backup that includes both files. Machines
// that need more should provide a real keychain.
package keychain

import (
	"errors"
	"sync"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/leandrodaf/barnraise/internal/ports"
)

// Backend is the system keychain API (go-keyring's shape).
type Backend interface {
	Set(service, user, password string) error
	Get(service, user string) (string, error)
	Delete(service, user string) error
}

type systemBackend struct{}

func (systemBackend) Set(s, u, p string) error        { return keyring.Set(s, u, p) }
func (systemBackend) Get(s, u string) (string, error) { return keyring.Get(s, u) }
func (systemBackend) Delete(s, u string) error        { return keyring.Delete(s, u) }

// Option configures New.
type Option func(*Store)

// ForceFile skips the system keychain and uses the encrypted file store.
func ForceFile() Option { return func(s *Store) { s.forceFile = true } }

// WithBackend replaces the system keychain (tests, other platforms).
func WithBackend(b Backend) Option { return func(s *Store) { s.backend = b } }

// WithProbeTimeout bounds how long the keychain availability probe may take
// (a locked Secret Service can block on a prompt). Default 3s.
func WithProbeTimeout(d time.Duration) Option { return func(s *Store) { s.probeTimeout = d } }

// Store implements ports.Secrets. It decides once, on first use, whether
// the system keychain answers; if not, every call goes to the file store.
type Store struct {
	service      string
	fallbackDir  string
	forceFile    bool
	backend      Backend
	probeTimeout time.Duration

	once   sync.Once
	active ports.Secrets
}

var _ ports.Secrets = (*Store)(nil)

// New returns the secrets store for a service name. fallbackDir holds the
// encrypted file store when there is no keychain (headless server, no D-Bus).
func New(service, fallbackDir string, opts ...Option) *Store {
	s := &Store{service: service, fallbackDir: fallbackDir, backend: systemBackend{}, probeTimeout: 3 * time.Second}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Backend reports which store is in use: "keychain" or "file".
func (s *Store) Backend() string {
	if _, ok := s.pick().(*FileStore); ok {
		return "file"
	}
	return "keychain"
}

func (s *Store) pick() ports.Secrets {
	s.once.Do(func() {
		if !s.forceFile && s.probe() {
			s.active = &systemStore{service: s.service, b: s.backend}
			return
		}
		s.active = NewFileStore(s.fallbackDir, s.service)
	})
	return s.active
}

func (s *Store) probe() bool {
	done := make(chan bool, 1)
	go func() {
		_, err := s.backend.Get(s.service, "barnraise-probe")
		done <- err == nil || errors.Is(err, keyring.ErrNotFound)
	}()
	select {
	case ok := <-done:
		return ok
	case <-time.After(s.probeTimeout):
		return false
	}
}

// Get returns a secret or ports.ErrSecretNotFound.
func (s *Store) Get(key string) (string, error) { return s.pick().Get(key) }

// Set stores a secret.
func (s *Store) Set(key, value string) error { return s.pick().Set(key, value) }

// Delete removes a secret; deleting a missing key is not an error.
func (s *Store) Delete(key string) error { return s.pick().Delete(key) }

type systemStore struct {
	service string
	b       Backend
}

func (k *systemStore) Get(key string) (string, error) {
	v, err := k.b.Get(k.service, key)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ports.ErrSecretNotFound
	}
	return v, err
}

func (k *systemStore) Set(key, value string) error { return k.b.Set(k.service, key, value) }

func (k *systemStore) Delete(key string) error {
	err := k.b.Delete(k.service, key)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}
