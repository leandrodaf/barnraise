package egress

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Receipt file names inside the proxy's folder of the data dir.
const (
	ReceiptsLog     = "receipts.jsonl"
	ReceiptsKeyFile = "receipts-key.pem"
	ReceiptsPubFile = "receipts.pub"
)

// Receipt records one decision of the proxy. Each line of the log is
// signed by the daemon's ed25519 key and names the hash of the line before
// it, so a changed, removed or reordered line breaks verification.
type Receipt struct {
	Seq      int64     `json:"seq"`
	Time     time.Time `json:"time"`
	Decision string    `json:"decision"` // allow or block
	Host     string    `json:"host"`
	Method   string    `json:"method"`
	Path     string    `json:"path,omitempty"` // URL path only, never the query
	Rule     string    `json:"rule"`
	Detail   string    `json:"detail,omitempty"` // redacted finding of a block
	// PayloadSHA256 is the hash of the request body the proxy saw (the
	// hash of nothing for a tunnel it could not see into).
	PayloadSHA256 string `json:"payload_sha256"`
	Prev          string `json:"prev"`
	Sig           string `json:"sig,omitempty"`
}

// Receipts appends signed receipts to a log.
type Receipts struct {
	mu   sync.Mutex
	f    *os.File
	key  ed25519.PrivateKey
	seq  int64
	prev string
	now  func() time.Time
}

// OpenReceipts opens (or starts) the receipt log in dir, creating the
// signing key on first use (0600; the public key is written next to it).
func OpenReceipts(dir string, now func() time.Time) (*Receipts, error) {
	if now == nil {
		now = time.Now
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	key, err := loadOrCreateSigner(dir)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, ReceiptsLog)
	seq, prev, err := lastReceipt(path)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &Receipts{f: f, key: key, seq: seq, prev: prev, now: now}, nil
}

func loadOrCreateSigner(dir string) (ed25519.PrivateKey, error) {
	keyPath := filepath.Join(dir, ReceiptsKeyFile)
	if data, err := os.ReadFile(keyPath); err == nil {
		b, _ := pem.Decode(data)
		if b == nil {
			return nil, fmt.Errorf("%s is not PEM", keyPath)
		}
		k, err := x509.ParsePKCS8PrivateKey(b.Bytes)
		if err != nil {
			return nil, err
		}
		ek, ok := k.(ed25519.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("%s is not an ed25519 key", keyPath)
		}
		return ek, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	der, _ := x509.MarshalPKCS8PrivateKey(priv)
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		return nil, err
	}
	pder, _ := x509.MarshalPKIXPublicKey(pub)
	if err := os.WriteFile(filepath.Join(dir, ReceiptsPubFile), pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pder}), 0o644); err != nil {
		return nil, err
	}
	return priv, nil
}

// ReadPublicKey reads the receipts' public key (PEM).
func ReadPublicKey(path string) (ed25519.PublicKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	b, _ := pem.Decode(data)
	if b == nil {
		return nil, fmt.Errorf("%s is not PEM", path)
	}
	k, err := x509.ParsePKIXPublicKey(b.Bytes)
	if err != nil {
		return nil, err
	}
	pk, ok := k.(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("%s is not an ed25519 public key", path)
	}
	return pk, nil
}

// lastReceipt finds the sequence and hash of the log's last line.
func lastReceipt(path string) (int64, string, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, "", nil
	}
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return 0, "", err
	}
	const tail = 64 << 10
	off := max(st.Size()-tail, 0)
	buf := make([]byte, st.Size()-off)
	if _, err := f.ReadAt(buf, off); err != nil && err != io.EOF {
		return 0, "", err
	}
	buf = bytes.TrimRight(buf, "\n")
	if len(buf) == 0 {
		return 0, "", nil
	}
	if i := bytes.LastIndexByte(buf, '\n'); i >= 0 {
		buf = buf[i+1:]
	}
	var r Receipt
	if err := json.Unmarshal(buf, &r); err != nil {
		return 0, "", fmt.Errorf("receipt log %s: last line: %w", path, err)
	}
	return r.Seq, lineHash(buf), nil
}

func lineHash(line []byte) string {
	h := sha256.Sum256(line)
	return hex.EncodeToString(h[:])
}

// PayloadHash is the hex SHA-256 of a request body.
func PayloadHash(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func signedBytes(r Receipt) []byte {
	r.Sig = ""
	b, _ := json.Marshal(r)
	return b
}

// Record signs and appends a receipt, filling its sequence, time and
// chain. It returns the receipt as written.
func (rs *Receipts) Record(r Receipt) (Receipt, error) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.seq++
	r.Seq = rs.seq
	if r.Time.IsZero() {
		r.Time = rs.now().UTC()
	}
	r.Prev = rs.prev
	r.Sig = base64.StdEncoding.EncodeToString(ed25519.Sign(rs.key, signedBytes(r)))
	line, err := json.Marshal(r)
	if err != nil {
		return r, err
	}
	if _, err := rs.f.Write(append(line, '\n')); err != nil {
		return r, err
	}
	rs.prev = lineHash(line)
	return r, nil
}

// PublicKey is the key that verifies the receipts.
func (rs *Receipts) PublicKey() ed25519.PublicKey { return rs.key.Public().(ed25519.PublicKey) }

// Close closes the log.
func (rs *Receipts) Close() error { return rs.f.Close() }

// VerifyReceipts checks every line of the log: its signature, that it
// names the hash of the line before it, and that the sequence has no gap.
// It returns how many receipts are valid before the first failure.
func VerifyReceipts(r io.Reader, pub ed25519.PublicKey) (int, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	n := 0
	prev := ""
	var seq int64
	for sc.Scan() {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var rc Receipt
		if err := json.Unmarshal(line, &rc); err != nil {
			return n, fmt.Errorf("line %d: not a receipt: %w", n+1, err)
		}
		sig, err := base64.StdEncoding.DecodeString(rc.Sig)
		if err != nil || !ed25519.Verify(pub, signedBytes(rc), sig) {
			return n, fmt.Errorf("receipt %d: bad signature", rc.Seq)
		}
		if n == 0 && (rc.Seq != 1 || rc.Prev != "") {
			return n, fmt.Errorf("receipt %d: the log does not start at receipt 1 (lines were removed from its start)", rc.Seq)
		}
		if n > 0 && rc.Prev != prev {
			return n, fmt.Errorf("receipt %d: does not follow receipt %d (a line was changed, removed or moved)", rc.Seq, seq)
		}
		if n > 0 && rc.Seq != seq+1 {
			return n, fmt.Errorf("receipt %d: expected sequence %d", rc.Seq, seq+1)
		}
		prev, seq = lineHash(line), rc.Seq
		n++
	}
	return n, sc.Err()
}
