// Package stepup implements RFC 6238 TOTP step-up authentication for
// admin money paths (R4-9c). Stolen bearer tokens alone must not move
// money: designated routes additionally require a live TOTP code or a
// short-lived single-use step-up ticket.
//
// Stdlib-only by design (crypto/hmac, crypto/sha1, encoding/base32,
// crypto/aes, crypto/cipher): no new third-party dependencies.
//
// Cross-service contract (shared with services/settlement/app/stepup.py):
//   - secret: 20 random bytes, base32 (RFC 4648, no padding) encoded
//   - TOTP: HMAC-SHA1, 30s period, 6 digits, acceptance window ±1 step
//   - recovery codes: 10 codes, "xxxx-xxxx" Crockford-ish base32, stored
//     as SHA-256 hashes, single-use
//   - ticket: HMAC-SHA256-signed JSON {jti, actor, action, payload_sha256,
//     exp}, ≤5min TTL, single-use (jti consumed on first use)
package stepup

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6238 mandates HMAC-SHA1
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	// Period is the TOTP time step (RFC 6238 default).
	Period = 30 * time.Second
	// Digits is the TOTP code length.
	Digits = 6
	// Window is the number of steps accepted on either side of now
	// (clock-skew tolerance). ±1 step => codes live ≤ ~90s.
	Window = 1
	// SecretBytes is the raw TOTP secret length (160 bits, RFC 4226 rec).
	SecretBytes = 20
	// RecoveryCount is the number of recovery codes issued at enrollment.
	RecoveryCount = 10
	// TicketTTL bounds step-up ticket lifetime (≤ 5 minutes).
	TicketTTL = 5 * time.Minute
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// GenerateSecret returns a new random TOTP secret, base32 (no padding)
// encoded — the canonical shared format across services.
func GenerateSecret() (string, error) {
	raw := make([]byte, SecretBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("stepup: secret rng: %w", err)
	}
	return b32.EncodeToString(raw), nil
}

// decodeSecret normalises (upper-case, strip spaces/padding) and decodes a
// base32 secret.
func decodeSecret(secret string) ([]byte, error) {
	s := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(secret), " ", ""))
	s = strings.TrimRight(s, "=")
	raw, err := b32.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("stepup: bad base32 secret: %w", err)
	}
	if len(raw) < 10 {
		return nil, fmt.Errorf("stepup: secret too short (%d bytes)", len(raw))
	}
	return raw, nil
}

// Code computes the TOTP code for secret at time t (RFC 6238, HMAC-SHA1).
func Code(secret string, t time.Time) (string, error) {
	raw, err := decodeSecret(secret)
	if err != nil {
		return "", err
	}
	return codeRaw(raw, t.Unix()/int64(Period/time.Second)), nil
}

func codeRaw(raw []byte, step int64) string {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(step))
	mac := hmac.New(sha1.New, raw) //nolint:gosec // RFC 6238
	mac.Write(buf[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	bin := (uint32(sum[off])&0x7f)<<24 | uint32(sum[off+1])<<16 |
		uint32(sum[off+2])<<8 | uint32(sum[off+3])
	mod := uint32(1)
	for i := 0; i < Digits; i++ {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", Digits, bin%mod)
}

// Validate reports whether code matches secret within ±Window steps of t.
// Comparison is constant-time over the accepted window.
func Validate(secret, code string, t time.Time) bool {
	raw, err := decodeSecret(secret)
	if err != nil {
		return false
	}
	code = strings.TrimSpace(code)
	if len(code) != Digits {
		return false
	}
	now := t.Unix() / int64(Period/time.Second)
	ok := false
	for w := -Window; w <= Window; w++ {
		if subtleEqual(codeRaw(raw, now+int64(w)), code) {
			ok = true
		}
	}
	return ok
}

func subtleEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := range a {
		v |= a[i] ^ b[i]
	}
	return v == 0
}

// OTPAuthURI builds the otpauth:// provisioning URI for authenticator apps.
func OTPAuthURI(issuer, account, secret string) string {
	label := url.PathEscape(issuer) + ":" + url.PathEscape(account)
	q := url.Values{}
	q.Set("secret", strings.ToUpper(secret))
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", fmt.Sprint(Digits))
	q.Set("period", fmt.Sprint(int64(Period/time.Second)))
	return "otpauth://totp/" + label + "?" + q.Encode()
}

// GenerateRecoveryCodes returns RecoveryCount one-time recovery codes in
// "XXXX-XXXX" form plus their SHA-256 hashes (hex) for at-rest storage.
// Only the hashes are stored; the plaintext codes are shown once.
func GenerateRecoveryCodes() (codes []string, hashes []string, err error) {
	for i := 0; i < RecoveryCount; i++ {
		raw := make([]byte, 5) // 40 bits -> 8 base32 chars
		if _, err := rand.Read(raw); err != nil {
			return nil, nil, fmt.Errorf("stepup: recovery rng: %w", err)
		}
		enc := b32.EncodeToString(raw) // 8 chars
		code := enc[:4] + "-" + enc[4:]
		codes = append(codes, code)
		hashes = append(hashes, HashRecoveryCode(code))
	}
	return codes, hashes, nil
}

// HashRecoveryCode normalises and hashes a recovery code (SHA-256 hex).
func HashRecoveryCode(code string) string {
	norm := strings.ToUpper(strings.TrimSpace(code))
	sum := sha256.Sum256([]byte(norm))
	return fmt.Sprintf("%x", sum)
}
