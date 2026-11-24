package egress

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// CA is the daemon's own certificate authority. The proxy uses it to
// terminate TLS only for the hosts it must see (the key-based providers,
// hosts with path rules, the hosts the project asks to inspect); every
// other host is tunneled untouched. Its key never leaves the data dir.
type CA struct {
	cert    *x509.Certificate
	key     crypto.Signer
	certPEM []byte
	now     func() time.Time

	mu      sync.Mutex
	leafKey *ecdsa.PrivateKey
	leaves  map[string]*tls.Certificate
}

// CA file names inside the proxy's folder of the data dir.
const (
	CACertFile = "ca.pem"
	CAKeyFile  = "ca-key.pem"
)

// NewCA creates a CA in memory (tests, and the first run of LoadOrCreateCA).
func NewCA(now func() time.Time) (*CA, error) {
	if now == nil {
		now = time.Now
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	t := now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: "Barnraise egress proxy local CA", Organization: []string{"Barnraise"}},
		NotBefore:             t.Add(-time.Hour),
		NotAfter:              t.AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	return newCA(der, key, now)
}

func newCA(der []byte, key crypto.Signer, now func() time.Time) (*CA, error) {
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &CA{cert: cert, key: key, now: now, leaves: map[string]*tls.Certificate{},
		certPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}, nil
}

// LoadOrCreateCA reads the CA from dir, creating it (files 0600, folder
// 0700) on first use.
func LoadOrCreateCA(dir string, now func() time.Time) (*CA, error) {
	if now == nil {
		now = time.Now
	}
	certPath, keyPath := filepath.Join(dir, CACertFile), filepath.Join(dir, CAKeyFile)
	certPEM, cerr := os.ReadFile(certPath)
	keyPEM, kerr := os.ReadFile(keyPath)
	if cerr == nil && kerr == nil {
		cb, _ := pem.Decode(certPEM)
		kb, _ := pem.Decode(keyPEM)
		if cb == nil || kb == nil {
			return nil, fmt.Errorf("egress CA in %s is not PEM", dir)
		}
		k, err := x509.ParsePKCS8PrivateKey(kb.Bytes)
		if err != nil {
			return nil, fmt.Errorf("egress CA key: %w", err)
		}
		signer, ok := k.(crypto.Signer)
		if !ok {
			return nil, errors.New("egress CA key cannot sign")
		}
		return newCA(cb.Bytes, signer, now)
	}
	if !errors.Is(cerr, os.ErrNotExist) && cerr != nil {
		return nil, cerr
	}
	ca, err := NewCA(now)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(ca.key)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		return nil, err
	}
	if err := os.WriteFile(certPath, ca.certPEM, 0o600); err != nil {
		return nil, err
	}
	return ca, nil
}

// CertPEM is the CA certificate, for the sessions' trust bundle.
func (c *CA) CertPEM() []byte { return c.certPEM }

// Pool is a cert pool with only this CA (tests and clients that trust it).
func (c *CA) Pool() *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(c.cert)
	return p
}

// Leaf returns a certificate for host signed by the CA, cached until it
// gets close to its end.
func (c *CA) Leaf(host string) (*tls.Certificate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if l, ok := c.leaves[host]; ok && now.Add(24*time.Hour).Before(l.Leaf.NotAfter) {
		return l, nil
	}
	if c.leafKey == nil {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, err
		}
		c.leafKey = k
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(30 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(host); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, &c.leafKey.PublicKey, c.key)
	if err != nil {
		return nil, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	l := &tls.Certificate{Certificate: [][]byte{der, c.cert.Raw}, PrivateKey: c.leafKey, Leaf: leaf}
	c.leaves[host] = l
	return l, nil
}

func serial() *big.Int {
	n, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	return n.Add(n, big.NewInt(1))
}

// SystemBundlePaths are where the system's CA bundle lives on Linux and
// macOS, in the order they are tried (after $SSL_CERT_FILE).
var SystemBundlePaths = []string{
	"/etc/ssl/certs/ca-certificates.crt",
	"/etc/pki/tls/certs/ca-bundle.crt",
	"/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem",
	"/etc/ssl/ca-bundle.pem",
	"/etc/pki/tls/cacert.pem",
	"/etc/ssl/cert.pem",
}

// TrustBundle is the system's CA bundle followed by this CA: what a
// session's TLS clients must trust when SSL_CERT_FILE (and the like)
// replaces their roots. Without a readable system bundle it fails, and
// the daemon then does not terminate TLS at all.
func (c *CA) TrustBundle() ([]byte, error) {
	paths := SystemBundlePaths
	if f := os.Getenv("SSL_CERT_FILE"); f != "" {
		paths = append([]string{f}, paths...)
	}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil || len(data) == 0 {
			continue
		}
		if !x509.NewCertPool().AppendCertsFromPEM(data) {
			continue
		}
		out := append([]byte(nil), data...)
		if out[len(out)-1] != '\n' {
			out = append(out, '\n')
		}
		return append(out, c.certPEM...), nil
	}
	return nil, errors.New("no system CA bundle found (looked for $SSL_CERT_FILE and the usual Linux and macOS paths)")
}
