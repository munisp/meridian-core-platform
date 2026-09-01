//! telemetry.rs — tracing + OTLP bootstrap for geo-rs (otel-foundation wave).
//!
//! Contract: DESIGN-CONTRACT.md. Env vars match the Go otelx package:
//! OTEL_EXPORTER_OTLP_ENDPOINT (gRPC), OTEL_SERVICE_NAME / OTEL_SERVICE_VERSION,
//! DEPLOYMENT_ENVIRONMENT, PROFILE. Telemetry is best-effort: no endpoint =>
//! local fmt logging only (+ loud warning when PROFILE=prod); exporter
//! failure never panics startup.

use opentelemetry_otlp::WithExportConfig;
use tracing_subscriber::layer::SubscriberExt;
use tracing_subscriber::util::SubscriberInitExt;
use tracing_subscriber::EnvFilter;

pub struct TelemetryGuard {
    #[allow(dead_code)]
    provider: Option<opentelemetry_sdk::trace::SdkTracerProvider>,
}

impl Drop for TelemetryGuard {
    fn drop(&mut self) {
        if let Some(p) = &self.provider {
            let _ = p.shutdown();
        }
    }
}

fn env_or(keys: &[&str], default: &str) -> String {
    for k in keys {
        if let Ok(v) = std::env::var(k) {
            if !v.is_empty() {
                return v;
            }
        }
    }
    default.to_string()
}

/// Initialise tracing. Always succeeds: on any exporter error we log and
/// fall back to fmt-only.
pub fn init_telemetry() -> TelemetryGuard {
    let endpoint = std::env::var("OTEL_EXPORTER_OTLP_ENDPOINT").unwrap_or_default();
    let service = env_or(&["OTEL_SERVICE_NAME", "SERVICE_NAME"], "geo-rs");
    let profile = std::env::var("PROFILE").unwrap_or_else(|_| "dev".into());

    let filter = EnvFilter::try_from_default_env().unwrap_or_else(|_| EnvFilter::new("info"));
    let fmt_layer = tracing_subscriber::fmt::layer().with_target(false);

    if endpoint.is_empty() {
        if profile == "prod" {
            eprintln!(
                "otel: WARNING PROFILE=prod but OTEL_EXPORTER_OTLP_ENDPOINT is unset; \
                 service={service} running with NO OTLP trace export"
            );
        }
        tracing_subscriber::registry()
            .with(filter)
            .with(fmt_layer)
            .init();
        return TelemetryGuard { provider: None };
    }

    let builder = opentelemetry_otlp::SpanExporter::builder()
        .with_tonic()
        .with_endpoint(endpoint.clone());
    match builder.build() {
        Ok(exporter) => {
            use opentelemetry::trace::TracerProvider as _;
            let provider = opentelemetry_sdk::trace::SdkTracerProvider::builder()
                .with_batch_exporter(exporter)
                .with_resource(
                    opentelemetry_sdk::Resource::builder()
                        .with_service_name(service.clone())
                        .with_attribute(opentelemetry::KeyValue::new(
                            "service.version",
                            env_or(&["OTEL_SERVICE_VERSION", "SERVICE_VERSION"], "0.1.0"),
                        ))
                        .with_attribute(opentelemetry::KeyValue::new(
                            "deployment.environment",
                            env_or(&["DEPLOYMENT_ENVIRONMENT", "PROFILE"], "dev"),
                        ))
                        .build(),
                )
                .build();
            let tracer = provider.tracer(service);
            let otel_layer = tracing_opentelemetry::layer().with_tracer(tracer);
            tracing_subscriber::registry()
                .with(filter)
                .with(fmt_layer)
                .with(otel_layer)
                .init();
            TelemetryGuard {
                provider: Some(provider),
            }
        }
        Err(e) => {
            eprintln!("otel: OTLP exporter build failed ({e}); traces stay local-only");
            tracing_subscriber::registry()
                .with(filter)
                .with(fmt_layer)
                .init();
            TelemetryGuard { provider: None }
        }
    }
}

/// Tenant resolution parity with Go otelx.TenantFromRequest:
/// X-Meridian-Tenant, then X-Tenant-ID. (JWT claim decode omitted in Rust to
/// avoid an extra JSON/JWT dependency; headers are the platform-canonical path.)
pub fn tenant_from_headers(headers: &axum::http::HeaderMap) -> String {
    for name in ["x-meridian-tenant", "x-tenant-id"] {
        if let Some(v) = headers.get(name) {
            if let Ok(s) = v.to_str() {
                if !s.is_empty() {
                    return s.to_string();
                }
            }
        }
    }
    String::new()
}

/// Axum middleware: one tracing span per request carrying tenant.id and the
/// matched route (low cardinality). The OpenTelemetry layer turns this into
/// an OTLP span when telemetry is enabled.
pub async fn tenant_span_middleware(
    req: axum::extract::Request,
    next: axum::middleware::Next,
) -> axum::response::Response {
    let tenant = tenant_from_headers(req.headers());
    let method = req.method().to_string();
    let path = req.uri().path().to_string();
    let span = tracing::info_span!(
        "http.request",
        http.method = %method,
        http.route = %path,
        otel.kind = "server",
        tenant.id = %tenant,
    );
    let _guard = span.enter();
    next.run(req).await
}

#[cfg(test)]
mod tests {
    use super::*;
    use axum::http::HeaderMap;

    #[test]
    fn tenant_header_priority() {
        let mut h = HeaderMap::new();
        assert_eq!(tenant_from_headers(&h), "");
        h.insert("x-tenant-id", "legacy".parse().unwrap());
        assert_eq!(tenant_from_headers(&h), "legacy");
        h.insert("x-meridian-tenant", "canonical".parse().unwrap());
        assert_eq!(tenant_from_headers(&h), "canonical");
    }

    #[test]
    fn init_without_endpoint_is_noop_not_fatal() {
        std::env::remove_var("OTEL_EXPORTER_OTLP_ENDPOINT");
        std::env::set_var("PROFILE", "dev");
        let _g = init_telemetry(); // must not panic
    }
}
