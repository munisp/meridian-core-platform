package rulepackschema

// Runtime ed25519 signature verification for rule packs (A1-09).
//
// Signing contract "meridian-canonical-json/v1": the signed message is the
// pack mapping WITHOUT the `signed` block, serialised as UTF-8 JSON with
// encoding/json (map keys sorted lexicographically, no insignificant
// whitespace, HTML-sensitive characters escaped per encoding/json). This is
// deterministic and reproducible in any language; ceremony tooling must
// produce exactly these bytes (json.dumps(body, sort_keys=True,
// separators=(",", ":")) matches for the string/number/bool/null data the
// schema permits).
//
// NOTE: this supersedes the CI-only PyYAML-canonical form
// (meridian-rule-packs tools/rpcommon.canonical_bytes) for runtime
// verification — PyYAML's emitter output is not byte-reproducible from Go.
// Ceremony packs must carry a signature over the JSON-canonical bytes.

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

// CanonicalSigningBytes returns the exact bytes that are signed for a pack:
// the pack mapping minus the `signed` block as canonical JSON (v1 contract
// above). raw is typically Pack.Raw from ParsePackYAML.
func CanonicalSigningBytes(raw map[string]any) ([]byte, error) {
	if raw == nil {
		return nil, errors.New("rulepack-schema: nil pack mapping")
	}
	body := make(map[string]any, len(raw))
	for k, v := range raw {
		if k == "signed" {
			continue
		}
		body[k] = v
	}
	// encoding/json marshals maps with sorted keys — canonical by contract.
	return json.Marshal(body)
}

// VerifyPackSignature cryptographically verifies pack.Signed against the
// pinned public keys (key_id -> ed25519 public key). Fail-closed: unsigned
// packs, unknown key ids, malformed or non-matching signatures are errors.
func VerifyPackSignature(pack *Pack, keys map[string]ed25519.PublicKey) error {
	if pack == nil {
		return errors.New("rulepack-schema: nil pack")
	}
	if pack.Signed == nil {
		return errors.New("rulepack-schema: pack is unsigned")
	}
	if pack.Signed.Algorithm != "ed25519" {
		return fmt.Errorf("rulepack-schema: signed.algorithm %q must be ed25519", pack.Signed.Algorithm)
	}
	if len(keys) == 0 {
		return errors.New("rulepack-schema: no pinned signing public keys configured; cannot verify (fail-closed)")
	}
	pub, ok := keys[pack.Signed.KeyID]
	if !ok {
		return fmt.Errorf("rulepack-schema: key_id %q is not a pinned signing key", pack.Signed.KeyID)
	}
	sig, err := hex.DecodeString(pack.Signed.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("rulepack-schema: signed.signature is not a valid ed25519 signature (64-byte hex)")
	}
	msg, err := CanonicalSigningBytes(pack.Raw)
	if err != nil {
		return err
	}
	if !ed25519.Verify(pub, msg, sig) {
		return fmt.Errorf("rulepack-schema: ed25519 signature does not verify against pinned key %q", pack.Signed.KeyID)
	}
	return nil
}

// ParseSigningKeys parses RULEPACK_SIGNING_PUBKEYS: a JSON object mapping
// key_id -> hex-encoded 32-byte ed25519 public key. Env-injected; no keys
// are ever hardcoded.
func ParseSigningKeys(jsonEnv string) (map[string]ed25519.PublicKey, error) {
	keys := map[string]ed25519.PublicKey{}
	if jsonEnv == "" {
		return keys, nil
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(jsonEnv), &m); err != nil {
		return nil, fmt.Errorf("rulepack-schema: RULEPACK_SIGNING_PUBKEYS is not a JSON object: %w", err)
	}
	for kid, hexPub := range m {
		b, err := hex.DecodeString(hexPub)
		if err != nil || len(b) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("rulepack-schema: pubkey for key_id %q is not a 32-byte hex ed25519 key", kid)
		}
		keys[kid] = ed25519.PublicKey(b)
	}
	return keys, nil
}
