// Package webhook sends Barnraise events as CloudEvents to outgoing,
// HMAC-signed webhooks with retries, and verifies inbound webhooks signed
// with the same scheme.
//
// Event type mapping: the CloudEvents `type` is the Barnraise type with the
// reverse-DNS prefix "dev.barnraise." (item.created becomes
// dev.barnraise.item.created), as the CloudEvents spec recommends. Webhook
// event filters are written with the plain Barnraise type ("item.*",
// "check.completed"); the prefix is stripped before matching.
//
// Signature scheme (outgoing and inbound):
//
//	X-Barnraise-Timestamp: <unix seconds>
//	X-Barnraise-Signature: sha256=<hex HMAC-SHA256(secret, timestamp + "." + body)>
//	X-Barnraise-Delivery:  <CloudEvent id>
//
// The timestamp is part of the signed message so that the replay window
// enforced by Verify cannot be bypassed by changing the header.
package webhook

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// TypePrefix is prepended to Barnraise event types.
const TypePrefix = "dev.barnraise."

// CloudEvent is a CloudEvents 1.0 event in structured JSON mode.
type CloudEvent struct {
	SpecVersion     string          `json:"specversion"`
	ID              string          `json:"id"`
	Source          string          `json:"source"`
	Type            string          `json:"type"`
	Subject         string          `json:"subject,omitempty"`
	Time            time.Time       `json:"time"`
	DataContentType string          `json:"datacontenttype,omitempty"`
	Data            json.RawMessage `json:"data,omitempty"`
	// Extension attributes (lowercase alphanumeric names per the spec).
	Project string `json:"barnraiseproject,omitempty"`
	Machine string `json:"barnraisemachine,omitempty"`
	Actor   string `json:"barnraiseactor,omitempty"`
}

// Source returns "barnraise://<machine>/<project>".
func Source(machine, project string) string {
	return "barnraise://" + machine + "/" + project
}

// ToCloudEvent converts an event. An empty source defaults to
// Source(e.Machine, e.Project).
func ToCloudEvent(e domain.Event, source string) CloudEvent {
	if source == "" {
		source = Source(e.Machine, e.Project)
	}
	ce := CloudEvent{
		SpecVersion: "1.0",
		ID:          e.ID,
		Source:      source,
		Type:        TypePrefix + e.Type,
		Subject:     e.Subject,
		Time:        e.Time.UTC(),
		Project:     e.Project,
		Machine:     e.Machine,
		Actor:       e.Actor,
	}
	if len(e.Data) > 0 {
		ce.DataContentType = "application/json"
		ce.Data = e.Data
	}
	return ce
}

// BarnraiseType strips TypePrefix from a CloudEvents type.
func BarnraiseType(ceType string) string { return strings.TrimPrefix(ceType, TypePrefix) }

// Matches reports whether a Barnraise (or prefixed CloudEvents) type passes
// a filter list: empty means all; "item.*" matches item.created and
// item.step.started; "*" matches all; anything else is exact.
func Matches(filters []string, typ string) bool {
	if len(filters) == 0 {
		return true
	}
	typ = BarnraiseType(typ)
	for _, f := range filters {
		f = BarnraiseType(strings.TrimSpace(f))
		switch {
		case f == "*" || f == typ:
			return true
		case strings.HasSuffix(f, ".*") && strings.HasPrefix(typ, strings.TrimSuffix(f, "*")):
			return true
		}
	}
	return false
}
