package main

// OTel span smoke test (DESIGN-CONTRACT): an in-memory exporter must observe
// a SERVER span named "<METHOD> <route-template>" carrying tenant.id for a
// ledger request, and the TigerBeetle client span must join the same trace
// (request-parented). Telemetry never changes the business response.
//
// R4 (otelx tenant-attribution hardening): tenant.id is stamped ONLY from a
// token verified by the platform verifier (otelx.TenantVerifier) — caller-
// controlled tenant headers are stripped at the server edge and never trusted.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/munisp/meridian-core-platform/packages/events/httpx"
	"github.com/munisp/meridian-core-platform/packages/events/otelx"
	"github.com/munisp/meridian-core-platform/services/ledger/internal/tb"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestOTelSpanSmoke(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	otel.SetTracerProvider(tp)
	defer tp.Shutdown(context.Background())

	// R4: tenant.id comes only from a VERIFIED token (stub verifier); the
	// spoofed tenant header below is stripped at the server edge.
	prev := otelx.TenantVerifier
	otelx.TenantVerifier = func(auth string) string {
		if auth == "Bearer stub-verified" {
			return "tenant-smoke"
		}
		return ""
	}
	defer func() { otelx.TenantVerifier = prev }()

	dev := tb.NewDevClient()
	s := &server{client: tb.Traced(dev), dev: dev, dir: t.TempDir(), thresh: newThresholdTracker()}
	handler := httpx.NewServer(":", s.routes()).Handler

	req := httptest.NewRequest(http.MethodGet, "/v1/transfers", nil)
	req.Header.Set("X-Meridian-Tenant", "attacker-spoofed")
	req.Header.Set("Authorization", "Bearer stub-verified")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (telemetry must not change responses)", rec.Code)
	}

	var server, tbSpan *tracetest.SpanStub
	for i, sp := range exp.GetSpans() {
		switch sp.Name {
		case "GET /v1/transfers":
			server = &exp.GetSpans()[i]
		case "tigerbeetle.list_transfers":
			tbSpan = &exp.GetSpans()[i]
		}
	}
	if server == nil {
		t.Fatalf("no server span; spans=%v", exp.GetSpans())
	}
	var tenant string
	for _, a := range server.Attributes {
		if a.Key == "tenant.id" {
			tenant = a.Value.AsString()
		}
	}
	if tenant != "tenant-smoke" {
		t.Errorf("tenant.id = %q, want tenant-smoke", tenant)
	}
	if tbSpan == nil {
		t.Fatal("no tigerbeetle.list_transfers span")
	}
	if tbSpan.Parent.TraceID() != server.SpanContext.TraceID() ||
		tbSpan.Parent.SpanID() != server.SpanContext.SpanID() {
		t.Errorf("TB span parent = %v, want request span %v", tbSpan.Parent, server.SpanContext)
	}
}
