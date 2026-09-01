package sdkx

// trace.go — OTel spans for workflow/activity execution (DESIGN-CONTRACT:
// Temporal workflow/activity spans). One INTERNAL span per workflow run and
// per activity invocation; saga steps nest as children of the caller's span.
// Fail-soft: spans never alter control flow or error semantics — with a
// no-op provider these are near-zero-cost pass-throughs.

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const sdkxTracerName = "github.com/munisp/meridian-core-platform/packages/temporal-sdkx"

// startSpan begins a span named "<kind>/<name>" (e.g. "workflow/CaptureSaga",
// "activity/PostCapture") parented to ctx.
func startSpan(ctx context.Context, kind, name string) (context.Context, trace.Span) {
	return otel.Tracer(sdkxTracerName).Start(ctx, kind+"/"+name,
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(
			attribute.String("temporal."+kind+".name", name),
		))
}

// endSpan records err (if any) and closes the span.
func endSpan(span trace.Span, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	span.End()
}
