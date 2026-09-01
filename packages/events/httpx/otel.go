package httpx

// otel.go — single integration point that puts every httpx-served service on
// the OTel foundation (DESIGN-CONTRACT.md): otelx.Middleware emits one SERVER
// span per request named "<METHOD> <route-template>" with tenant.id
// attribution and W3C tracecontext/baggage propagation. Fail-soft by
// construction: with telemetry disabled the wrapped handler is a no-op
// pass-through, so money paths never block on telemetry.

import (
	"net/http"

	"github.com/munisp/meridian-core-platform/packages/events/otelx"
)

// OTel wraps h with the shared OTel server-span middleware. It is installed
// by NewServer for every httpx service; services with custom http.Server
// construction should wrap their handler with OTel explicitly.
func OTel(h http.Handler) http.Handler {
	return otelx.Middleware(h)
}
