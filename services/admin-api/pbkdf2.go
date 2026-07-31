package main

// pbkdf2.go — password hashing with PBKDF2-SHA256, stdlib only (A6).
//
// golang.org/x/crypto/bcrypt cannot be vendored through the file-push
// pipeline (go.sum is CI-regenerated), so we implement PBKDF2 (RFC 2898)
// directly with HMAC-SHA256: 100,000 iterations, 16-byte random salt,
// 32-byte derived key, constant-time comparison.
//
// Encoded form: pbkdf2$<iterations>$<salt b64>$<key b64>

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

const (
	pbkdf2Iterations = 100_000
	pbkdf2SaltLen    = 16
	pbkdf2KeyLen     = 32
)

// pbkdf2SHA256 derives keyLen bytes from password+salt (RFC 2898, PRF=HMAC-SHA256).
func pbkdf2SHA256(password, salt []byte, iterations, keyLen int) []byte {
	out := make([]byte, 0, keyLen)
	for block := uint32(1); len(out) < keyLen; block++ {
		mac := hmac.New(sha256.New, password)
		mac.Write(salt)
		mac.Write([]byte{byte(block >> 24), byte(block >> 16), byte(block >> 8), byte(block)})
		u := mac.Sum(nil)
		t := make([]byte, len(u))
		copy(t, u)
		for i := 1; i < iterations; i++ {
			mac.Reset()
			mac.Write(u)
			u = mac.Sum(nil)
			for j := range t {
				t[j] ^= u[j]
			}
		}
		out = append(out, t...)
	}
	return out[:keyLen]
}

// HashPassword encodes a salted PBKDF2-SHA256 hash of pw.
func HashPassword(pw string) (string, error) {
	salt := make([]byte, pbkdf2SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	dk := pbkdf2SHA256([]byte(pw), salt, pbkdf2Iterations, pbkdf2KeyLen)
	return fmt.Sprintf("pbkdf2$%d$%s$%s",
		pbkdf2Iterations,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(dk)), nil
}

// MustHashPassword is HashPassword for seed data (panics only on RNG failure).
func MustHashPassword(pw string) string {
	h, err := HashPassword(pw)
	if err != nil {
		panic(err)
	}
	return h
}

// VerifyPassword checks pw against an encoded hash in constant time.
// Legacy plaintext values (pre-hardening dev seeds) are REJECTED — callers
// must migrate rows through HashPassword first.
func VerifyPassword(encoded, pw string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2" {
		return false
	}
	iters, err := strconv.Atoi(parts[1])
	if err != nil || iters < 1 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	got := pbkdf2SHA256([]byte(pw), salt, iters, len(want))
	return subtle.ConstantTimeCompare(got, want) == 1
}
