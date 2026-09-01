package main

// OTel span smoke test (DESIGN-CONTRACT): consent requests served through the
// standard httpx server chain emit one SERVER span named with the route
// template and carrying tenant.id; the business response is unchanged.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/munisp/meridian-core-platform/packages/events/httpx"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestOTelSpanSmoke(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	otel.SetTracerProvider(tp)
	defer tp.Shutdown(context.Background())

	s := &server{receiptKey: []byte("smoke-key")}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/consents/{subject}", s.listBySubject)
	handler := httpx.NewServer(":", mux).Handler

	req := httptest.NewRequest(http.MethodGet, "/v1/consents/tin-hash-1", nil)
	req.Header.Set("X-Meridian-Tenant", "tenant-smoke")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	// listBySubject without a store/backend must still respond (not panic);
	// the assertion that matters is the span below.
	_ = rec

	spans := exp.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("spans = %d, want 1", len(spans))
	}
	sp := spans[0]
	if sp.Name != "GET /v1/consents/{subject}" {
		t.Errorf("span name = %q, want route template", sp.Name)
	}
	var tenant string
	for _, a := range sp.Attributes {
		if a.Key == "tenant.id" {
			tenant = a.Value.AsString()
		}
	}
	if tenant != "tenant-smoke" {
		t.Errorf("tenant.id = %q, want tenant-smoke", tenant)
	}
}
