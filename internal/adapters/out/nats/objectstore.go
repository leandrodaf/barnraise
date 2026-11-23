package nats

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/leandrodaf/barnraise/internal/ports"
)

const (
	// BlobBucket is the JetStream Object Store bucket mirroring the blobs.
	BlobBucket = "BR_BLOBS"
	blobStream = "OBJ_" + BlobBucket
)

// ErrBlobNotFound means no local copy and no peer had it.
var ErrBlobNotFound = errors.New("blob not found")

// ObjectStore keeps blobs by sha256 on the local disk, optionally mirrored
// into the local JetStream bucket so peers can fetch them. Get falls back to
// the peers' buckets and caches what it downloads.
type ObjectStore struct {
	dir string
	obj jetstream.ObjectStore // nil when not mirrored

	mu    sync.Mutex
	peers map[string]blobPeer
}

type blobPeer struct {
	url  string
	opts []nats.Option
}

var _ ports.BlobStore = (*ObjectStore)(nil)

// NewObjectStore stores blobs under dir. When js is not nil, every Put is
// also written to the BR_BLOBS bucket of that (local) JetStream.
func NewObjectStore(ctx context.Context, dir string, js jetstream.JetStream) (*ObjectStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &ObjectStore{dir: dir, peers: map[string]blobPeer{}}
	if js != nil {
		obj, err := js.CreateOrUpdateObjectStore(ctx, jetstream.ObjectStoreConfig{
			Bucket:      BlobBucket,
			Description: "content-addressed blobs (logs, prints, diffs)",
			Storage:     jetstream.FileStorage,
		})
		if err != nil {
			return nil, fmt.Errorf("nats: blob bucket: %w", err)
		}
		s.obj = obj
	}
	return s, nil
}

// AddPeer registers a machine whose bucket Get may fetch from.
func (s *ObjectStore) AddPeer(name, url string, opts ...nats.Option) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.peers[name] = blobPeer{url: url, opts: opts}
}

// RemovePeer forgets a peer.
func (s *ObjectStore) RemovePeer(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.peers, name)
}

// ValidHash reports whether h is a lowercase sha256 hex digest.
func ValidHash(h string) bool {
	if len(h) != 64 {
		return false
	}
	for _, c := range h {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// Path is where the blob lives (or would live) on disk.
func (s *ObjectStore) Path(hash string) string {
	if !ValidHash(hash) {
		return filepath.Join(s.dir, "invalid")
	}
	return filepath.Join(s.dir, hash[:2], hash)
}

// Put stores the content and returns its sha256 hex digest.
func (s *ObjectStore) Put(ctx context.Context, r io.Reader) (string, error) {
	hash, err := s.writeLocal(r, "")
	if err != nil {
		return "", err
	}
	if s.obj != nil {
		if err := s.mirror(ctx, hash); err != nil {
			return hash, err
		}
	}
	return hash, nil
}

// writeLocal copies r into the store; when want is set the digest must match.
func (s *ObjectStore) writeLocal(r io.Reader, want string) (string, error) {
	tmp, err := os.CreateTemp(s.dir, ".put-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), r); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	hash := hex.EncodeToString(h.Sum(nil))
	if want != "" && hash != want {
		return "", fmt.Errorf("nats: blob digest mismatch: want %s got %s", want, hash)
	}
	dst := s.Path(hash)
	if _, err := os.Stat(dst); err == nil {
		return hash, nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return "", err
	}
	return hash, nil
}

func (s *ObjectStore) mirror(ctx context.Context, hash string) error {
	if _, err := s.obj.GetInfo(ctx, hash); err == nil {
		return nil
	}
	f, err := os.Open(s.Path(hash))
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := s.obj.Put(ctx, jetstream.ObjectMeta{Name: hash}, f); err != nil {
		return fmt.Errorf("nats: mirror blob: %w", err)
	}
	return nil
}

// Get opens a blob: local disk, then the local bucket, then each peer.
func (s *ObjectStore) Get(ctx context.Context, hash string) (io.ReadCloser, error) {
	if !ValidHash(hash) {
		return nil, fmt.Errorf("nats: invalid blob hash %q", hash)
	}
	if f, err := os.Open(s.Path(hash)); err == nil {
		return f, nil
	}
	if s.obj != nil {
		if r, err := s.obj.Get(ctx, hash); err == nil {
			_, err := s.writeLocal(r, hash)
			r.Close()
			if err == nil {
				return os.Open(s.Path(hash))
			}
		}
	}
	s.mu.Lock()
	peers := make([]blobPeer, 0, len(s.peers))
	for _, p := range s.peers {
		peers = append(peers, p)
	}
	s.mu.Unlock()
	for _, p := range peers {
		if err := s.FetchFrom(ctx, p.url, hash, p.opts...); err == nil {
			return os.Open(s.Path(hash))
		}
	}
	return nil, fmt.Errorf("%w: %s", ErrBlobNotFound, hash)
}

// FetchFrom downloads a blob from a peer's bucket into the local store
// (and the local bucket when mirrored). The digest is verified.
func (s *ObjectStore) FetchFrom(ctx context.Context, peerURL, hash string, opts ...nats.Option) error {
	if !ValidHash(hash) {
		return fmt.Errorf("nats: invalid blob hash %q", hash)
	}
	nc, err := nats.Connect(peerURL, append([]nats.Option{nats.Timeout(5 * time.Second), nats.Name("barnraise-blob-fetch")}, opts...)...)
	if err != nil {
		return err
	}
	defer nc.Close()
	js, err := jetstream.New(nc)
	if err != nil {
		return err
	}
	obj, err := js.ObjectStore(ctx, BlobBucket)
	if err != nil {
		return err
	}
	r, err := obj.Get(ctx, hash)
	if err != nil {
		return err
	}
	defer r.Close()
	if _, err := s.writeLocal(r, hash); err != nil {
		return err
	}
	if s.obj != nil {
		return s.mirror(ctx, hash)
	}
	return nil
}
