package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Dev mode: creating a user without a password generates a one-off password
// and flags force_password_reset (no shared default credential).
func TestUserCreateDevGeneratesPassword(t *testing.T) {
	a := &app{store: NewStore(), authMode: "dev"}
	body := `{"email":"new@meridian.local","name":"New User"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/users", strings.NewReader(body))
	rec := httptest.NewRecorder()
	a.handleUserCreate(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	u := a.store.Users["new@meridian.local"]
	if u == nil {
		t.Fatal("user not stored")
	}
	if !u.ForcePasswordReset {
		t.Fatal("expected ForcePasswordReset to be set")
	}
	if u.PasswordHash == "" || strings.Contains(u.PasswordHash, "changeme123") {
		t.Fatalf("unexpected password hash: %q", u.PasswordHash)
	}
	if u.Password != "" {
		t.Fatal("plaintext password must not be retained")
	}
	// two generated passwords must differ (no shared default)
	a2 := &app{store: NewStore(), authMode: "dev"}
	req2 := httptest.NewRequest(http.MethodPost, "/v1/admin/users",
		strings.NewReader(`{"email":"other@meridian.local","name":"Other"}`))
	a2.handleUserCreate(httptest.NewRecorder(), req2)
	if a.store.Users["new@meridian.local"].PasswordHash == a2.store.Users["other@meridian.local"].PasswordHash {
		t.Fatal("generated passwords must be unique per user")
	}
}

// Prod mode: creating a user without an explicit password is rejected.
func TestUserCreateProdRequiresPassword(t *testing.T) {
	a := &app{store: NewStore(), authMode: "keycloak"}
	body := `{"email":"new@meridian.local","name":"New User"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/users", strings.NewReader(body))
	rec := httptest.NewRecorder()
	a.handleUserCreate(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if _, ok := a.store.Users["new@meridian.local"]; ok {
		t.Fatal("user must not be created without a password in prod")
	}
	var p problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("problem+json expected: %v", err)
	}
	if p.Title != "password required" {
		t.Fatalf("unexpected problem title: %q", p.Title)
	}
}

// Seed persona must not use a hardcoded known password.
func TestSeedPersonaPasswordNotHardcoded(t *testing.T) {
	s := NewStore()
	u := s.Users["amina@chambers.ng"]
	if u == nil {
		t.Fatal("seed persona missing")
	}
	if VerifyPassword(u.PasswordHash, "changeme123") {
		t.Fatal("seed persona must not keep the old changeme123 default")
	}
}
