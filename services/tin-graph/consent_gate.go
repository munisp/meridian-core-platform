// consent_gate.go — C1: NDPA consent gating for verification endpoints.
//
// NIN/TIN verification processes personal data, so every request must
//   1. carry a lawful_basis (one of the six NDPA bases), and
//   2. hold an active consent for (subject, purpose) in the consent service.
//
// The gate RE-CHECKS consent on every request (see consent/check.go), so a
// revocation halts processing immediately.
//
// Fail-closed contract:
//   - consent reachable, no valid consent -> 403 RFC7807 (always, dev+prod).
//   - consent unreachable/misconfigured in PROFILE=prod -> startup refuses
//     (CONSENT_URL required); in dev the gate degrades to allow-with-warning
//     so local workflows without a consent service keep working.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/munisp/meridian-core-platform/packages/events/otelx"
	"github.com/munisp/meridian-core-platform/packages/events/httpx"
)

var gateLawfulBases = map[string]bool{
	"consent": true, "contract": true, "legal_obligation": true,
	"vital_interest": true, "public_task": true, "legitimate_interest": true,
}

// consentChecker is the consent-gate client seam (HTTP in prod, fake in tests).
type consentChecker interface {
	Check(ctx context.Context, subject, purpose, lawfulBasis string) (bool, error)
}

// httpConsentChecker calls the consent service CheckConsent fast path.
type httpConsentChecker struct {
	base   string
	client *http.Client
}

func (h *httpConsentChecker) Check(ctx context.Context, subject, purpose, lawfulBasis string) (bool, error) {
	body, _ := json.Marshal(map[string]string{
		"subject": subject, "purpose": purpose, "lawful_basis": lawfulBasis,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.base+"/v1/consents/check", bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	// propagate the caller's credentials so consent sees the same principal
	if tok, ok := ctx.Value(bearerCtxKey{}).(string); ok && tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("consent check status %d", resp.StatusCode)
	}
	var out struct {
		Allowed bool `json:"allowed"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return false, err
	}
	return out.Allowed, nil
}

// bearerCtxKey carries the inbound bearer token to the consent call.
type bearerCtxKey struct{}

// noopConsentChecker allows everything; dev-only (constructed only when
// PROFILE != prod and CONSENT_URL is unset).
type noopConsentChecker struct{}

func (noopConsentChecker) Check(context.Context, string, string, string) (bool, error) {
	return true, nil
}

// consentCheckerFromEnv wires the gate. Returns (checker, required): prod
// without CONSENT_URL is a startup error (fail-closed), handled by main.
func consentCheckerFromEnv(profile string) (consentChecker, error) {
	base := os.Getenv("CONSENT_URL")
	if base == "" {
		if profile == "prod" {
			return nil, fmt.Errorf("profile=prod FATAL: CONSENT_URL is required (consent gate fail-closed)")
		}
		log.Printf("profile=dev component=tin-graph WARNING: CONSENT_URL unset, consent gate is allow-all (dev only)")
		return noopConsentChecker{}, nil
	}
	return &httpConsentChecker{base: base, client: &http.Client{Timeout: 5 * time.Second, Transport: otelx.Client(nil)}}, nil
}

// gateVerification enforces the C1 contract on a verify endpoint. It returns
// true when processing may proceed; otherwise it has already written the
// RFC7807 error response.
func (s *server) gateVerification(w http.ResponseWriter, r *http.Request, subjectHash, purpose, lawfulBasis string) bool {
	if lawfulBasis == "" {
		httpx.Errorf(w, http.StatusBadRequest, "bad request",
			"lawful_basis is required for verification (NDPA)")
		return false
	}
	if !gateLawfulBases[lawfulBasis] {
		httpx.Errorf(w, http.StatusBadRequest, "bad request",
			"lawful_basis must be one of the six NDPA bases")
		return false
	}
	ctx := r.Context()
	if tok := bearerToken(r); tok != "" {
		ctx = context.WithValue(ctx, bearerCtxKey{}, tok)
	}
	allowed, err := s.consent.Check(ctx, subjectHash, purpose, lawfulBasis)
	if err != nil {
		// unreachable consent service: fail closed in prod; dev noop never errs
		httpx.Errorf(w, http.StatusServiceUnavailable, "consent unavailable",
			"consent check failed; verification denied (fail-closed): %v", err)
		return false
	}
	if !allowed {
		httpx.Errorf(w, http.StatusForbidden, "consent required",
			"no valid consent for subject+purpose %q; verification denied (NDPA)", purpose)
		return false
	}
	return true
}

func bearerToken(r *http.Request) string {
	const p = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) > len(p) && h[:len(p)] == p {
		return h[len(p):]
	}
	return ""
}
