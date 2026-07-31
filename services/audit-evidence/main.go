// audit-evidence — audit service + WORM evidence service (SPEC 2).
package main

import (
	"encoding/base64"
	"log"
	"net/http"
	"os"

	"github.com/munisp/meridian-core-platform/packages/events/auth"
	"github.com/munisp/meridian-core-platform/packages/events/httpx"
	"github.com/munisp/meridian-core-platform/packages/events/store"
	"github.com/munisp/meridian-core-platform/services/audit-evidence/internal/evidence"
)

const (
	service = "audit-evidence"
	version = "0.1.0"
)

type server struct {
	al      *evidence.AuditLog
	ws      *evidence.WormStore
	sealKey string
}

func main() {
	dir := httpx.Env("DATA_DIR", "./data")
	// A5: dedicated seal/chain keys. PROFILE=prod REQUIRES TAT_SEAL_KEY —
	// no dev-secret default in prod (fail closed at startup).
	profile := httpx.Env("PROFILE", "dev")
	sealKey := os.Getenv("TAT_SEAL_KEY")
	if sealKey == "" {
		if profile == "prod" {
			log.Fatal("profile=prod FATAL: TAT_SEAL_KEY is required (dedicated ceremony key; no dev-secret default)")
		}
		sealKey = "dev-tat-seal-key-change-me"
		log.Printf("profile=dev component=audit-evidence WARNING: TAT_SEAL_KEY unset, using dev seal key")
	}
	chainKey := os.Getenv("TAT_CHAIN_HMAC_KEY")
	if chainKey == "" {
		chainKey = sealKey // chain HMAC key derives from the seal key by default
	}
	st, err := store.OpenFromEnv(dir)
	if err != nil {
		log.Fatal(err)
	}
	al, err := evidence.OpenAuditLog(dir, []byte(chainKey))
	if err != nil {
		log.Fatal(err)
	}
	ws, err := evidence.NewWormStoreFromEnv(dir+"/worm", st)
	if err != nil {
		log.Fatal(err)
	}
	s := &server{al: al, ws: ws, sealKey: sealKey}

	mux := http.NewServeMux()
	httpx.RegisterStandard(mux, service, version, nil)
	mux.HandleFunc("POST /v1/audit/events", s.appendEvent)
	mux.HandleFunc("GET /v1/audit/events", s.queryEvents)
	mux.HandleFunc("GET /v1/audit/chain/verify", s.verifyChain)
	mux.HandleFunc("POST /v1/evidence", s.putEvidence)
	mux.HandleFunc("GET /v1/evidence/{id}", s.getEvidence)
	mux.HandleFunc("GET /v1/evidence/{id}/verify", s.verifyEvidence)
	mux.HandleFunc("POST /v1/tat/assemble", s.assembleTAT)

	addr := ":" + httpx.Port("8004")
	log.Printf("%s %s", service, version)
	log.Fatal(httpx.ListenAndServe(addr, auth.Middleware(mux)))
}

type appendReq struct {
	Actor           string         `json:"actor"`
	Subject         string         `json:"subject"`
	Action          string         `json:"action"`
	Type            string         `json:"type"`
	RulePackVersion string         `json:"rule_pack_version,omitempty"`
	Details         map[string]any `json:"details,omitempty"`
}

func (s *server) appendEvent(w http.ResponseWriter, r *http.Request) {
	var req appendReq
	if err := httpx.Decode(r, &req); err != nil || req.Subject == "" || req.Action == "" {
		httpx.BadRequest(w, "subject and action are required")
		return
	}
	claims, _ := auth.FromContext(r.Context())
	if req.Actor == "" {
		req.Actor = claims.Sub
	}
	typ := req.Type
	if typ == "" {
		typ = "audit.generic"
	}
	e, err := s.al.Append(evidence.AuditEvent{
		Actor:           req.Actor,
		Subject:         req.Subject,
		Action:          req.Action,
		Type:            typ,
		TenantID:        claims.TenantID,
		RulePackVersion: req.RulePackVersion,
		Details:         req.Details,
	})
	if err != nil {
		httpx.Internal(w, "%v", err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"event": e})
}

func (s *server) queryEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	events, err := s.al.Query(q.Get("subject"), q.Get("type"), q.Get("from"), q.Get("to"))
	if err != nil {
		httpx.Internal(w, "%v", err)
		return
	}
	if events == nil {
		events = []evidence.AuditEvent{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"events": events, "count": len(events)})
}

func (s *server) verifyChain(w http.ResponseWriter, r *http.Request) {
	broken, err := s.al.VerifyChain()
	if err != nil {
		httpx.Internal(w, "%v", err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"chain_valid": broken == 0, "first_broken_seq": broken})
}

type evidenceReq struct {
	ID            string         `json:"id,omitempty"`
	ContentBase64 string         `json:"content_base64,omitempty"`
	Content       string         `json:"content,omitempty"` // inline UTF-8
	ContentType   string         `json:"content_type,omitempty"`
	Metadata      map[string]any `json:"metadata,omitempty"`
}

func (s *server) putEvidence(w http.ResponseWriter, r *http.Request) {
	var req evidenceReq
	if err := httpx.Decode(r, &req); err != nil {
		httpx.BadRequest(w, "invalid JSON: %v", err)
		return
	}
	var content []byte
	switch {
	case req.ContentBase64 != "":
		b, err := base64.StdEncoding.DecodeString(req.ContentBase64)
		if err != nil {
			httpx.BadRequest(w, "bad base64: %v", err)
			return
		}
		content = b
	case req.Content != "":
		content = []byte(req.Content)
	default:
		httpx.BadRequest(w, "content or content_base64 required")
		return
	}
	ct := req.ContentType
	if ct == "" {
		ct = "application/octet-stream"
	}
	obj, err := s.ws.Put(req.ID, content, ct, req.Metadata)
	if err == evidence.ErrImmutable() {
		httpx.Conflict(w, "evidence object is immutable (WORM)")
		return
	}
	if err != nil {
		httpx.Internal(w, "%v", err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"object": obj})
}

func (s *server) getEvidence(w http.ResponseWriter, r *http.Request) {
	obj, err := s.ws.Get(r.PathValue("id"))
	if err != nil {
		httpx.NotFound(w, "evidence %s", r.PathValue("id"))
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"object": obj})
}

func (s *server) verifyEvidence(w http.ResponseWriter, r *http.Request) {
	ok, got, err := s.ws.Verify(r.PathValue("id"))
	if err != nil {
		httpx.NotFound(w, "evidence %s", r.PathValue("id"))
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"id": r.PathValue("id"), "verified": ok, "sha256_actual": got})
}

type tatReq struct {
	Subject string `json:"subject"`
	From    string `json:"from,omitempty"`
	To      string `json:"to,omitempty"`
}

func (s *server) assembleTAT(w http.ResponseWriter, r *http.Request) {
	var req tatReq
	if err := httpx.Decode(r, &req); err != nil || req.Subject == "" {
		httpx.BadRequest(w, "subject required")
		return
	}
	claims, _ := auth.FromContext(r.Context())
	// A5: dedicated TAT seal key (never the shared dev JWT secret).
	tat, err := evidence.AssembleTAT(s.al, s.ws, req.Subject, req.From, req.To, claims.Sub, s.sealKey)
	if err != nil {
		httpx.Internal(w, "%v", err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"tat": tat})
}
