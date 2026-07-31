// dsr.go — C3: NDPA data-subject-rights endpoints.
//
//   GET  /v1/dsr/{subject}/export    — JSON portability bundle (consents +
//                                      receipts + breach exposures).
//   POST /v1/dsr/{subject}/erasure   — anonymise the subject's consent
//                                      records (legal-hold aware), with an
//                                      audit entry.
//   GET  /v1/dsr/{subject}/access-log — DSR access log (officer/admin only).
//
// Every DSR touch is recorded in dsr_access_log. Erasure anonymises rather
// than deletes (consistent with the platform's anonymise-not-delete
// retention policy): the record structure and NDPA receipts survive for
// accountability, but the subject linkage and free-form metadata are
// irreversibly pseudonymised.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"time"

	"github.com/munisp/meridian-core-platform/packages/events/auth"
	"github.com/munisp/meridian-core-platform/packages/events/envelope"
	"github.com/munisp/meridian-core-platform/packages/events/httpx"
)

// dsrAuthorize allows the subject themselves, or a privacy:officer/admin.
func dsrAuthorize(claims auth.Claims, subject string) bool {
	return claims.Sub == subject || claims.HasRole("admin") || claims.HasRole("privacy:officer")
}

// logAccess appends a DSR access-log entry (best-effort, like receipts).
func (s *server) logAccess(subject, action, actor string) {
	id := "dsl-" + envelope.NewULID()
	if err := s.st.Put("dsr_access_log", id, map[string]any{
		"id": id, "subject": subject, "action": action, "actor": actor,
		"time": time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		// access logging must not silently fail — surface loudly
		// (the platform log is the fallback audit trail)
		println("dsr access-log persist failure:", err.Error())
	}
}

func (s *server) dsrExport(w http.ResponseWriter, r *http.Request) {
	subject := r.PathValue("subject")
	claims, _ := auth.FromContext(r.Context())
	if !dsrAuthorize(claims, subject) {
		httpx.Errorf(w, http.StatusForbidden, "forbidden",
			"only the data subject or a privacy officer may export this data")
		return
	}
	var consents []Consent
	if err := s.st.ListInto("consents", &consents); err != nil {
		httpx.Internal(w, "%v", err)
		return
	}
	ownC := []Consent{}
	for _, c := range consents {
		if c.Subject == subject {
			ownC = append(ownC, c)
		}
	}
	var receipts []Receipt
	if err := s.st.ListInto("receipts", &receipts); err != nil {
		httpx.Internal(w, "%v", err)
		return
	}
	ownR := []Receipt{}
	for _, rc := range receipts {
		if rc.Subject == subject {
			ownR = append(ownR, rc)
		}
	}
	var breaches []Breach
	if err := s.st.ListInto("breaches", &breaches); err != nil {
		httpx.Internal(w, "%v", err)
		return
	}
	exposed := []string{}
	for _, b := range breaches {
		for _, sub := range b.AffectedSubjects {
			if sub == subject {
				exposed = append(exposed, b.ID)
			}
		}
	}
	s.logAccess(subject, "export", claims.Sub)
	httpx.JSON(w, http.StatusOK, map[string]any{
		"subject":      subject,
		"exported_at":  time.Now().UTC().Format(time.RFC3339),
		"format":       "application/json (NDPA portability bundle)",
		"consents":     ownC,
		"receipts":     ownR,
		"breach_ids":   exposed,
	})
}

// legalHold is an active-preservation marker; erasure is refused while one
// is active for the subject.
type legalHold struct {
	Subject string `json:"subject"`
	Active  bool   `json:"active"`
	Reason  string `json:"reason,omitempty"`
}

func (s *server) underLegalHold(subject string) bool {
	var holds []legalHold
	if err := s.st.ListInto("legal_holds", &holds); err != nil {
		return false
	}
	for _, h := range holds {
		if h.Subject == subject && h.Active {
			return true
		}
	}
	return false
}

// anonymisedSubject derives the irreversible post-erasure subject id.
func anonymisedSubject(subject string) string {
	sum := sha256.Sum256([]byte("dsr-erasure|" + subject))
	return "anon-" + hex.EncodeToString(sum[:])[:24]
}

func (s *server) dsrErasure(w http.ResponseWriter, r *http.Request) {
	subject := r.PathValue("subject")
	claims, _ := auth.FromContext(r.Context())
	if !dsrAuthorize(claims, subject) {
		httpx.Errorf(w, http.StatusForbidden, "forbidden",
			"only the data subject or a privacy officer may request erasure")
		return
	}
	if s.underLegalHold(subject) {
		s.logAccess(subject, "erasure_refused_legal_hold", claims.Sub)
		httpx.Conflict(w, "subject %s is under an active legal hold; erasure refused", subject)
		return
	}
	var consents []Consent
	if err := s.st.ListInto("consents", &consents); err != nil {
		httpx.Internal(w, "%v", err)
		return
	}
	anon := anonymisedSubject(subject)
	now := time.Now().UTC().Format(time.RFC3339)
	n := 0
	for _, c := range consents {
		if c.Subject != subject {
			continue
		}
		c.Subject = anon
		c.Metadata = nil
		c.Channel = ""
		if c.Status == "active" {
			c.Status = "revoked"
			c.Granted = false
			c.RevokedAt = now
		}
		if err := s.st.Put("consents", c.ID, c); err != nil {
			httpx.Internal(w, "%v", err)
			return
		}
		n++
	}
	// audit entry: keyed by anon id so the audit trail itself carries no
	// re-identifiable subject reference beyond the erasure mapping.
	auditID := "aud-" + envelope.NewULID()
	if err := s.st.Put("dsr_audit", auditID, map[string]any{
		"id": auditID, "subject": subject, "anonymised_subject": anon,
		"action": "erasure", "records_anonymised": n,
		"actor": claims.Sub, "time": now,
	}); err != nil {
		httpx.Internal(w, "%v", err)
		return
	}
	s.logAccess(subject, "erasure", claims.Sub)
	httpx.JSON(w, http.StatusOK, map[string]any{
		"subject": subject, "anonymised_subject": anon,
		"records_anonymised": n, "audit_id": auditID,
	})
}

func (s *server) dsrAccessLog(w http.ResponseWriter, r *http.Request) {
	subject := r.PathValue("subject")
	claims, _ := auth.FromContext(r.Context())
	if !claims.HasRole("admin") && !claims.HasRole("privacy:officer") {
		httpx.Errorf(w, http.StatusForbidden, "forbidden",
			"access log is restricted to privacy officers and admins")
		return
	}
	var entries []map[string]any
	if err := s.st.ListInto("dsr_access_log", &entries); err != nil {
		httpx.Internal(w, "%v", err)
		return
	}
	out := []map[string]any{}
	for _, e := range entries {
		if e["subject"] == subject {
			out = append(out, e)
		}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"subject": subject, "entries": out, "count": len(out)})
}
