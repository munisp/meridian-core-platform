// consent — NDPA consent management (SPEC 2). Consent records with receipts
// per NDPA (grant/renew/revoke all receipted and hash-sealed).
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/munisp/meridian-core-platform/packages/events/auth"
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

// Receipt is the NDPA receipt issued for every consent action.
type Receipt struct {
	ReceiptID string `json:"receipt_id"`
	ConsentID string `json:"consent_id"`
	Subject   string `json:"subject"`
	Action    string `json:"action"` // granted|revoked
	Time      string `json:"time"`
	Actor     string `json:"actor"`
	SHA256    string `json:"sha256"` // hash of the consent record at action time
}

var lawfulBases = map[string]bool{
	"consent": true, "contract": true, "legal_obligation": true,
	"vital_interest": true, "public_task": true, "legitimate_interest": true,
}

type server struct {
	st store.DocStore
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
	if err := s.st.Put("receipts", r.ReceiptID, r); err != nil {
		log.Printf("receipt persist: %v", err)
	}
	return r
}

func main() {
	dir := httpx.Env("DATA_DIR", "./data")
	st, err := store.OpenFromEnv(dir)
	if err != nil {
		log.Fatal(err)
	}
	s := &server{st: st}

	mux := http.NewServeMux()
	httpx.RegisterStandard(mux, service, version, nil)
	mux.HandleFunc("POST /v1/consents", s.create)
	mux.HandleFunc("GET /v1/consents/{subject}", s.listBySubject)
	mux.HandleFunc("POST /v1/consents/{id}/revoke", s.revoke)
	mux.HandleFunc("GET /v1/receipts/{id}", s.getReceipt)

	addr := ":" + httpx.Port("8007")
	log.Printf("%s %s", service, version)
	log.Fatal(httpx.ListenAndServe(addr, auth.Middleware(mux)))
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

func (s *server) getReceipt(w http.ResponseWriter, r *http.Request) {
	var receipt Receipt
	if err := s.st.Get("receipts", r.PathValue("id"), &receipt); err != nil {
		httpx.NotFound(w, "receipt %s", r.PathValue("id"))
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"receipt": receipt})
}
