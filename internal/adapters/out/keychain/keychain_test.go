package keychain

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/leandrodaf/barnraise/internal/ports"
)

func TestFileStoreRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "secrets")
	s := New("barnraise-test", dir, ForceFile())
	if s.Backend() != "file" {
		t.Fatal("expected file backend")
	}
	if _, err := s.Get("missing"); !errors.Is(err, ports.ErrSecretNotFound) {
		t.Fatalf("err = %v", err)
	}
	if err := s.Set("github", "ghp_value"); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("slack", "hook"); err != nil {
		t.Fatal(err)
	}
	// A fresh store reads what the first one wrote.
	s2 := New("barnraise-test", dir, ForceFile())
	if v, err := s2.Get("github"); err != nil || v != "ghp_value" {
		t.Fatalf("v=%q err=%v", v, err)
	}
	if err := s2.Delete("github"); err != nil {
		t.Fatal(err)
	}
	if err := s2.Delete("github"); err != nil {
		t.Fatal("deleting a missing key must not fail")
	}
	if _, err := s.Get("github"); !errors.Is(err, ports.ErrSecretNotFound) {
		t.Fatalf("err = %v", err)
	}

	for name, want := range map[string]os.FileMode{"": 0o700, keyFile: 0o600, storeFile: 0o600} {
		st, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != want {
			t.Errorf("%q mode = %o, want %o", name, st.Mode().Perm(), want)
		}
	}
	blob, _ := os.ReadFile(filepath.Join(dir, storeFile))
	if bytes.Contains(blob, []byte("hook")) || bytes.Contains(blob, []byte("slack")) {
		t.Fatal("store is not encrypted")
	}
}

func TestFileStoreTamperAndWrongService(t *testing.T) {
	dir := t.TempDir()
	s := NewFileStore(dir, "svc")
	if err := s.Set("k", "v"); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileStore(dir, "other").Get("k"); err == nil {
		t.Fatal("other service must not open the store")
	}
	p := filepath.Join(dir, storeFile)
	blob, _ := os.ReadFile(p)
	blob[len(blob)-1] ^= 1
	os.WriteFile(p, blob, 0o600)
	if _, err := s.Get("k"); err == nil {
		t.Fatal("tampered store must fail")
	}
}

func TestFileStoreConcurrent(t *testing.T) {
	s := New("svc", t.TempDir(), ForceFile())
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := s.Set(string(rune('a'+i)), "v"); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	for i := range 8 {
		if _, err := s.Get(string(rune('a' + i))); err != nil {
			t.Fatal(err)
		}
	}
}

type fakeBackend struct {
	mu   sync.Mutex
	m    map[string]string
	fail error
	hang bool
}

func (f *fakeBackend) Set(s, u, p string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.m[s+"/"+u] = p
	return nil
}

func (f *fakeBackend) Get(s, u string) (string, error) {
	if f.hang {
		time.Sleep(time.Second)
	}
	if f.fail != nil {
		return "", f.fail
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.m[s+"/"+u]
	if !ok {
		return "", keyring.ErrNotFound
	}
	return v, nil
}

func (f *fakeBackend) Delete(s, u string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.m[s+"/"+u]; !ok {
		return keyring.ErrNotFound
	}
	delete(f.m, s+"/"+u)
	return nil
}

func TestSystemBackendUsedWhenAvailable(t *testing.T) {
	b := &fakeBackend{m: map[string]string{}}
	s := New("svc", t.TempDir(), WithBackend(b))
	if s.Backend() != "keychain" {
		t.Fatal("expected keychain")
	}
	if err := s.Set("k", "v"); err != nil {
		t.Fatal(err)
	}
	if b.m["svc/k"] != "v" {
		t.Fatal("not stored in backend")
	}
	if _, err := s.Get("nope"); !errors.Is(err, ports.ErrSecretNotFound) {
		t.Fatalf("err = %v", err)
	}
	if err := s.Delete("nope"); err != nil {
		t.Fatal(err)
	}
}

func TestFallbackWhenKeychainUnavailable(t *testing.T) {
	b := &fakeBackend{m: map[string]string{}, fail: errors.New("no dbus")}
	if s := New("svc", t.TempDir(), WithBackend(b)); s.Backend() != "file" {
		t.Fatal("expected file fallback")
	}
	h := &fakeBackend{m: map[string]string{}, hang: true}
	if s := New("svc", t.TempDir(), WithBackend(h), WithProbeTimeout(20*time.Millisecond)); s.Backend() != "file" {
		t.Fatal("expected file fallback on hanging keychain")
	}
}
