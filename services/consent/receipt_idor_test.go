package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/munisp/meridian-core-platform/packages/events/auth"
)

// Regression (V2 round, B2-#14 residual): GET /v1/receipts/{id} had NO
// subject/role check — any authenticated caller could read any subject's
// consent receipt (IDOR). Now subject-self or admin only.

func receiptReq(t *testing.T, s *server, id string, c auth.Claims) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", "/v1/receipts/"+id, nil)
	req.SetPathValue("id", id)
	req.Header.Set("Authorization", bearerFor(t, c))
	rec := httptest.NewRecorder()
	auth.Middleware(http.HandlerFunc(s.getReceipt)).ServeHTTP(rec, req)
	return rec
}

func TestGetReceiptIDOR(t *testing.T) {
	s := newTestServer(t)
	rc := Receipt{ReceiptID: "rcp-a1", ConsentID: "c1", Subject: "tin-hash-A",
		Action: "granted", Time: "2024-01-01T00:00:00Z", Actor: "tin-hash-A"}
	if err := s.st.Put("receipts", rc.ReceiptID, rc); err != nil {
		t.Fatal(err)
	}

	// other subject -> 403
	if rec := receiptReq(t, s, "rcp-a1", auth.Claims{Sub: "tin-hash-B"}); rec.Code != http.StatusForbidden {
		t.Fatalf("cross-subject receipt read: want 403, got %d", rec.Code)
	}
	// auditor (non-admin) -> 403
	if rec := receiptReq(t, s, "rcp-a1", auth.Claims{Sub: "aud-1", Roles: []string{"auditor"}}); rec.Code != http.StatusForbidden {
		t.Fatalf("auditor receipt read: want 403, got %d", rec.Code)
	}
	// subject self -> 200
	if rec := receiptReq(t, s, "rcp-a1", auth.Claims{Sub: "tin-hash-A"}); rec.Code != http.StatusOK {
		t.Fatalf("subject receipt read: want 200, got %d", rec.Code)
	}
	// admin -> 200
	if rec := receiptReq(t, s, "rcp-a1", auth.Claims{Sub: "dpo-7", Roles: []string{"admin"}}); rec.Code != http.StatusOK {
		t.Fatalf("admin receipt read: want 200, got %d", rec.Code)
	}
}
