// Package envelope implements the Meridian event envelope (SPEC 1.1).
// Every message on every nrs.* topic family uses this canonical JSON shape.
package envelope

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"time"
)

// Envelope is the canonical event envelope for all nrs.* messages.
type Envelope struct {
	ID              string          `json:"id"`                // ULID
	Type            string          `json:"type"`              // e.g. nrs.psm.payments.v1
	Source          string          `json:"source"`            // svc-name
	Time            string          `json:"time"`              // RFC3339
	TenantID        string          `json:"tenant_id"`         // string-or-empty
	TraceID         string          `json:"trace_id"`          // w3c trace id
	RulePackVersion string          `json:"rule_pack_version"` // rp-xxx@1.2.0 or empty
	Data            json.RawMessage `json:"data"`              // domain payload
}

// New builds an envelope with a fresh ULID, timestamp and trace id.
func New(eventType, source, tenantID, rulePackVersion string, data any) (Envelope, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return Envelope{}, fmt.Errorf("marshal data: %w", err)
	}
	return Envelope{
		ID:              NewULID(),
		Type:            eventType,
		Source:          source,
		Time:            time.Now().UTC().Format(time.RFC3339),
		TenantID:        tenantID,
		TraceID:         NewTraceID(),
		RulePackVersion: rulePackVersion,
		Data:            raw,
	}, nil
}

// Decode unmarshals the Data payload into v.
func (e Envelope) Decode(v any) error { return json.Unmarshal(e.Data, v) }

// DLQTopic returns the dead-letter topic for a topic family (SPEC 1.2).
func DLQTopic(topic string) string { return topic + ".dlq" }

// --- ULID (Crockford base32, 128-bit: 48ms time + 80 bits random) ---

const ulidAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewULID returns a canonical 26-char ULID string.
func NewULID() string {
	var b [16]byte
	ms := uint64(time.Now().UnixMilli())
	b[0] = byte(ms >> 40)
	b[1] = byte(ms >> 32)
	b[2] = byte(ms >> 24)
	b[3] = byte(ms >> 16)
	b[4] = byte(ms >> 8)
	b[5] = byte(ms)
	if _, err := rand.Read(b[6:]); err != nil {
		panic(err) // crypto/rand failure is unrecoverable
	}
	// Encode as 26 base32 chars over a 130-bit stream: 2 leading zero bits
	// (canonical ULID constraint) followed by the 128-bit value, MSB first.
	var out [26]byte
	for i := 0; i < 26; i++ {
		var v byte
		for k := uint(0); k < 5; k++ {
			p := uint(i)*5 + k // bit index in the 130-bit stream
			v <<= 1
			if p >= 2 {
				q := p - 2 // bit index into the 128-bit value
				v |= (b[q/8] >> (7 - q%8)) & 1
			}
		}
		out[i] = ulidAlphabet[v]
	}
	return string(out[:])
}

// NewTraceID returns a W3C trace id (32 hex chars).
func NewTraceID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return fmt.Sprintf("%x", b[:])
}

// UnixMilli parses the envelope time back to millis (utility for relays).
func (e Envelope) UnixMilli() int64 {
	t, err := time.Parse(time.RFC3339, e.Time)
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}

// MarshalBinary / UnmarshalBinary for convenience.
func (e Envelope) MarshalBinary() ([]byte, error)  { return json.Marshal(e) }
func (e *Envelope) UnmarshalBinary(b []byte) error { return json.Unmarshal(b, e) }
