package tb

// Span smoke test for the TigerBeetle tracing decorator: an in-memory
// exporter must observe one CLIENT span per call, request-parented when
// WithContext is used, and business results must pass through unchanged
// (telemetry is fail-soft on money paths).

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func setupSpans(t *testing.T) *tracetest.InMemoryExporter {
	t.Helper()
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	return exp
}

func TestTracedClientSpansAndPassthrough(t *testing.T) {
	exp := setupSpans(t)

	dev := NewDevClient()
	c := Traced(dev)
	res, err := c.CreateAccounts([]Account{{ID: MakeID(100, 1), Ledger: 100, Code: 1}})
	if err != nil || res[0].Code != OK {
		t.Fatalf("CreateAccounts = %v, %v", res, err)
	}
	tr := Transfer{ID: MakeID(100, 999), DebitAccountID: MakeID(100, 1), CreditAccountID: MakeID(100, 1),
		Amount: 1, Ledger: 100, Code: CodeTopup}
	// Same-account transfer is rejected by business rules; the point is the
	// result passes through the decorator unchanged.
	if _, err := c.Transfer(tr); err != nil {
		t.Fatalf("Transfer err: %v", err)
	}

	spans := exp.GetSpans()
	if len(spans) != 2 {
		t.Fatalf("spans = %d, want 2", len(spans))
	}
	names := map[string]bool{}
	for _, s := range spans {
		names[s.Name] = true
		if s.SpanKind != trace.SpanKindClient {
			t.Errorf("span %q kind = %v, want CLIENT", s.Name, s.SpanKind)
		}
	}
	if !names["tigerbeetle.create_accounts"] || !names["tigerbeetle.transfer"] {
		t.Errorf("span names = %v", names)
	}
}

func TestWithContextParentsSpans(t *testing.T) {
	exp := setupSpans(t)
	tracer := otel.Tracer("test")

	ctx, parent := tracer.Start(context.Background(), "request")
	c := WithContext(ctx, NewDevClient())
	if _, err := c.CreateAccounts([]Account{{ID: MakeID(100, 2), Ledger: 100, Code: 1}}); err != nil {
		t.Fatalf("CreateAccounts: %v", err)
	}
	parent.End()

	spans := exp.GetSpans()
	if len(spans) != 2 {
		t.Fatalf("spans = %d, want 2", len(spans))
	}
	var child, req tracetest.SpanStub
	for _, s := range spans {
		if s.Name == "request" {
			req = s
		} else {
			child = s
		}
	}
	if child.Parent.TraceID() != req.SpanContext.TraceID() || !child.Parent.IsValid() {
		t.Errorf("TB span not parented to request span: parent=%v", child.Parent)
	}
}

func TestTracedNil(t *testing.T) {
	if Traced(nil) != nil {
		t.Error("Traced(nil) must return nil")
	}
}
