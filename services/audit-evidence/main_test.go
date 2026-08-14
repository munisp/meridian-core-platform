package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/munisp/meridian-core-platform/packages/events/auth"
	"github.com/munisp/meridian-core-platform/services/audit-evidence/internal/evidence"
)

func newTestServer(t *testing.T) *server {
	t.Helper()
	dir := t.TempDir()
	al, err := evidence.OpenAuditLog(dir, []byte("test-chain-key"))
	if err != nil {
		t.Fatalf("open audit log: %v", err)
	}
	return &server{al: al}
}

func tokenFor(t *testing.T, sub, tenant string) string {
	t.Helper()
	tok, err := auth.SignHS256(auth.Claims{Sub: sub, TenantID: tenant, Roles: []string{"operator"}}, time.Hour)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return tok
}

func doReq(t *testing.T, h http.Handler, method, path, tok, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// Regression: pre-fix, POST /v1/audit/events accepted a caller-supplied
// Actor (attribution forgery). Now a mismatched actor must be 403 and a
// matching/absent actor derives from the JWT principal.
func TestAppendEventRejectsSpoofedActor(t *testing.T) {
	s := newTestServer(t)
	h := auth.Middleware(http.HandlerFunc(s.appendEvent))
	tok := tokenFor(t, "alice", "tenant-a")

	// spoofed actor → 403 (pre-fix: 201 with forged attribution)
	rec := doReq(t, h, "POST", "/v1/audit/events", tok,
		`{"actor":"mallory","subject":"case-1","action":"str.file"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("spoofed actor: want 403, got %d body=%s", rec.Code, rec.Body.String())
	}

	// omitted actor → derived from principal, 201
	rec = doReq(t, h, "POST", "/v1/audit/events", tok,
		`{"subject":"case-1","action":"str.file"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("honest append: want 201, got %d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Event evidence.AuditEvent `json:"event"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Event.Actor != "alice" {
		t.Fatalf("actor not derived from principal: %q", out.Event.Actor)
	}

	// matching actor → accepted
	rec = doReq(t, h, "POST", "/v1/audit/events", tok,
		`{"actor":"alice","subject":"case-2","action":"str.file"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("matching actor: want 201, got %d", rec.Code)
	}
}

// Regression: pre-fix, GET /v1/audit/events had no tenant scoping (cross-
// tenant audit read). Now a token only sees its own tenant's events.
func TestQueryEventsTenantScoped(t *testing.T) {
	s := newTestServer(t)
	appendH := auth.Middleware(http.HandlerFunc(s.appendEvent))
	queryH := auth.Middleware(http.HandlerFunc(s.queryEvents))

	for _, tenant := range []string{"tenant-a", "tenant-b"} {
		rec := doReq(t, appendH, "POST", "/v1/audit/events", tokenFor(t, "svc-"+tenant, tenant),
			`{"subject":"case","action":"act"}`)
		if rec.Code != http.StatusCreated {
			t.Fatalf("seed %s: %d", tenant, rec.Code)
		}
	}

	// tenant-a sees exactly its own event (pre-fix: count=2, both tenants)
	rec := doReq(t, queryH, "GET", "/v1/audit/events", tokenFor(t, "alice", "tenant-a"), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("query: %d", rec.Code)
	}
	var out struct {
		Events []evidence.AuditEvent `json:"events"`
		Count  int                   `json:"count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Count != 1 || out.Events[0].TenantID != "tenant-a" {
		b, _ := json.Marshal(out)
		t.Fatalf("cross-tenant leak: %s", b)
	}

	// token without tenant claim → 403
	rec = doReq(t, queryH, "GET", "/v1/audit/events", tokenFor(t, "alice", ""), "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("tenantless token: want 403, got %d", rec.Code)
	}
}

var _ = bytes.NewReader // keep bytes import if unused by future edits
