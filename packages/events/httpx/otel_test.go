package httpx_test

// Span smoke test for the httpx OTel integration (DESIGN-CONTRACT.md): an
// in-memory exporter must observe one SERVER span named "<METHOD> <route>"
// carrying tenant.id, and the wrapped handler must behave identically when
// telemetry is disabled (fail-soft on money paths).

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/munisp/meridian-core-platform/packages/events/httpx"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	nooptrace "go.opentelemetry.io/otel/trace/noop"
)

func setupInMemory(t *testing.T) *tracetest.InMemoryExporter {
	t.Helper()
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		_ = tp.Shutdown(t.Context())
		otel.SetTracerProvider(nil)
	})
	return exp
}

func TestOTelMiddlewareServerSpan(t *testing.T) {
	exp := setupInMemory(t)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/ping/{id}", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, http.StatusOK, map[string]string{"ok": "1"})
	})
	srv := httptest.NewServer(httpx.NewServer(":", mux).Handler)
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/v1/ping/abc", nil)
	req.Header.Set("X-Meridian-Tenant", "tenant-42")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	spans := exp.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("spans = %d, want 1", len(spans))
	}
	s := spans[0]
	if s.Name != "GET /v1/ping/{id}" {
		t.Errorf("span name = %q, want route template", s.Name)
	}
	var tenant, route string
	for _, a := range s.Attributes {
		if a.Key == attribute.Key("tenant.id") {
			tenant = a.Value.AsString()
		}
		if a.Key == attribute.Key("http.route") {
			route = a.Value.AsString()
		}
	}
	if tenant != "tenant-42" {
		t.Errorf("tenant.id = %q, want tenant-42", tenant)
	}
	if route != "/v1/ping/{id}" {
		t.Errorf("http.route = %q", route)
	}
}

func TestOTelMiddlewareDisabledPassthrough(t *testing.T) {
	// Disabled mode (no-op global provider, as otelx installs when no OTLP
	// endpoint is configured): handler must still serve.
	otel.SetTracerProvider(nooptrace.NewTracerProvider())
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", httpx.Healthz("svc", "0.0.0"))
	rec := httptest.NewRecorder()
	httpx.NewServer(":", mux).Handler.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (telemetry must never block requests)", rec.Code)
	}
}
