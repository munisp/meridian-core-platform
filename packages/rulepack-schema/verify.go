package rulepackschema

// Runtime ed25519 signature verification for rule packs (A1-09, R4 bridge).
//
// Signing contract "meridian-ceremony-canonical-yaml/v1": the signed message
// is the pack mapping WITHOUT the `signed` block, serialised as canonical
// YAML — byte-identical to meridian-rule-packs
// tools/rpcommon.canonical_bytes (PyYAML safe_dump, sort_keys=True,
// allow_unicode=True, default_flow_style=False, width=10**6, UTF-8). This
// IS the ceremony contract: tools/ceremony.py signs exactly these bytes
// (ed25519, key_id governance-board-2026), and packs cannot be re-signed,
// so the ceremony contract alone governs verification.
//
// R4 contract bridge: this supersedes the retired "meridian-canonical-json/v1"
// runtime contract, under which NO ceremony pack ever verified (the ceremony
// never signed JSON-canonical bytes) and the Python packloader disagreed with
// this Go verifier (sha256 digest vs raw message). There is now exactly ONE
// contract across ceremony, Python packloader, and Go.
//
// Because the signed bytes are a PyYAML serialisation, verification runs
// against the STORED canonical YAML artifact (the vendored pack file, or
// the YAML held by the pack registry/WORM archive): the artifact is parsed
// and re-emitted in the ceremony's canonical form (canon_yaml.go), never
// re-signed from a Go-native serialisation. Byte-exactness of the Go
// emitter is proven by verifying real ceremony signatures over its output
// (see verify_test.go — ed25519 succeeds only on byte-identical messages).

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"gopkg.in/yaml.v3"
)

// VerifyPackSignature cryptographically verifies pack.Signed against the
// pinned public keys (key_id -> ed25519 public key). canonical is the
// ceremony canonical signing bytes derived from the stored YAML artifact
// (CanonicalSigningBytesFromYAML). Fail-closed: unsigned packs, unknown key
// ids, malformed or non-matching signatures, or an artifact that does not
// match the pack being verified are errors.
func VerifyPackSignature(pack *Pack, canonical []byte, keys map[string]ed25519.PublicKey) error {
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
	if len(canonical) == 0 {
		return errors.New("rulepack-schema: no canonical artifact bytes supplied; refusing to verify against re-serialised content")
	}
	// Binding check: the supplied canonical bytes must decode to exactly the
	// pack being verified (minus its signed block), so a signature over a
	// DIFFERENT pack's artifact cannot be replayed against this pack.
	if err := bindCanonicalToPack(pack, canonical); err != nil {
		return err
	}
	if !ed25519.Verify(pub, canonical, sig) {
		return fmt.Errorf("rulepack-schema: ed25519 signature does not verify against pinned key %q", pack.Signed.KeyID)
	}
	return nil
}

// VerifyPackYAMLArtifact is the common case: verify the signature on the
// pack carried by the raw YAML artifact (vendored file / registry YAML),
// deriving the ceremony canonical bytes from the artifact itself.
func VerifyPackYAMLArtifact(pack *Pack, artifact []byte, keys map[string]ed25519.PublicKey) error {
	canonical, err := CanonicalSigningBytesFromYAML(artifact)
	if err != nil {
		return err
	}
	return VerifyPackSignature(pack, canonical, keys)
}

// bindCanonicalToPack decodes the canonical bytes and requires deep
// equality with pack.Raw minus the `signed` block.
func bindCanonicalToPack(pack *Pack, canonical []byte) error {
	if pack.Raw == nil {
		return errors.New("rulepack-schema: pack has no decoded raw form to bind against the artifact")
	}
	var canonRaw map[string]any
	if err := yaml.Unmarshal(canonical, &canonRaw); err != nil {
		return fmt.Errorf("rulepack-schema: canonical artifact does not decode: %w", err)
	}
	body := make(map[string]any, len(pack.Raw))
	for k, v := range pack.Raw {
		if k == "signed" {
			continue
		}
		body[k] = v
	}
	if !reflect.DeepEqual(body, canonRaw) {
		return errors.New("rulepack-schema: canonical artifact does not match the pack being verified (binding check failed)")
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
