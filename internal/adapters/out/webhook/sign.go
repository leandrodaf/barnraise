package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Header names.
const (
	HeaderSignature = "X-Barnraise-Signature"
	HeaderTimestamp = "X-Barnraise-Timestamp"
	HeaderDelivery  = "X-Barnraise-Delivery"
)

// Verification errors.
var (
	ErrBadSignature = errors.New("webhook signature does not match")
	ErrStale        = errors.New("webhook timestamp outside the replay window")
	ErrNoSignature  = errors.New("webhook is not signed")
)

// Sign returns "sha256=<hex>" over timestamp + "." + body.
func Sign(secret, timestamp string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(timestamp))
	m.Write([]byte{'.'})
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

// SignRequest sets the timestamp and signature headers on r for body.
func SignRequest(r *http.Request, secret string, body []byte, now time.Time) {
	ts := strconv.FormatInt(now.Unix(), 10)
	r.Header.Set(HeaderTimestamp, ts)
	if secret != "" {
		r.Header.Set(HeaderSignature, Sign(secret, ts, body))
	}
}

// Verify checks an inbound signature in constant time and rejects
// timestamps further than tolerance from now (tolerance <= 0 disables the
// window check).
func Verify(secret string, body []byte, sigHeader, timestamp string, tolerance time.Duration) error {
	return VerifyAt(time.Now(), secret, body, sigHeader, timestamp, tolerance)
}

// VerifyAt is Verify with an explicit clock.
func VerifyAt(now time.Time, secret string, body []byte, sigHeader, timestamp string, tolerance time.Duration) error {
	if sigHeader == "" || timestamp == "" {
		return ErrNoSignature
	}
	sec, err := strconv.ParseInt(strings.TrimSpace(timestamp), 10, 64)
	if err != nil {
		return ErrStale
	}
	if tolerance > 0 {
		d := now.Sub(time.Unix(sec, 0))
		if d < 0 {
			d = -d
		}
		if d > tolerance {
			return ErrStale
		}
	}
	want := Sign(secret, strings.TrimSpace(timestamp), body)
	if !hmac.Equal([]byte(want), []byte(strings.TrimSpace(sigHeader))) {
		return ErrBadSignature
	}
	return nil
}

// VerifyRequest reads the headers of r and verifies body.
func VerifyRequest(r *http.Request, secret string, body []byte, tolerance time.Duration) error {
	return Verify(secret, body, r.Header.Get(HeaderSignature), r.Header.Get(HeaderTimestamp), tolerance)
}
