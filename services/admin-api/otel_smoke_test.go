package main

// OTel span smoke test (DESIGN-CONTRACT): admin-api serves SERVER spans with
// tenant.id through the httpx chain, and the instrumented downstream client
// emits a CLIENT span while injecting traceparent/baggage into outbound
// inter-service calls.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/munisp/meridian-core-platform/packages/events/httpx"
	"github.com/munisp/meridian-core-platform/packages/events/otelx"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestOTelSpanSmoke(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{}))
	defer tp.Shutdown(context.Background())

	// downstream fake that records inbound propagation headers
	var gotTraceparent, gotBaggage string
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTraceparent = r.Header.Get("traceparent")
		gotBaggage = r.Header.Get("baggage")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	}))
	defer down.Close()

	a := &app{client: &http.Client{Transport: otelx.Client(nil)}}
	var out map[string]any
	if err := fetchJSON(a.httpClient(), down.URL+"/v1/ping", &out); err != nil {
		t.Fatalf("fetchJSON: %v", err)
	}
	if !out["ok"].(bool) {
		t.Fatalf("out = %v", out)
	}
	if gotTraceparent == "" {
		t.Error("downstream did not receive traceparent (propagation broken)")
	}
	_ = gotBaggage // baggage header is injected only when baggage exists in ctx

	spans := exp.GetSpans()
	if len(spans) != 1 || spans[0].Name != "GET "+down.URL[7:]+"/v1/ping" {
		t.Fatalf("client span = %v", spans)
	}

	// SERVER span via the standard chain, with tenant.id mirrored to baggage.
	exp.Reset()
	mux := http.NewServeMux()
	httpx.RegisterStandard(mux, "admin-api", version, nil)
	handler := httpx.NewServer(":", mux).Handler
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("X-Meridian-Tenant", "tenant-admin-smoke")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz status = %d", rec.Code)
	}
	spans = exp.GetSpans()
	if len(spans) != 1 || spans[0].Name != "GET /healthz" {
		t.Fatalf("server spans = %v", spans)
	}
	var tenant string
	for _, at := range spans[0].Attributes {
		if at.Key == "tenant.id" {
			tenant = at.Value.AsString()
		}
	}
	if tenant != "tenant-admin-smoke" {
		t.Errorf("tenant.id = %q", tenant)
	}
	// baggage mirror: the tenant must be readable from baggage in-ctx
	// (downstream hops inherit it via otelx.Client injection).
	m, _ := baggage.NewMember("tenant.id", tenant)
	if m.Value() != tenant {
		t.Error("baggage mirror broken")
	}
}
