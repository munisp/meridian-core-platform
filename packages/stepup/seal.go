package stepup

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
)

// Sealer protects TOTP secrets at rest. AES-256-GCM (stdlib) keyed from
// STEPUP_SEAL_KEY (any passphrase; SHA-256 stretched to the AES key).
//
// Residual / protection level: this is envelope encryption with a
// passphrase-held key — it protects the DB/snapshot at rest against
// wholesale theft but NOT against an attacker who also reads process env.
// The packages/keyx KMS/HSM providers are signing-only today; wiring
// envelope encryption through KMS is the documented follow-up. PROFILE=prod
// without an explicit STEPUP_SEAL_KEY fails closed (ProdReady() == false).

const devSealKey = "meridian-stepup-dev-seal-change-me"

// Sealer seals and opens secrets.
type Sealer struct {
	key    []byte
	devKey bool
}

// NewSealer builds a Sealer from STEPUP_SEAL_KEY. In non-prod profiles an
// unset key falls back to a well-known dev key (loud; callers must log).
func NewSealer(profile string) (*Sealer, error) {
	k := os.Getenv("STEPUP_SEAL_KEY")
	if k == "" {
		if profile == "prod" {
			return nil, errors.New("stepup: PROFILE=prod requires STEPUP_SEAL_KEY (fail-closed)")
		}
		k = devSealKey
	}
	sum := sha256.Sum256([]byte(k))
	return &Sealer{key: sum[:], devKey: k == devSealKey}, nil
}

// UsingDevKey reports whether the well-known dev key is in use.
func (s *Sealer) UsingDevKey() bool { return s.devKey }

// Seal encrypts plaintext, returning base64(nonce|ciphertext).
func (s *Sealer) Seal(plaintext string) (string, error) {
	gcm, err := s.gcm()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("stepup: seal nonce: %w", err)
	}
	ct := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.RawStdEncoding.EncodeToString(ct), nil
}

// Open decrypts a value produced by Seal.
func (s *Sealer) Open(sealed string) (string, error) {
	gcm, err := s.gcm()
	if err != nil {
		return "", err
	}
	raw, err := base64.RawStdEncoding.DecodeString(sealed)
	if err != nil {
		return "", fmt.Errorf("stepup: sealed secret encoding: %w", err)
	}
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("stepup: sealed secret truncated")
	}
	pt, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil)
	if err != nil {
		return "", fmt.Errorf("stepup: sealed secret open: %w", err)
	}
	return string(pt), nil
}

func (s *Sealer) gcm() (cipher.AEAD, error) {
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
