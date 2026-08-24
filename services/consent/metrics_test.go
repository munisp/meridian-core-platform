package main

// metrics_test.go — service-level /metrics regression: the consent business
// handler increments the request counters, and the scrape exposes them.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/munisp/meridian-core-platform/packages/events/auth"
	"github.com/munisp/meridian-core-platform/packages/events/httpx"
	"github.com/munisp/meridian-core-platform/packages/events/store"
)

func TestBusinessHandlerIncrementsMetrics(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := &server{st: st, receiptKey: []byte("test-key")}
	reg := &httpx.MetricsRegistry{}
	mux := http.NewServeMux()
	// B2-#14: create is subject-bound, so the request must carry a principal.
	mux.Handle("POST /v1/consents", auth.Middleware(httpx.InstrumentRegistry(reg, http.HandlerFunc(s.create))))

	body := `{"subject":"tin-hash-9","purpose":"direct tax","lawful_basis":"consent"}`
	req := httptest.NewRequest("POST", "/v1/consents", strings.NewReader(body))
	req.Header.Set("Authorization", bearerFor(t, auth.Claims{Sub: "tin-hash-9"}))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	scrape := httptest.NewRecorder()
	reg.Handler().ServeHTTP(scrape, httptest.NewRequest("GET", "/metrics", nil))
	out := scrape.Body.String()
	if !strings.Contains(out, `meridian_http_requests_total{service="",route="POST /v1/consents",method="POST",status="201"} 1`) {
		t.Fatalf("consent create counter missing from scrape:\n%s", out)
	}
}
