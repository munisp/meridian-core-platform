package stepup

import (
	"strings"
	"testing"
	"time"
)

var testNow = time.Unix(1_800_000_000, 0)

func newTestService(t *testing.T) *Service {
	t.Helper()
	t.Setenv("STEPUP_SEAL_KEY", "test-seal-key")
	s, err := NewService(NewMemStore(), "dev", "MeridianTest")
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return testNow }
	return s
}

func TestTOTPRFC6238Vector(t *testing.T) {
	// RFC 6238 Appendix B test vector (SHA1, 8 digits). We compute 6-digit
	// truncation of the same HOTP value: 59s -> 94287082 -> "287082".
	code, err := Code("GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ", time.Unix(59, 0))
	if err != nil {
		t.Fatal(err)
	}
	if code != "287082" {
		t.Fatalf("RFC 6238 vector mismatch: got %s want 287082", code)
	}
}

func TestValidateWindow(t *testing.T) {
	secret, _ := GenerateSecret()
	now := time.Unix(1_800_000_000, 0)
	good, _ := Code(secret, now)
	if !Validate(secret, good, now) {
		t.Fatal("current-step code rejected")
	}
	prev, _ := Code(secret, now.Add(-30*time.Second))
	if !Validate(secret, prev, now) {
		t.Fatal("previous-step code (inside window) rejected")
	}
	outside, _ := Code(secret, now.Add(90*time.Second))
	if Validate(secret, outside, now) {
		t.Fatal("code outside ±1 window accepted")
	}
	if Validate(secret, "000000x", now) || Validate(secret, "12345", now) {
		t.Fatal("malformed code accepted")
	}
}

func TestEnrollConfirmVerify(t *testing.T) {
	s := newTestService(t)
	secret, uri, recovery, err := s.Enroll("admin-1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(uri, "otpauth://totp/MeridianTest:admin-1?") {
		t.Fatalf("bad uri %s", uri)
	}
	if len(recovery) != RecoveryCount {
		t.Fatalf("want %d recovery codes, got %d", RecoveryCount, len(recovery))
	}
	// sealed at rest: raw secret must not appear in the stored record
	rec, _ := s.Store.Get("admin-1")
	if strings.Contains(rec.SealedSecret, secret) {
		t.Fatal("plaintext secret persisted")
	}
	// not yet enabled
	code, _ := Code(secret, testNow)
	if err := s.VerifyCode("admin-1", code); err != ErrNotConfirmed {
		t.Fatalf("pre-confirm verify: %v", err)
	}
	if err := s.Confirm("admin-1", "000000"); err != ErrBadCode {
		t.Fatalf("bad confirm: %v", err)
	}
	if err := s.Confirm("admin-1", code); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if !s.Enrolled("admin-1") {
		t.Fatal("not enrolled after confirm")
	}
	if err := s.VerifyCode("admin-1", code); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

func TestRecoveryCodeSingleUse(t *testing.T) {
	s := newTestService(t)
	secret, _, recovery, err := s.Enroll("admin-2")
	if err != nil {
		t.Fatal(err)
	}
	code, _ := Code(secret, testNow)
	if err := s.Confirm("admin-2", code); err != nil {
		t.Fatal(err)
	}
	if err := s.VerifyCode("admin-2", recovery[0]); err != nil {
		t.Fatalf("first recovery use: %v", err)
	}
	if err := s.VerifyCode("admin-2", recovery[0]); err != ErrBadCode {
		t.Fatalf("recovery replay accepted: %v", err)
	}
	if err := s.VerifyCode("admin-2", recovery[1]); err != nil {
		t.Fatalf("second distinct recovery code: %v", err)
	}
}

func TestTicketMintVerifyReplay(t *testing.T) {
	store := NewMemStore()
	key := []byte("ticket-key")
	ph := HashPayload([]byte(`{"amount":1}`))
	tok, err := MintTicket(key, "admin-1", "ledger.post", ph, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyTicket(key, store, tok, "admin-1", "ledger.post", ph, testNow); err != nil {
		t.Fatalf("verify: %v", err)
	}
	// replay
	if _, err := VerifyTicket(key, store, tok, "admin-1", "ledger.post", ph, testNow); err != ErrReplay {
		t.Fatalf("replay: got %v want ErrReplay", err)
	}
	// wrong binding
	tok2, _ := MintTicket(key, "admin-1", "ledger.post", ph, testNow)
	if _, err := VerifyTicket(key, store, tok2, "admin-1", "ledger.void", ph, testNow); err != ErrTicketBinding {
		t.Fatalf("binding: %v", err)
	}
	// expired
	tok3, _ := MintTicket(key, "admin-1", "ledger.post", ph, testNow.Add(-10*time.Minute))
	if _, err := VerifyTicket(key, store, tok3, "admin-1", "ledger.post", ph, testNow); err != ErrTicketExpired {
		t.Fatalf("expiry: %v", err)
	}
	// wrong key
	tok4, _ := MintTicket(key, "admin-1", "ledger.post", ph, testNow)
	if _, err := VerifyTicket([]byte("other"), store, tok4, "admin-1", "ledger.post", ph, testNow); err != ErrTicketSignature {
		t.Fatalf("signature: %v", err)
	}
}

func TestSealerRoundTripAndProdGate(t *testing.T) {
	t.Setenv("STEPUP_SEAL_KEY", "k")
	s, err := NewSealer("prod")
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := s.Seal("ABCSECRET")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sealed, "ABCSECRET") {
		t.Fatal("seal leaks plaintext")
	}
	pt, err := s.Open(sealed)
	if err != nil || pt != "ABCSECRET" {
		t.Fatalf("open: %v %q", err, pt)
	}
	// prod without key fails closed
	t.Setenv("STEPUP_SEAL_KEY", "")
	if _, err := NewSealer("prod"); err == nil {
		t.Fatal("prod without STEPUP_SEAL_KEY did not fail closed")
	}
	if _, err := NewSealer("dev"); err != nil {
		t.Fatalf("dev fallback: %v", err)
	}
}
