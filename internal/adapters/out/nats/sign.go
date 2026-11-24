package nats

import (
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/nats-io/nkeys"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// Sign signs msg with the machine key whose seed is given (the NKey of
// daemon.yaml) and returns the public key and the signature (base64url).
func Sign(seed string, msg []byte) (publicKey, signature string, err error) {
	kp, err := nkeys.FromSeed([]byte(seed))
	if err != nil {
		return "", "", fmt.Errorf("machine key: %w", err)
	}
	defer kp.Wipe()
	sig, err := kp.Sign(msg)
	if err != nil {
		return "", "", err
	}
	pub, err := kp.PublicKey()
	if err != nil {
		return "", "", err
	}
	return pub, base64.RawURLEncoding.EncodeToString(sig), nil
}

// VerifySigned checks a signature made by Sign with publicKey.
func VerifySigned(publicKey string, msg []byte, signature string) error {
	if publicKey == "" || signature == "" {
		return errors.New("not signed")
	}
	kp, err := nkeys.FromPublicKey(publicKey)
	if err != nil {
		return fmt.Errorf("public key: %w", err)
	}
	sig, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil {
		return fmt.Errorf("signature: %w", err)
	}
	return kp.Verify(msg, sig)
}

// AnchorVerifier checks that an event chain anchor was signed by the
// machine key pub (the NKey of daemon.yaml).
func AnchorVerifier(pub string) func(domain.ChainAnchor) error {
	return func(a domain.ChainAnchor) error {
		if pub == "" {
			return errors.New("this machine has no key in daemon.yaml")
		}
		if a.PublicKey != pub {
			return fmt.Errorf("signed by %s, not by this machine's key", a.PublicKey)
		}
		return VerifySigned(a.PublicKey, a.SignedBytes(), a.Signature)
	}
}
