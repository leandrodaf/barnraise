package domain

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"hash"
	"strconv"
	"time"
)

// chainDomain separates the event hash from any other SHA-256 Barnraise
// computes; it changes only if the canonical form below changes.
const chainDomain = "barnraise.event.v1"

// ChainHash is the hash of a stored event in the local log's chain:
// SHA-256 over the previous event's hash and the event's canonical content
// (seq, id, project, type, subject, machine, actor, time in nanoseconds and
// the raw data). Every field is length-prefixed, so no two different events
// share a canonical form. The first event of a log has prev "".
func ChainHash(prev string, e Event) string {
	h := sha256.New()
	field(h, chainDomain)
	field(h, prev)
	field(h, strconv.FormatInt(e.Seq, 10))
	field(h, e.ID)
	field(h, e.Project)
	field(h, e.Type)
	field(h, e.Subject)
	field(h, e.Machine)
	field(h, e.Actor)
	field(h, strconv.FormatInt(chainTime(e.Time), 10))
	fieldBytes(h, e.Data)
	return hex.EncodeToString(h.Sum(nil))
}

// chainTime is the time as the store keeps it (UTC nanoseconds).
func chainTime(t time.Time) int64 { return t.UTC().UnixNano() }

func field(h hash.Hash, s string) { fieldBytes(h, []byte(s)) }

func fieldBytes(h hash.Hash, b []byte) {
	var n [binary.MaxVarintLen64]byte
	h.Write(n[:binary.PutUvarint(n[:], uint64(len(b)))])
	h.Write(b)
}

// ChainBreak is the first place the chain does not hold.
type ChainBreak struct {
	Seq int64 `json:"seq"`
	// Reason: "hash" (the content does not match its hash: the row was
	// edited), "link" (prev_hash is not the hash of the event before it: a
	// row was removed or inserted) or "missing" (a row has no hash).
	Reason string `json:"reason"`
	Want   string `json:"want,omitempty"`
	Got    string `json:"got,omitempty"`
}

// ChainReport is the result of walking the chain.
type ChainReport struct {
	From    int64             `json:"from"`    // first seq checked (0: empty)
	To      int64             `json:"to"`      // last seq checked
	Checked int64             `json:"checked"` // events checked
	Head    string            `json:"head,omitempty"`
	Genesis int64             `json:"genesis"` // events up to this seq were chained by the migration, not on write
	Break   *ChainBreak       `json:"break,omitempty"`
	Anchor  *ChainAnchorCheck `json:"anchor,omitempty"`
}

// OK reports an intact chain (and anchor, when one was checked).
func (r ChainReport) OK() bool { return r.Break == nil && (r.Anchor == nil || r.Anchor.OK) }

// ChainAnchor is a signed statement of the chain's head at a moment: it lets
// a later check notice that events after it were cut off, which the links
// alone cannot show.
type ChainAnchor struct {
	Seq       int64     `json:"seq"`
	Hash      string    `json:"hash"`
	Time      time.Time `json:"time"`
	Machine   string    `json:"machine"`
	PublicKey string    `json:"public_key"`
	Signature string    `json:"signature"`
}

// SignedBytes is what the anchor's signature covers.
func (a ChainAnchor) SignedBytes() []byte {
	return []byte(chainDomain + ".anchor\n" + strconv.FormatInt(a.Seq, 10) + "\n" + a.Hash + "\n" +
		strconv.FormatInt(chainTime(a.Time), 10) + "\n" + a.Machine)
}

// ChainAnchorCheck is the verdict on the last anchor.
type ChainAnchorCheck struct {
	Seq    int64  `json:"seq"`
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"`
}
