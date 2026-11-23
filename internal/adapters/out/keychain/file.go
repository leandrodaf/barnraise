package keychain

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/crypto/chacha20poly1305"

	"github.com/leandrodaf/barnraise/internal/ports"
)

const (
	keyFile   = "key"
	storeFile = "secrets.enc"
	magic     = "BRS1"
)

// FileStore is the encrypted file fallback. See the package comment for
// what it protects against and what it does not.
type FileStore struct {
	dir     string
	service string
	mu      sync.Mutex
}

var _ ports.Secrets = (*FileStore)(nil)

// NewFileStore returns a file store in dir. The service name is bound to
// the ciphertext as associated data.
func NewFileStore(dir, service string) *FileStore {
	return &FileStore{dir: dir, service: service}
}

// Get returns a secret or ports.ErrSecretNotFound.
func (f *FileStore) Get(key string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.load()
	if err != nil {
		return "", err
	}
	v, ok := m[key]
	if !ok {
		return "", ports.ErrSecretNotFound
	}
	return v, nil
}

// Set stores a secret.
func (f *FileStore) Set(key, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.load()
	if err != nil {
		return err
	}
	m[key] = value
	return f.save(m)
}

// Delete removes a secret; a missing key is not an error.
func (f *FileStore) Delete(key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.load()
	if err != nil {
		return err
	}
	if _, ok := m[key]; !ok {
		return nil
	}
	delete(m, key)
	return f.save(m)
}

func (f *FileStore) ensureDir() error {
	if f.dir == "" {
		return errors.New("keychain: no fallback directory for the file store")
	}
	if err := os.MkdirAll(f.dir, 0o700); err != nil {
		return err
	}
	return os.Chmod(f.dir, 0o700)
}

// key loads the key, creating it on first use.
func (f *FileStore) key(create bool) ([]byte, error) {
	p := filepath.Join(f.dir, keyFile)
	k, err := os.ReadFile(p)
	if err == nil {
		if len(k) != chacha20poly1305.KeySize {
			return nil, fmt.Errorf("keychain: key file %s is corrupt", p)
		}
		return k, nil
	}
	if !errors.Is(err, fs.ErrNotExist) || !create {
		return nil, err
	}
	k = make([]byte, chacha20poly1305.KeySize)
	if _, err := rand.Read(k); err != nil {
		return nil, err
	}
	// O_EXCL: never overwrite a key another process just created.
	fh, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return f.key(false)
	}
	if err != nil {
		return nil, err
	}
	if _, err := fh.Write(k); err != nil {
		fh.Close()
		return nil, err
	}
	return k, fh.Close()
}

func (f *FileStore) load() (map[string]string, error) {
	m := map[string]string{}
	if f.dir == "" {
		return nil, errors.New("keychain: no fallback directory for the file store")
	}
	blob, err := os.ReadFile(filepath.Join(f.dir, storeFile))
	if errors.Is(err, fs.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	k, err := f.key(false)
	if err != nil {
		return nil, fmt.Errorf("keychain: store exists but its key cannot be read: %w", err)
	}
	aead, err := chacha20poly1305.NewX(k)
	if err != nil {
		return nil, err
	}
	if len(blob) < len(magic)+aead.NonceSize() || string(blob[:len(magic)]) != magic {
		return nil, errors.New("keychain: secrets file is not a barnraise store")
	}
	blob = blob[len(magic):]
	nonce, ct := blob[:aead.NonceSize()], blob[aead.NonceSize():]
	pt, err := aead.Open(nil, nonce, ct, []byte(f.service))
	if err != nil {
		return nil, errors.New("keychain: secrets file failed authentication (wrong key or tampered)")
	}
	if err := json.Unmarshal(pt, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func (f *FileStore) save(m map[string]string) error {
	if err := f.ensureDir(); err != nil {
		return err
	}
	k, err := f.key(true)
	if err != nil {
		return err
	}
	aead, err := chacha20poly1305.NewX(k)
	if err != nil {
		return err
	}
	pt, err := json.Marshal(m)
	if err != nil {
		return err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	out := append([]byte(magic), nonce...)
	out = aead.Seal(out, nonce, pt, []byte(f.service))

	tmp, err := os.CreateTemp(f.dir, ".secrets-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(f.dir, storeFile))
}
