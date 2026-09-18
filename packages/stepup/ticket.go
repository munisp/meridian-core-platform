package stepup

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// Ticket is a short-lived, single-use step-up credential obtained from the
// challenge endpoint with a valid TOTP. It binds actor + action + payload
// hash and is HMAC-SHA256 signed with the shared STEPUP_TICKET_KEY so
// downstream services (e.g. ledger) can verify without sharing the TOTP
// secret store.
type Ticket struct {
	JTI           string `json:"jti"`
	Actor         string `json:"actor"`
	Action        string `json:"action"`
	PayloadSHA256 string `json:"payload_sha256"` // hex sha256 of the gated request body
	Exp           int64  `json:"exp"`
}

var (
	ErrTicketMalformed = errors.New("stepup: malformed ticket")
	ErrTicketSignature = errors.New("stepup: ticket signature mismatch")
	ErrTicketExpired   = errors.New("stepup: ticket expired")
	ErrTicketBinding   = errors.New("stepup: ticket does not match actor/action/payload")
)

// HashPayload returns the canonical hex SHA-256 of a gated request body.
func HashPayload(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// TicketKey resolves the shared ticket signing key (STEPUP_TICKET_KEY).
// fallback is used in non-prod when the env is unset (dev: the service JWT
// secret); prod without an explicit key fails closed (returns error).
func TicketKey(profile, fallback string) ([]byte, error) {
	if k := os.Getenv("STEPUP_TICKET_KEY"); k != "" {
		return []byte(k), nil
	}
	if profile == "prod" {
		return nil, errors.New("stepup: PROFILE=prod requires STEPUP_TICKET_KEY (fail-closed)")
	}
	if fallback == "" {
		fallback = "meridian-stepup-dev-ticket-key"
	}
	return []byte(fallback), nil
}

// MintTicket issues a signed ticket for actor+action+payload, TTL ≤ 5min.
func MintTicket(key []byte, actor, action, payloadHash string, now time.Time) (string, error) {
	jti := make([]byte, 16)
	if _, err := rand.Read(jti); err != nil {
		return "", fmt.Errorf("stepup: ticket jti: %w", err)
	}
	t := Ticket{
		JTI:           hex.EncodeToString(jti),
		Actor:         actor,
		Action:        action,
		PayloadSHA256: payloadHash,
		Exp:           now.Add(TicketTTL).Unix(),
	}
	body, err := json.Marshal(t)
	if err != nil {
		return "", err
	}
	b64 := base64.RawURLEncoding.EncodeToString(body)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(b64))
	return b64 + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

// VerifyTicket checks signature, expiry, binding (actor/action/payload)
// and single-use (via store.ConsumeTicket). Any failure is an error;
// replay maps to ErrReplay.
func VerifyTicket(key []byte, store Store, tok, actor, action, payloadHash string, now time.Time) (*Ticket, error) {
	parts := strings.Split(tok, ".")
	if len(parts) != 2 {
		return nil, ErrTicketMalformed
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(parts[0]))
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(sig, mac.Sum(nil)) {
		return nil, ErrTicketSignature
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, ErrTicketMalformed
	}
	var t Ticket
	if err := json.Unmarshal(body, &t); err != nil {
		return nil, ErrTicketMalformed
	}
	if now.Unix() > t.Exp || t.Exp > now.Add(TicketTTL).Unix()+30 {
		return nil, ErrTicketExpired
	}
	if t.Actor != actor || t.Action != action || t.PayloadSHA256 != payloadHash {
		return nil, ErrTicketBinding
	}
	if err := store.ConsumeTicket(t.JTI, time.Unix(t.Exp, 0)); err != nil {
		return nil, err // ErrReplay
	}
	return &t, nil
}
