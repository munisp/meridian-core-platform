package main

import (
	"strings"
	"testing"
)

func TestHashAndVerify(t *testing.T) {
	h, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "pbkdf2$100000$") {
		t.Fatalf("unexpected encoding: %s", h)
	}
	if !VerifyPassword(h, "correct horse battery staple") {
		t.Fatal("correct password rejected")
	}
	if VerifyPassword(h, "wrong") {
		t.Fatal("wrong password accepted")
	}
	// salts differ between hashes of the same password
	h2, _ := HashPassword("correct horse battery staple")
	if h == h2 {
		t.Fatal("salt not randomised")
	}
	// legacy plaintext rejected
	if VerifyPassword("admin123", "admin123") {
		t.Fatal("plaintext value must not verify")
	}
	// malformed encodings rejected
	for _, bad := range []string{"", "pbkdf2$abc$x$y", "pbkdf2$1$!!$xx", "bcrypt$10$abc$def"} {
		if VerifyPassword(bad, "x") {
			t.Fatalf("malformed encoding verified: %q", bad)
		}
	}
}

func TestSeedUsersAreHashed(t *testing.T) {
	s := NewStore()
	for email, u := range s.Users {
		if u.Password != "" {
			t.Fatalf("user %s still holds a plaintext password", email)
		}
		if !strings.HasPrefix(u.PasswordHash, "pbkdf2$") {
			t.Fatalf("user %s missing PBKDF2 hash", email)
		}
	}
	if !VerifyPassword(s.Users["admin@meridian.local"].PasswordHash, "admin123") {
		t.Fatal("seeded admin hash does not verify against its documented dev password")
	}
}
