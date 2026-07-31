// check.go — C1: CheckConsent fast path (NDPA consent gating).
//
// POST /v1/consents/check {subject, purpose, lawful_basis} -> {allowed, ...}
//
// Strategy: RE-CHECK ON EVERY REQUEST. There is no event-driven cache
// invalidation: every consuming service (e.g. tin-graph verification) calls
// this endpoint per request, so a revocation takes effect immediately and no
// stale-consent window exists. This is the simpler correct option — any
// cached/event-driven design would reintroduce a revocation lag.
package main

import (
	"net/http"
	"time"

	"github.com/munisp/meridian-core-platform/packages/events/httpx"
)

// checkReq is one consent-gate query from a consuming service.
type checkReq struct {
	Subject     string `json:"subject"`  // pseudonymised subject id (tin_hash/nin_hash)
	Purpose     string `json:"purpose"`  // e.g. nin_verification, tin_verification
	LawfulBasis string `json:"lawful_basis,omitempty"`
}

// findValidConsent returns the active, granted, non-expired consent covering
// (subject, purpose), lazily expiring records whose ExpiresAt has passed.
// Fail-closed by construction: any ambiguity (missing, revoked, expired,
// denied) returns ok=false.
func (s *server) findValidConsent(subject, purpose string, now time.Time) (Consent, bool) {
	var all []Consent
	if err := s.st.ListInto("consents", &all); err != nil {
		return Consent{}, false
	}
	nowS := now.UTC().Format(time.RFC3339)
	for _, c := range all {
		if c.Subject != subject || c.Purpose != purpose {
			continue
		}
		if c.Status == "active" && c.ExpiresAt != "" && c.ExpiresAt < nowS {
			c.Status = "expired"
			_ = s.st.Put("consents", c.ID, c)
		}
		if c.Status == "active" && c.Granted {
			return c, true
		}
	}
	return Consent{}, false
}

// check is the fast path consumed by tin-graph (and any other processor).
// Answers are RFC7807 on malformed requests; a well-formed query always gets
// a 200 with an explicit allowed flag (deny is data, not an error).
func (s *server) check(w http.ResponseWriter, r *http.Request) {
	var req checkReq
	if err := httpx.Decode(r, &req); err != nil {
		httpx.BadRequest(w, "invalid JSON: %v", err)
		return
	}
	if req.Subject == "" || req.Purpose == "" {
		httpx.BadRequest(w, "subject and purpose are required")
		return
	}
	if req.LawfulBasis != "" && !lawfulBases[req.LawfulBasis] {
		httpx.BadRequest(w, "lawful_basis must be one of the NDPA bases")
		return
	}
	c, ok := s.findValidConsent(req.Subject, req.Purpose, time.Now().UTC())
	if !ok {
		httpx.JSON(w, http.StatusOK, map[string]any{
			"allowed": false,
			"subject": req.Subject,
			"purpose": req.Purpose,
			"reason":  "no active consent for subject+purpose",
		})
		return
	}
	// If the caller asserts a lawful basis, the consent covering the
	// processing must rest on that same basis.
	if req.LawfulBasis != "" && c.LawfulBasis != req.LawfulBasis {
		httpx.JSON(w, http.StatusOK, map[string]any{
			"allowed": false,
			"subject": req.Subject,
			"purpose": req.Purpose,
			"reason":  "consent lawful_basis mismatch",
		})
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"allowed":      true,
		"subject":      req.Subject,
		"purpose":      req.Purpose,
		"consent_id":   c.ID,
		"lawful_basis": c.LawfulBasis,
		"expires_at":   c.ExpiresAt,
	})
}
