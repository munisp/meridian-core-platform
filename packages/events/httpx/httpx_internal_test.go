package httpx

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// R4: Internal must not leak internal error detail to the client — the
// response body carries only the generic title while the detail is logged.
func TestInternalDoesNotEchoDetail(t *testing.T) {
	secret := "pq: connection refused host=db.internal password=hunter2"
	rec := httptest.NewRecorder()
	Internal(rec, "%v", errors.New(secret))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status: %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, secret) || strings.Contains(body, "db.internal") {
		t.Fatalf("internal detail leaked to client: %s", body)
	}
	var p Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if p.Detail != "internal error" || p.Title != "internal_error" {
		t.Fatalf("unexpected problem body: %+v", p)
	}
}
