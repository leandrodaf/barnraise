package ports

// KeyBroker keeps the API keys of key-based providers out of the agents'
// sessions when the egress proxy is in use: a session gets a placeholder
// that the proxy swaps for the real key only on requests to the
// provider's own host.
type KeyBroker interface {
	// Seal returns a placeholder for key that is only honoured toward
	// host, and a function that forgets it (safe to call more than once).
	// ok is false when the broker does not guard host: keep the key then.
	Seal(host, key string) (placeholder string, release func(), ok bool)
	// CABundle is the PEM bundle a session's TLS clients must trust (the
	// system roots and the proxy's CA); empty when the proxy does not
	// terminate TLS.
	CABundle() []byte
}
