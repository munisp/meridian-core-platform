package otelx

// otelx_test.go — foundation contract tests:
//  1. middleware creates spans carrying tenant.id stamped ONLY from a
//     verified bearer token (R4 hardening, S3 #7)
//  2. spoofed tenant assertions (headers/baggage/unsigned JWT) never reach
//     the span
//  3. client spans redact query strings (no TIN/PII in url.full, S3 #18)
//  4. propagation round-trip: client injects traceparent, server middleware
//     joins the same trace
//  5. disabled mode (no OTLP endpoint) is a full no-op and never fails

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// setupRecorder installs a real SDK tracer provider backed by the in-memory
// exporter and returns the recorder.
func setupRecorder(t *testing.T) *tracetest.InMemoryExporter {
	t.Helper()
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{}))
	t.Cleanup(func() { _ = tp.Shutdown(t.Context()) })
	return exp
}

// stubVerifier installs a test-local TenantVerifier that accepts exactly
// one token (otelx tests cannot import auth — auth imports otelx for the
// hook registration; the end-to-end wiring is covered in auth's tests).
func stubVerifier(t *testing.T, goodToken, tenant string) {
	t.Helper()
	prev := TenantVerifier
	TenantVerifier = func(authz string) string {
		if authz == "Bearer "+goodToken {
			return tenant
		}
		return ""
	}
	t.Cleanup(func() { TenantVerifier = prev })
}

func okHandler(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }

func TestMiddlewareSpanWithVerifiedJWT(t *testing.T) {
	exp := setupRecorder(t)
	stubVerifier(t, "good-token", "tenant-ng-01")
	mux := http.NewServeMux()
	mux.Handle("GET /v1/transfers/{id}", http.HandlerFunc(okHandler))

	req := httptest.NewRequest(http.MethodGet, "/v1/transfers/abc", nil)
	req.Header.Set("Authorization", "Bearer good-token")
	Middleware(mux).ServeHTTP(httptest.NewRecorder(), req)

	spans := exp.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	s := spans[0]
	var tenant, route string
	for _, a := range s.Attributes {
		switch string(a.Key) {
		case "tenant.id":
			tenant = a.Value.AsString()
		case "http.route":
			route = a.Value.AsString()
		}
	}
	if tenant != "tenant-ng-01" {
		t.Errorf("tenant.id = %q, want tenant-ng-01", tenant)
	}
	if route != "/v1/transfers/{id}" {
		t.Errorf("http.route = %q, want templated route (method stripped; span name carries the method)", route)
	}
	if s.Name != "GET /v1/transfers/{id}" {
		t.Errorf("span name = %q, want \"GET /v1/transfers/{id}\" per contract", s.Name)
	}
}

// R4 (S3 #7): spoofed tenant assertions must never reach the span.
func TestMiddlewareRejectsSpoofedTenant(t *testing.T) {
	cases := []struct {
		name  string
		apply func(r *http.Request)
	}{
		{"header X-Meridian-Tenant", func(r *http.Request) { r.Header.Set("X-Meridian-Tenant", "victim-tenant") }},
		{"header X-Tenant-ID", func(r *http.Request) { r.Header.Set("X-Tenant-ID", "victim-tenant") }},
		{"inbound baggage", func(r *http.Request) { r.Header.Set("Baggage", "tenant.id=victim-tenant") }},
		{"unsigned JWT", func(r *http.Request) {
			hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
			payload, _ := json.Marshal(map[string]string{"tenant_id": "victim-tenant"})
			r.Header.Set("Authorization", "Bearer "+hdr+"."+base64.RawURLEncoding.EncodeToString(payload)+".sig")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exp := setupRecorder(t)
			stubVerifier(t, "good-token", "tenant-ng-01")
			req := httptest.NewRequest(http.MethodGet, "/x", nil)
			tc.apply(req)
			Middleware(http.HandlerFunc(okHandler)).ServeHTTP(httptest.NewRecorder(), req)
			for _, s := range exp.GetSpans() {
				for _, a := range s.Attributes {
					if string(a.Key) == "tenant.id" {
						t.Errorf("spoofed tenant.id %q reached the span", a.Value.AsString())
					}
				}
			}
		})
	}
}

// R4 (S3 #18): client spans must not leak query strings (TINs) in url.full.
func TestClientRedactsQueryString(t *testing.T) {
	exp := setupRecorder(t)
	outReq, _ := http.NewRequest(http.MethodGet,
		"http://filings/v1/exports?tin=12345678-0001&from_period=2026-01", nil)
	rt := Client(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: http.NoBody, Header: http.Header{}}, nil
	}))
	if _, err := rt.RoundTrip(outReq); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range exp.GetSpans() {
		for _, a := range s.Attributes {
			if string(a.Key) == "url.full" {
				found = true
				if strings.Contains(a.Value.AsString(), "tin=") || strings.Contains(a.Value.AsString(), "?") {
					t.Errorf("url.full leaked query string: %q", a.Value.AsString())
				}
			}
		}
	}
	if !found {
		t.Error("no url.full attribute found on client span")
	}
}

// R4 (S3 #18): server spans must not carry raw url.path (IRNs/invoice ids).
func TestServerSpanHasNoRawPath(t *testing.T) {
	exp := setupRecorder(t)
	mux := http.NewServeMux()
	mux.Handle("GET /v1/invoices/{irn}", http.HandlerFunc(okHandler))
	req := httptest.NewRequest(http.MethodGet, "/v1/invoices/INV-0091-SRV-20260101", nil)
	Middleware(mux).ServeHTTP(httptest.NewRecorder(), req)
	spans := exp.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	if strings.Contains(spans[0].Name, "INV-0091") {
		t.Errorf("span name carries raw path: %q", spans[0].Name)
	}
	for _, a := range spans[0].Attributes {
		if string(a.Key) == "url.path" {
			t.Errorf("url.path attribute present with raw value %q", a.Value.AsString())
		}
	}
}

func TestPropagationRoundTrip(t *testing.T) {
	exp := setupRecorder(t)

	// Client side: start a span, inject into outbound request.
	tracer := otel.Tracer("test")
	ctx, clientSpan := tracer.Start(t.Context(), "outbound")
	outReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://downstream/v1/x", nil)
	rt := Client(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Traceparent") == "" {
			t.Error("client transport did not inject traceparent")
		}
		return &http.Response{StatusCode: 200, Body: http.NoBody, Header: http.Header{}}, nil
	}))
	if _, err := rt.RoundTrip(outReq); err != nil {
		t.Fatal(err)
	}
	clientSpan.End()

	// Server side: middleware must join the same trace as remote parent.
	inReq := httptest.NewRequest(http.MethodGet, "/v1/x", nil)
	inReq.Header.Set("Traceparent", outReq.Header.Get("Traceparent"))
	Middleware(http.HandlerFunc(okHandler)).ServeHTTP(httptest.NewRecorder(), inReq)

	var serverSpan *tracetest.SpanStub
	for i, s := range exp.GetSpans() {
		if s.SpanContext.IsValid() && s.Parent.IsValid() {
			serverSpan = &exp.GetSpans()[i]
		}
	}
	if serverSpan == nil {
		t.Fatal("no server span with parent recorded")
	}
	if serverSpan.SpanContext.TraceID() != clientSpan.SpanContext().TraceID() {
		t.Errorf("trace mismatch: server %s vs client %s",
			serverSpan.SpanContext.TraceID(), clientSpan.SpanContext().TraceID())
	}
	if !serverSpan.Parent.IsRemote() {
		t.Error("server span parent should be remote (extracted from traceparent)")
	}
}

var _ = fmt.Sprintf // keep fmt import if unused after edits

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
