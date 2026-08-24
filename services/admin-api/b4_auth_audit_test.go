package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// Regression: B4-5 — admin credential/role changes had no re-authentication
// and no session invalidation (12h stateless JWTs survived password/role
// change and deletion). B4-6 — admin audit writes were in-mem only and never
// reached the WORM audit-evidence service.

func probeWithToken(a *app, tok string) int {
	h := a.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/v1/admin/me", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func issueToken(t *testing.T, a *app, email string, iat int64) string {
	t.Helper()
	u := a.store.Users[email]
	if u == nil {
		t.Fatalf("user %s not seeded", email)
	}
	tok, err := a.issueJWT(claims{
		Sub: u.ID, Email: u.Email, Roles: u.Roles, TenantID: u.TenantID,
		Issuer: "admin-api-dev", IssuedAt: iat, Expires: time.Now().Unix() + 3600,
	})
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func updateUser(t *testing.T, a *app, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPatch, "/v1/admin/users/"+id, strings.NewReader(body))
	req.SetPathValue("id", id)
	rec := httptest.NewRecorder()
	a.handleUserUpdate(rec, req)
	return rec
}

func TestPasswordChangeRequiresCurrentPassword(t *testing.T) {
	a := &app{store: NewStore(), authMode: "dev"}
	u := a.store.Users["operator@meridian.local"] // seeded: operator123

	// no current password -> 403
	if rec := updateUser(t, a, u.ID, `{"password":"newpass123"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("no current password: want 403, got %d (%s)", rec.Code, rec.Body.String())
	}
	// wrong current password -> 403
	if rec := updateUser(t, a, u.ID, `{"password":"newpass123","current_password":"wrong"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("wrong current password: want 403, got %d", rec.Code)
	}
	if VerifyPassword(a.store.Users["operator@meridian.local"].PasswordHash, "newpass123") {
		t.Fatal("password mutated despite failed current-password proof")
	}
	// correct current password -> 200, hash updated, epoch bumped
	if rec := updateUser(t, a, u.ID, `{"password":"newpass123","current_password":"operator123"}`); rec.Code != http.StatusOK {
		t.Fatalf("valid change: want 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	nu := a.store.Users["operator@meridian.local"]
	if !VerifyPassword(nu.PasswordHash, "newpass123") {
		t.Fatal("password not updated after valid proof")
	}
	if nu.MinTokenIAT == 0 {
		t.Fatal("session epoch not bumped on password change")
	}
}

func TestOldTokenDiesAfterPasswordChange(t *testing.T) {
	a := &app{store: NewStore(), authMode: "dev"}
	tok := issueToken(t, a, "operator@meridian.local", time.Now().Unix()-3600)
	if code := probeWithToken(a, tok); code != http.StatusOK {
		t.Fatalf("pre-change token: want 200, got %d", code)
	}
	u := a.store.Users["operator@meridian.local"]
	if rec := updateUser(t, a, u.ID, `{"password":"rotated123","current_password":"operator123"}`); rec.Code != http.StatusOK {
		t.Fatalf("password change: got %d (%s)", rec.Code, rec.Body.String())
	}
	if code := probeWithToken(a, tok); code != http.StatusUnauthorized {
		t.Fatalf("post-change old token: want 401, got %d", code)
	}
}

func TestRoleStatusChangeAndDeletionInvalidateSessions(t *testing.T) {
	a := &app{store: NewStore(), authMode: "dev"}
	tok := issueToken(t, a, "auditor@meridian.local", time.Now().Unix()-3600)
	if code := probeWithToken(a, tok); code != http.StatusOK {
		t.Fatalf("pre-change token: want 200, got %d", code)
	}
	u := a.store.Users["auditor@meridian.local"]
	// role change kills the session
	if rec := updateUser(t, a, u.ID, `{"roles":["operator"]}`); rec.Code != http.StatusOK {
		t.Fatalf("role change: got %d", rec.Code)
	}
	if code := probeWithToken(a, tok); code != http.StatusUnauthorized {
		t.Fatalf("post-role-change token: want 401, got %d", code)
	}
	// a freshly minted token for the same (still active) user works
	tok2 := issueToken(t, a, "auditor@meridian.local", time.Now().Unix())
	if code := probeWithToken(a, tok2); code != http.StatusOK {
		t.Fatalf("fresh token: want 200, got %d", code)
	}
	// disabling kills sessions
	if rec := updateUser(t, a, u.ID, `{"status":"disabled"}`); rec.Code != http.StatusOK {
		t.Fatalf("disable: got %d", rec.Code)
	}
	if code := probeWithToken(a, tok2); code != http.StatusUnauthorized {
		t.Fatalf("disabled-user token: want 401, got %d", code)
	}
	// deletion kills sessions
	tok3 := issueToken(t, a, "amina@chambers.ng", time.Now().Unix())
	delete(a.store.Users, "amina@chambers.ng")
	if code := probeWithToken(a, tok3); code != http.StatusUnauthorized {
		t.Fatalf("deleted-user token: want 401, got %d", code)
	}
}

// --- B4-6: audit forwarding to WORM audit-evidence ---

type wormCapture struct {
	mu     sync.Mutex
	events []map[string]any
}

func (w *wormCapture) serve(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audit/events" || r.Method != http.MethodPost {
			rw.WriteHeader(http.StatusNotFound)
			return
		}
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			rw.WriteHeader(http.StatusBadRequest)
			return
		}
		w.mu.Lock()
		w.events = append(w.events, m)
		w.mu.Unlock()
		rw.WriteHeader(http.StatusCreated)
	}))
}

func TestAuditWriteForwardedToWORM(t *testing.T) {
	cap := &wormCapture{}
	ts := cap.serve(t)
	defer ts.Close()
	a := &app{store: NewStore(), authMode: "dev"}
	a.store.Services["audit-evidence"] = &ServiceEntry{BaseURL: ts.URL}

	a.appendAudit("user.updated", "user:victim@x.ng", "admin@meridian.local", "update", "role change")

	if a.auditQueueDepth() != 0 {
		t.Fatal("reachable WORM sink must not queue")
	}
	if len(cap.events) != 1 {
		t.Fatalf("WORM sink received %d events, want 1", len(cap.events))
	}
	ev := cap.events[0]
	if ev["subject"] != "user:victim@x.ng" || ev["action"] != "update" || ev["type"] != "user.updated" {
		t.Fatalf("unexpected forwarded event: %+v", ev)
	}
	details, _ := ev["details"].(map[string]any)
	if details["actor"] != "admin@meridian.local" {
		t.Fatalf("human actor not preserved in details: %+v", details)
	}
}

func TestAuditForwardFailureQueuesThenFlushes(t *testing.T) {
	a := &app{store: NewStore(), authMode: "dev"}
	a.store.Services["audit-evidence"] = &ServiceEntry{BaseURL: "http://127.0.0.1:1"} // refused
	a.appendAudit("gate.flipped", "gate:G8", "admin@meridian.local", "flip", "open")
	if a.auditQueueDepth() != 1 {
		t.Fatalf("unreachable WORM sink: want queue depth 1, got %d", a.auditQueueDepth())
	}
	// sink recovers -> flush delivers the queued event
	cap := &wormCapture{}
	ts := cap.serve(t)
	defer ts.Close()
	a.store.Services["audit-evidence"] = &ServiceEntry{BaseURL: ts.URL}
	a.flushAuditQueue()
	if a.auditQueueDepth() != 0 {
		t.Fatalf("queue not drained: %d", a.auditQueueDepth())
	}
	if len(cap.events) != 1 || cap.events[0]["subject"] != "gate:G8" {
		t.Fatalf("queued event not delivered: %+v", cap.events)
	}
}

func TestProdRefusesInMemoryOnlyAudit(t *testing.T) {
	t.Setenv("PROFILE", "prod")
	t.Setenv("KEYCLOAK_AUDIENCE", "meridian")
	if err := validateAuthConfig("keycloak", "non-default-secret"); err == nil ||
		!strings.Contains(err.Error(), "AUDIT_EVIDENCE_URL") {
		t.Fatalf("prod without AUDIT_EVIDENCE_URL must be refused, got %v", err)
	}
	t.Setenv("AUDIT_EVIDENCE_URL", "https://audit-evidence.internal")
	if err := validateAuthConfig("keycloak", "non-default-secret"); err != nil {
		t.Fatalf("prod with AUDIT_EVIDENCE_URL must pass, got %v", err)
	}
}

// V2 repair: with 1s granularity, `IssuedAt >= MinTokenIAT` let a token
// minted in the SAME unix second as the credential change survive
// revocation. The comparison is now strict (`>`); issuance guarantees a
// fresh login token always satisfies it (iat = max(now, MinTokenIAT+1)).
func TestSameSecondTokenDiesAfterEpochBump(t *testing.T) {
	a := &app{store: NewStore(), authMode: "dev"}
	u := a.store.Users["operator@meridian.local"]
	now := time.Now().Unix()
	// Token minted in the SAME second as the epoch bump must die.
	a.store.mu.Lock()
	u.MinTokenIAT = now
	a.store.mu.Unlock()
	if code := probeWithToken(a, issueToken(t, a, "operator@meridian.local", now)); code != http.StatusUnauthorized {
		t.Fatalf("same-second token = %d, want 401 (strict > epoch)", code)
	}
	// A strictly-later token survives.
	if code := probeWithToken(a, issueToken(t, a, "operator@meridian.local", now+1)); code != http.StatusOK {
		t.Fatalf("later token = %d, want 200", code)
	}
}
