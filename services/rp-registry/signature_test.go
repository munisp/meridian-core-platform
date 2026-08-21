package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/munisp/meridian-core-platform/packages/events/store"
	rpschema "github.com/munisp/meridian-core-platform/packages/rulepack-schema"
)

// A1-09 regression: rule-pack ed25519 signatures must be cryptographically
// verified at publish/load against pinned, env-injected public keys —
// previously only the hex format was checked, so registry store write
// access meant arbitrary rule injection.

func signedPackBody(t *testing.T, pub ed25519.PublicKey, priv ed25519.PrivateKey, keyID string) map[string]any {
	t.Helper()
	pack := map[string]any{
		"id": "rp-signed-test", "version": "1.0.0",
		"effective_from": "2025-01-01", "effective_to": nil,
		"status": "published", "subject_to_regazette": false,
		"provenance": map[string]any{"as_passed": "passed", "as_gazetted": nil, "source_citation": "Test Regulations 2025"},
		"rules": []any{map[string]any{"id": "test.rule", "when": map[string]any{"a": 1}, "then": map[string]any{"rate_bps": 100}}},
	}
	if priv == nil {
		return pack // unsigned variant
	}
	msg, err := rpschema.CanonicalSigningBytes(pack)
	if err != nil {
		t.Fatal(err)
	}
	sig := ed25519.Sign(priv, msg)
	pack["signed"] = map[string]any{
		"algorithm": "ed25519", "key_id": keyID, "signature": hex.EncodeToString(sig),
	}
	return pack
}

func newSigningServer(t *testing.T, keys map[string]ed25519.PublicKey, prod bool) (*server, http.Handler) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := &server{st: st, signKeys: keys, prod: prod}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/packs", s.registerPack)
	return s, mux
}

func postPack(t *testing.T, h http.Handler, pack map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"pack": pack})
	r := httptest.NewRequest("POST", "/v1/packs", strings.NewReader(string(b)))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestRulePackSignatureValidAccepted(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, h := newSigningServer(t, map[string]ed25519.PublicKey{"board-2026": pub}, true)
	w := postPack(t, h, signedPackBody(t, pub, priv, "board-2026"))
	if w.Code != http.StatusCreated && w.Code != http.StatusOK {
		t.Fatalf("valid signed pack must be accepted, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRulePackSignatureTamperedRejected(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	_, h := newSigningServer(t, map[string]ed25519.PublicKey{"board-2026": pub}, true)
	pack := signedPackBody(t, pub, priv, "board-2026")
	// tamper AFTER signing: flip the rate — signature must no longer verify.
	pack["rules"] = []any{map[string]any{"id": "test.rule", "when": map[string]any{"a": 1}, "then": map[string]any{"rate_bps": 99999}}}
	w := postPack(t, h, pack)
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "pack_signature_invalid") {
		t.Fatalf("tampered pack must be rejected 422 pack_signature_invalid, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRulePackSignatureUnpinnedKeyRejected(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	otherPub, _, _ := ed25519.GenerateKey(rand.Reader)
	_, h := newSigningServer(t, map[string]ed25519.PublicKey{"board-2026": otherPub}, true)
	w := postPack(t, h, signedPackBody(t, pub, priv, "rogue-key"))
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unpinned key_id must be rejected, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRulePackUnsignedPublishedRejectedInProd(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	_, h := newSigningServer(t, map[string]ed25519.PublicKey{"board-2026": pub}, true)
	pack := signedPackBody(t, pub, nil, "x")
	delete(pack, "signed")
	w := postPack(t, h, pack)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unsigned published pack in prod must be rejected, got %d: %s", w.Code, w.Body.String())
	}
}

func TestParseSigningKeys(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	keys, err := rpschema.ParseSigningKeys(`{"board-2026":"` + hex.EncodeToString(pub) + `"}`)
	if err != nil || len(keys) != 1 {
		t.Fatalf("valid env parse: %v %d", err, len(keys))
	}
	if _, err := rpschema.ParseSigningKeys(`{"k":"deadbeef"}`); err == nil {
		t.Fatal("short pubkey must be rejected")
	}
	if _, err := rpschema.ParseSigningKeys(`not-json`); err == nil {
		t.Fatal("malformed JSON must be rejected")
	}
}
