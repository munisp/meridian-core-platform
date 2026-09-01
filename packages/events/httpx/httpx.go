// Package httpx provides shared HTTP service conventions (SPEC 1.3):
// JSON responses, RFC7807 problem+json errors, health endpoints, config.
package httpx

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"
)

// Version is set by services (build-time or const).
const DefaultVersion = "0.1.0"

// JSON writes v as JSON with the given status code.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// Problem is an RFC7807 problem detail.
type Problem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// Errorf writes an RFC7807 problem+json response.
func Errorf(w http.ResponseWriter, status int, title, format string, args ...any) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(Problem{
		Type:   "about:blank",
		Title:  title,
		Status: status,
		Detail: fmt.Sprintf(format, args...),
	})
}

// BadRequest / NotFound / Conflict / Internal helpers.
func BadRequest(w http.ResponseWriter, format string, args ...any) {
	Errorf(w, http.StatusBadRequest, "bad_request", format, args...)
}
func NotFound(w http.ResponseWriter, format string, args ...any) {
	Errorf(w, http.StatusNotFound, "not_found", format, args...)
}
func Conflict(w http.ResponseWriter, format string, args ...any) {
	Errorf(w, http.StatusConflict, "conflict", format, args...)
}
// Internal never echoes internal error detail to the client (assurance R4):
// the detail is logged server-side and the response body carries only the
// generic "internal error" title.
func Internal(w http.ResponseWriter, format string, args ...any) {
	log.Printf("internal_error: %s", fmt.Sprintf(format, args...))
	Errorf(w, http.StatusInternalServerError, "internal_error", "internal error")
}

// Decode reads a JSON request body into v, rejecting unknown fields loosely.
func Decode(r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	return dec.Decode(v)
}

// Healthz returns the standard health payload handler (SPEC 1.3).
func Healthz(service, version string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		JSON(w, http.StatusOK, map[string]string{
			"status": "ok", "service": service, "version": version,
		})
	}
}

// Readyz returns ok when check() is nil or returns nil error.
func Readyz(service, version string, check func() error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if check != nil {
			if err := check(); err != nil {
				Errorf(w, http.StatusServiceUnavailable, "not_ready", "%v", err)
				return
			}
		}
		JSON(w, http.StatusOK, map[string]string{
			"status": "ok", "service": service, "version": version,
		})
	}
}

// RegisterStandard mounts /healthz and /readyz on a mux.
func RegisterStandard(mux *http.ServeMux, service, version string, readyCheck func() error) {
	mux.Handle("GET /healthz", Healthz(service, version))
	mux.Handle("GET /readyz", Readyz(service, version, readyCheck))
}

// Env returns an env var or a default.
func Env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// ProfileLog emits the HARDENING H1 startup profile line:
// "profile=<dev|prod> component=<name> <detail>".
func ProfileLog(component, profile, format string, args ...any) {
	log.Printf("profile=%s component=%s %s", profile, component, fmt.Sprintf(format, args...))
}

// Port returns the configured PORT (default given).
func Port(def string) string { return Env("PORT", def) }

// ShutdownTimeout bounds graceful shutdown on SIGTERM/SIGINT (F-5).
const ShutdownTimeout = 15 * time.Second

// Serve runs srv until it fails or a SIGTERM/SIGINT arrives, then drains
// in-flight requests via http.Server.Shutdown bounded by ShutdownTimeout
// (F-5: SIGTERM must not hard-kill in-flight ledger transfers / outbox
// writes). A nil error (or http.ErrServerClosed) means a clean shutdown.
func Serve(srv *http.Server) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errCh := make(chan error, 1)
	go func() {
		log.Printf("listening on %s", srv.Addr)
		errCh <- srv.ListenAndServe()
	}()
	select {
	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	case <-ctx.Done():
		log.Printf("shutdown signal received; draining in-flight requests (timeout %s)", ShutdownTimeout)
		dctx, cancel := context.WithTimeout(context.Background(), ShutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(dctx); err != nil {
			return fmt.Errorf("graceful shutdown: %w", err)
		}
		return nil
	}
}

// NewServer builds the standard service server: OTel server spans (DESIGN-
// CONTRACT: one span per request, route-template name, tenant.id attribute),
// logging + panic recovery and full timeout defaults (F-5: ReadHeaderTimeout
// alone leaves body-read slowloris exposure). With no OTLP endpoint
// configured the otelx middleware is a pass-through on no-op providers, so
// behaviour is unchanged when telemetry is disabled.
func NewServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           Recover(Logging(OTel(h))),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
}

// ListenAndServe runs the server with logging, graceful shutdown on
// SIGTERM/SIGINT, and full timeout defaults.
func ListenAndServe(addr string, h http.Handler) error {
	return Serve(NewServer(addr, h))
}

// Logging logs method, path, status-ish duration.
func Logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s (%s)", r.Method, r.URL.Path, time.Since(start).Round(time.Microsecond))
	})
}

// Recover converts panics into RFC7807 500s.
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("panic: %v\n%s", rec, debug.Stack())
				Errorf(w, http.StatusInternalServerError, "internal_error", "%v", rec)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
