// consent — NDPA consent management (SPEC 2). Consent records with receipts
// per NDPA (grant/renew/revoke all receipted and hash-sealed).
package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/munisp/meridian-core-platform/packages/events/auth"
	"github.com/munisp/meridian-core-platform/packages/events/bus"
	"github.com/munisp/meridian-core-platform/packages/events/envelope"
	"github.com/munisp/meridian-core-platform/packages/events/httpx"
	"github.com/munisp/meridian-core-platform/packages/events/store"
)

const (
	service = "consent"
	version = "0.1.0"
)

// Consent is one NDPA consent record.
type Consent struct {
	ID          string         `json:"id"`
	Subject     string         `json:"subject"` // pseudonymised subject id (tin_hash/nin_hash)
	Purpose     string         `json:"purpose"`
	LawfulBasis string         `json:"lawful_basis"` // consent|contract|legal_obligation|vital_interest|public_task|legitimate_interest
	Granted     bool           `json:"granted"`
	Channel     string         `json:"channel,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	GrantedAt   string         `json:"granted_at"`
	ExpiresAt   string         `json:"expires_at,omitempty"`
	RevokedAt   string         `json:"revoked_at,omitempty"`
	Status      string         `json:"status"` // active|revoked|expired
}

// Receipt is the NDPA receipt issued for every consent action. HMAC is a
// keyed HMAC-SHA256 over the receipt contents (A7: receipts are forged by
// no one holding only the data store — the key never leaves the service).
type Receipt struct {
	ReceiptID string `json:"receipt_id"`
	ConsentID string `json:"consent_id"`
	Subject   string `json:"subject"`
	Action    string `json:"action"` // granted|denied|revoked|renewed
	Time      string `json:"time"`
	Actor     string `json:"actor"`
	SHA256    string `json:"sha256"` // hash of the consent record at action time
	HMAC      string `json:"hmac"`   // keyed HMAC-SHA256 over the fields above
}

var lawfulBases = map[string]bool{
	"consent": true, "contract": true, "legal_obligation": true,
	"vital_interest": true, "public_task": true, "legitimate_interest": true,
}

type server struct {
	st         store.DocStore
	receiptKey []byte
	eventBus   bus.Bus // C2 alert events; nil in tests
}

func hashConsent(c Consent) string {
	b, _ := json.Marshal(c)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (s *server) issueReceipt(c Consent, action, actor string) Receipt {
	r := Receipt{
		ReceiptID: "rcpt-" + envelope.NewULID(),
		ConsentID: c.ID,
		Subject:   c.Subject,
		Action:    action,
		Time:      time.Now().UTC().Format(time.RFC3339),
		Actor:     actor,
		SHA256:    hashConsent(c),
	}
	r.HMAC = receiptHMAC(s.receiptKey, r)
	if err := s.st.Put("receipts", r.ReceiptID, r); err != nil {
		log.Printf("receipt persist: %v", err)
	}
	return r
}

// receiptHMAC computes the keyed receipt seal over the stable fields.
func receiptHMAC(key []byte, r Receipt) string {
	payload := strings.Join([]string{
		r.ReceiptID, r.ConsentID, r.Subject, r.Action, r.Time, r.Actor, r.SHA256,
	}, "|")
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifyReceipt recomputes the receipt seal (exposed for audit checks).
func (s *server) VerifyReceipt(r Receipt) bool {
	return hmac.Equal([]byte(receiptHMAC(s.receiptKey, r)), []byte(r.HMAC))
}

// ownsConsent enforces NDPA ownership: only the subject themselves or an
// admin may act on a consent record (A7).
func ownsConsent(claims auth.Claims, c Consent) bool {
	return claims.Sub == c.Subject || claims.HasRole("admin")
}

func main() {
	dir := httpx.Env("DATA_DIR", "./data")
	// A7: dedicated receipt HMAC key. PROFILE=prod REQUIRES it (no dev
	// default) so receipts are unforgeable by store-only attackers.
	receiptKey := os.Getenv("CONSENT_RECEIPT_KEY")
	if receiptKey == "" {
		if os.Getenv("PROFILE") == "prod" {
			log.Fatal("profile=prod FATAL: CONSENT_RECEIPT_KEY is required (no dev default)")
		}
		receiptKey = "meridian-dev-consent-receipt-key"
		log.Printf("profile=dev component=consent WARNING: CONSENT_RECEIPT_KEY unset, using dev key")
	}
	st, err := store.OpenFromEnv(dir)
	if err != nil {
		log.Fatal(err)
	}
	s := &server{st: st, receiptKey: []byte(receiptKey)}

	mux := http.NewServeMux()
	httpx.RegisterStandard(mux, service, version, nil)
	mux.HandleFunc("POST /v1/consents", s.create)
	mux.HandleFunc("POST /v1/consents/check", s.check) // C1 fast path
	mux.HandleFunc("GET /v1/consents/{subject}", s.listBySubject)
	mux.HandleFunc("POST /v1/consents/{id}/revoke", s.revoke)
	mux.HandleFunc("POST /v1/consents/{id}/renew", s.renew)
	mux.HandleFunc("GET /v1/receipts/{id}", s.getReceipt)
	// C2: NDPA s.40 breach registry — privacy:officer/admin only
	mux.HandleFunc("POST /v1/privacy/breaches", requireAnyRole(s.breachCreate, "privacy:officer", "admin"))
	mux.HandleFunc("GET /v1/privacy/breaches", requireAnyRole(s.breachList, "privacy:officer", "admin"))
	mux.HandleFunc("GET /v1/privacy/breaches/{id}", requireAnyRole(s.breachGet, "privacy:officer", "admin"))
	mux.HandleFunc("POST /v1/privacy/breaches/{id}/transition", requireAnyRole(s.breachTransition, "privacy:officer", "admin"))
	// C3: data subject rights (export / erasure / access log)
	mux.HandleFunc("GET /v1/dsr/{subject}/export", s.dsrExport)
	mux.HandleFunc("POST /v1/dsr/{subject}/erasure", s.dsrErasure)
	mux.HandleFunc("GET /v1/dsr/{subject}/access-log", s.dsrAccessLog)

	addr := ":" + httpx.Port("8007")
	log.Printf("%s %s", service, version)
	httpx.InitMetrics(service, version)
	httpx.StartMetricsServer()
	log.Fatal(httpx.ListenAndServe(addr, auth.Middleware(httpx.Instrument(mux))))
}

type createReq struct {
	Subject     string         `json:"subject"`
	Purpose     string         `json:"purpose"`
	LawfulBasis string         `json:"lawful_basis"`
	Granted     *bool          `json:"granted"`
	Channel     string         `json:"channel,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	ExpiresAt   string         `json:"expires_at,omitempty"`
}

func (s *server) create(w http.ResponseWriter, r *http.Request) {
	var req createReq
	if err := httpx.Decode(r, &req); err != nil {
		httpx.BadRequest(w, "invalid JSON: %v", err)
		return
	}
	if req.Subject == "" || req.Purpose == "" {
		httpx.BadRequest(w, "subject and purpose are required")
		return
	}
	if req.LawfulBasis == "" {
		req.LawfulBasis = "consent"
	}
	if !lawfulBases[req.LawfulBasis] {
		httpx.BadRequest(w, "lawful_basis must be one of NDPA bases")
		return
	}
	granted := true
	if req.Granted != nil {
		granted = *req.Granted
	}
	claims, _ := auth.FromContext(r.Context())
	// B2-#14 (NDPA): the consent subject is bound to the caller's JWT
	// principal — a caller may only capture consent for THEMSELVES;
	// cross-subject capture requires the admin role (receipt records the
	// admin actor, preserving the audit trail).
	if req.Subject != claims.Sub && !claims.HasRole("admin") {
		httpx.Errorf(w, http.StatusForbidden, "forbidden",
			"subject must match the authenticated principal (cross-subject create requires admin)")
		return
	}
	c := Consent{
		ID:          "con-" + envelope.NewULID(),
		Subject:     req.Subject,
		Purpose:     req.Purpose,
		LawfulBasis: req.LawfulBasis,
		Granted:     granted,
		Channel:     req.Channel,
		Metadata:    req.Metadata,
		GrantedAt:   time.Now().UTC().Format(time.RFC3339),
		ExpiresAt:   req.ExpiresAt,
		Status:      "active",
	}
	if !granted {
		c.Status = "revoked"
		c.RevokedAt = c.GrantedAt
	}
	if err := s.st.Put("consents", c.ID, c); err != nil {
		httpx.Internal(w, "%v", err)
		return
	}
	receipt := s.issueReceipt(c, map[bool]string{true: "granted", false: "denied"}[granted], claims.Sub)
	httpx.JSON(w, http.StatusCreated, map[string]any{"consent": c, "receipt": receipt})
}

func (s *server) listBySubject(w http.ResponseWriter, r *http.Request) {
	subject := r.PathValue("subject")
	// B2-#14 (NDPA): listing is scoped to the caller's own subject;
	// cross-subject reads require the admin role (IDOR fix).
	claims, _ := auth.FromContext(r.Context())
	if subject != claims.Sub && !claims.HasRole("admin") {
		httpx.Errorf(w, http.StatusForbidden, "forbidden",
			"only the data subject or an admin may list consents for %s", subject)
		return
	}
	var all []Consent
	if err := s.st.ListInto("consents", &all); err != nil {
		httpx.Internal(w, "%v", err)
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	out := []Consent{}
	for _, c := range all {
		if c.Subject != subject {
			continue
		}
		if c.Status == "active" && c.ExpiresAt != "" && c.ExpiresAt < now {
			c.Status = "expired"
			_ = s.st.Put("consents", c.ID, c)
		}
		out = append(out, c)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"subject": subject, "consents": out, "count": len(out)})
}

func (s *server) revoke(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var c Consent
	if err := s.st.Get("consents", id, &c); err != nil {
		httpx.NotFound(w, "consent %s", id)
		return
	}
	if c.Status == "revoked" {
		httpx.Conflict(w, "consent %s already revoked", id)
		return
	}
	claims, _ := auth.FromContext(r.Context())
	// A7: revoke requires subject ownership or admin role (NDPA: only the
	// data subject or an authorised officer may withdraw consent).
	if !ownsConsent(claims, c) {
		httpx.Errorf(w, http.StatusForbidden, "forbidden",
			"only the data subject or an admin may revoke this consent")
		return
	}
	c.Status = "revoked"
	c.Granted = false
	c.RevokedAt = time.Now().UTC().Format(time.RFC3339)
	if err := s.st.Put("consents", id, c); err != nil {
		httpx.Internal(w, "%v", err)
		return
	}
	receipt := s.issueReceipt(c, "revoked", claims.Sub)
	httpx.JSON(w, http.StatusOK, map[string]any{"consent": c, "receipt": receipt})
}

// renew: POST /v1/consents/{id}/renew {"expires_at": "..."} — A7. Extends or
// reactivates a non-revoked consent; revoked consents must be re-granted via
// POST /v1/consents (a fresh grant, not a silent resurrection).
func (s *server) renew(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ExpiresAt string `json:"expires_at"` // RFC3339; empty = non-expiring
	}
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Errorf(w, http.StatusBadRequest, "bad request", "%v", err)
		return
	}
	id := r.PathValue("id")
	var c Consent
	if err := s.st.Get("consents", id, &c); err != nil {
		httpx.NotFound(w, "consent %s", id)
		return
	}
	claims, _ := auth.FromContext(r.Context())
	if !ownsConsent(claims, c) {
		httpx.Errorf(w, http.StatusForbidden, "forbidden",
			"only the data subject or an admin may renew this consent")
		return
	}
	if c.Status == "revoked" {
		httpx.Conflict(w, "consent %s is revoked; grant a fresh consent instead", id)
		return
	}
	c.Status = "active"
	c.Granted = true
	c.RevokedAt = ""
	c.ExpiresAt = req.ExpiresAt
	if err := s.st.Put("consents", id, c); err != nil {
		httpx.Internal(w, "%v", err)
		return
	}
	receipt := s.issueReceipt(c, "renewed", claims.Sub)
	httpx.JSON(w, http.StatusOK, map[string]any{"consent": c, "receipt": receipt})
}

func (s *server) getReceipt(w http.ResponseWriter, r *http.Request) {
	var receipt Receipt
	if err := s.st.Get("receipts", r.PathValue("id"), &receipt); err != nil {
		httpx.NotFound(w, "receipt %s", r.PathValue("id"))
		return
	}
	// V2 repair: receipts were readable by ANY authenticated caller (IDOR).
	// Only the receipt's subject or an admin may read it.
	claims, ok := auth.FromContext(r.Context())
	if !ok || (claims.Sub != receipt.Subject && !claims.HasRole("admin")) {
		httpx.Errorf(w, http.StatusForbidden, "forbidden", "only the receipt subject or an admin may read a receipt")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"receipt": receipt})
}
