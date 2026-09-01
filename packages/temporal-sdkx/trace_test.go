package sdkx

// Span smoke tests for workflow/activity tracing (DESIGN-CONTRACT) using an
// in-memory exporter: workflow runs and activity calls emit spans, errors
// mark the span ERROR, and results/errors pass through unchanged.

import (
	"go.opentelemetry.io/otel/codes"
	"context"
	"errors"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func setupSpans(t *testing.T) *tracetest.InMemoryExporter {
	t.Helper()
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	return exp
}

func spanNames(exp *tracetest.InMemoryExporter) map[string]int {
	out := map[string]int{}
	for _, s := range exp.GetSpans() {
		out[s.Name]++
	}
	return out
}

func TestActivitySpan(t *testing.T) {
	exp := setupSpans(t)
	reg := NewActivityRegistry()
	reg.Register("PostCapture", func(ctx context.Context, in any) (any, error) { return "ok", nil })

	out, err := ExecuteActivity(context.Background(), reg, RetryPolicy{MaximumAttempts: 1}, "PostCapture", nil)
	if err != nil || out != "ok" {
		t.Fatalf("out=%v err=%v (results must pass through unchanged)", out, err)
	}
	if spanNames(exp)["activity/PostCapture"] != 1 {
		t.Errorf("spans = %v, want one activity/PostCapture", spanNames(exp))
	}
}

func TestActivitySpanErrorStatus(t *testing.T) {
	exp := setupSpans(t)
	reg := NewActivityRegistry()
	boom := errors.New("boom")
	reg.Register("Fail", func(ctx context.Context, in any) (any, error) { return nil, boom })

	if _, err := ExecuteActivity(context.Background(), reg, RetryPolicy{MaximumAttempts: 1}, "Fail", nil); err == nil {
		t.Fatal("expected error passthrough")
	}
	spans := exp.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("spans = %d", len(spans))
	}
	if spans[0].Status.Code != codes.Error {
		t.Errorf("status = %v, want ERROR", spans[0].Status)
	}
}

func TestWorkflowAndSagaSpans(t *testing.T) {
	exp := setupSpans(t)
	r := NewInprocRunner()
	saga := NewSaga(
		SagaStep{Name: "debit", Action: func(ctx context.Context, in any) (any, error) { return nil, nil }},
		SagaStep{Name: "credit", Action: func(ctx context.Context, in any) (any, error) { return nil, nil }},
	)
	r.RegisterWorkflow("CaptureSaga", func(ctx context.Context, in any) (any, error) {
		return nil, saga.Run(ctx, in)
	})

	if _, err := r.Execute(context.Background(), "CaptureSaga", nil); err != nil {
		t.Fatalf("execute: %v", err)
	}
	names := spanNames(exp)
	if names["workflow/CaptureSaga"] != 1 || names["saga_step/debit"] != 1 || names["saga_step/credit"] != 1 {
		t.Errorf("spans = %v", names)
	}
	// saga steps must be children of the workflow span (same trace).
	var wfTrace string
	for _, s := range exp.GetSpans() {
		if s.Name == "workflow/CaptureSaga" {
			wfTrace = s.SpanContext.TraceID().String()
		}
	}
	for _, s := range exp.GetSpans() {
		if s.SpanContext.TraceID().String() != wfTrace {
			t.Errorf("span %q in different trace", s.Name)
		}
	}
}
