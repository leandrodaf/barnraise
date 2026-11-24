package app

import (
	"net/url"
	"os"
	"path/filepath"

	"github.com/leandrodaf/barnraise/internal/ports"
)

// EgressCAFile is the trust bundle written into a session's folder when
// the egress proxy terminates TLS for some hosts.
const EgressCAFile = "egress-ca.pem"

// egressTrustEnv are the variables the common TLS stacks read for their
// roots (OpenSSL and Go, Node.js, Python requests, curl, git, pip, Cargo,
// Deno, Codex). All point at the same bundle: the system roots plus the
// proxy's CA, so tools that replace their roots with it lose nothing.
var egressTrustEnv = []string{
	"SSL_CERT_FILE", "NODE_EXTRA_CA_CERTS", "REQUESTS_CA_BUNDLE", "CURL_CA_BUNDLE", "GIT_SSL_CAINFO",
	"PIP_CERT", "CARGO_HTTP_CAINFO", "DENO_CERT", "CODEX_CA_CERTIFICATE",
}

// egressTrust makes the session trust the proxy's CA. Nothing happens
// without the proxy or when it does not terminate TLS.
func (r *Runtime) egressTrust(env map[string]string, sessDir string) error {
	if r.EgressProxy == "" || r.KeyBroker == nil {
		return nil
	}
	pem := r.KeyBroker.CABundle()
	if len(pem) == 0 {
		return nil
	}
	if err := os.MkdirAll(sessDir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(sessDir, EgressCAFile)
	// Certificates only, no key: readable by the agent's own user.
	if err := os.WriteFile(path, pem, 0o644); err != nil {
		return err
	}
	for _, k := range egressTrustEnv {
		env[k] = path
	}
	return nil
}

// sealKey returns what the session's environment carries for a key-based
// provider: a placeholder the proxy swaps for the key on the provider's
// own host when the proxy and its broker are in use, the key itself
// otherwise (no proxy on this machine: as before). release forgets the
// placeholder when the session ends.
func (r *Runtime) sealKey(endpoint *ports.Endpoint, key string) (string, func()) {
	if r.EgressProxy == "" || r.KeyBroker == nil || endpoint == nil {
		return key, func() {}
	}
	u, err := url.Parse(endpoint.BaseURL)
	if err != nil || u.Hostname() == "" {
		return key, func() {}
	}
	ph, release, ok := r.KeyBroker.Seal(u.Hostname(), key)
	if !ok {
		return key, func() {}
	}
	return ph, release
}
