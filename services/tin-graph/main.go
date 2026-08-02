// tin-graph — identity/TIN graph service (SPEC 2).
// NIN=TIN / CAC-RC=TIN fusion, verification adapters (NIMC/CAC simulators),
// entity resolution with thresholds from rp-identity-match-thresholds.
package main

import (
	_ "embed"
	"encoding/json"
	"log"
	"net/http"
	"os"

	"github.com/munisp/meridian-core-platform/packages/events/auth"
	"github.com/munisp/meridian-core-platform/packages/events/httpx"
	"github.com/munisp/meridian-core-platform/packages/events/store"
	"github.com/munisp/meridian-core-platform/services/tin-graph/internal/graph"
	"gopkg.in/yaml.v3"
)

const (
	service = "tin-graph"
	version = "0.1.0"
)

//go:embed packs/rp-identity-match-thresholds/1.0.0.yaml
var seedThresholdsPack []byte

type server struct {
	st      store.DocStore
	cfg     graph.MatchConfig
	nin     graph.NINAdapter
	cac     graph.CACAdapter
	consent consentChecker // C1 consent gate (nil only before wiring)
}

func loadMatchConfig() graph.MatchConfig {
	cfg := graph.DefaultMatchConfig
	// optional: override from rp-registry
	if url := os.Getenv("RP_REGISTRY_URL"); url != "" {
		if loaded, err := fetchPackConfig(url + "/v1/packs/rp-identity-match-thresholds/latest"); err == nil && loaded != nil {
			log.Printf("loaded rp-identity-match-thresholds from registry")
			return *loaded
		}
	}
	// [seed] embedded pack
	var pack struct {
		Rules []struct {
			ID   string         `yaml:"id"`
			Then map[string]any `yaml:"then"`
		} `yaml:"rules"`
	}
	if err := yaml.Unmarshal(seedThresholdsPack, &pack); err == nil {
		for _, r := range pack.Rules {
			if r.ID != "identity.match.config" {
				continue
			}
			if set, ok := r.Then["set"].(map[string]any); ok {
				if v, ok := set["auto_match_threshold"].(float64); ok {
					cfg.AutoMatchThreshold = v
				}
				if v, ok := set["review_threshold"].(float64); ok {
					cfg.ReviewThreshold = v
				}
				if w, ok := set["weights"].(map[string]any); ok {
					cfg.Weights = map[string]float64{}
					for k, v := range w {
						if f, ok := v.(float64); ok {
							cfg.Weights[k] = f
						}
					}
				}
			}
		}
	}
	return cfg
}

func fetchPackConfig(url string) (*graph.MatchConfig, error) {
	resp, err := http.Get(url) // dev only; prod goes through mTLS client
	if err != nil || resp.StatusCode != 200 {
		return nil, err
	}
	defer resp.Body.Close()
	var body struct {
		Pack struct {
			Rules []struct {
				ID   string `json:"id"`
				Then struct {
					Set struct {
						AutoMatchThreshold float64            `json:"auto_match_threshold"`
						ReviewThreshold    float64            `json:"review_threshold"`
						Weights            map[string]float64 `json:"weights"`
					} `json:"set"`
				} `json:"then"`
			} `json:"rules"`
		} `json:"pack"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	for _, r := range body.Pack.Rules {
		if r.ID == "identity.match.config" && r.Then.Set.AutoMatchThreshold > 0 {
			return &graph.MatchConfig{
				AutoMatchThreshold: r.Then.Set.AutoMatchThreshold,
				ReviewThreshold:    r.Then.Set.ReviewThreshold,
				Weights:            r.Then.Set.Weights,
			}, nil
		}
	}
	return nil, nil
}

func main() {
	dir := httpx.Env("DATA_DIR", "./data")
	st, err := store.OpenFromEnv(dir)
	if err != nil {
		log.Fatal(err)
	}
	// O8: fail-closed prod — NIMC/CAC simulators are dev-only; PROFILE=prod
	// without NIMC_API_URL/CAC_API_URL refuses to start.
	nin, cac, err := graph.AdaptersFromEnv(os.Getenv("PROFILE"))
	if err != nil {
		log.Fatal(err)
	}
	// C1: consent gate — fail-closed in prod (CONSENT_URL required).
	gate, err := consentCheckerFromEnv(os.Getenv("PROFILE"))
	if err != nil {
		log.Fatal(err)
	}
	// P0: Permify centralized authz for provisioning/taxpayer360 scoping —
	// fail-closed in prod without PERMIFY_URL (permify_gate.go).
	pc, err := permifyFromEnv(os.Getenv("PROFILE"))
	if err != nil {
		log.Fatal(err)
	}
	permChecker = pc
	s := &server{st: st, cfg: loadMatchConfig(), nin: nin, cac: cac, consent: gate}

	mux := s.routes()
	addr := ":" + httpx.Port("8003")
	log.Printf("%s %s (thresholds auto=%.2f review=%.2f)", service, version, s.cfg.AutoMatchThreshold, s.cfg.ReviewThreshold)
	log.Fatal(httpx.ListenAndServe(addr, auth.Middleware(mux)))
}

// routes registers the HTTP API. Authz (audit M-3): POST /v1/tin/provision
// and GET /v1/taxpayer360/{tin_hash} are object/role-scoped inside their
// handlers (nrs:officer/admin, or own-record reads for taxpayer360).
func (s *server) routes() *http.ServeMux {
	mux := http.NewServeMux()
	httpx.RegisterStandard(mux, service, version, nil)
	mux.HandleFunc("POST /v1/tin/provision", s.provision)
	mux.HandleFunc("POST /v1/verify/tin", s.verifyTIN)
	mux.HandleFunc("POST /v1/verify/nin", s.verifyNIN)
	mux.HandleFunc("POST /v1/verify/cac", s.verifyCAC)
	mux.HandleFunc("POST /v1/entities/resolve", s.resolve)
	mux.HandleFunc("GET /v1/entities/{id}/graph", s.entityGraph)
	mux.HandleFunc("GET /v1/entities/{id}/ubos", s.entityUBOs)
	mux.HandleFunc("POST /v1/entities/{id}/kyb", s.updateKYB)
	mux.HandleFunc("GET /v1/taxpayer360/{tin_hash}", s.taxpayer360Handler) // I1
	// M2: TIN dedup/merge lifecycle (merge.go) — officer-gated, 409 on
	// illegal lifecycle transitions, unmerge reversible within window.
	mux.HandleFunc("POST /v1/tins/merge", s.mergeTINs)
	mux.HandleFunc("POST /v1/tins/unmerge", s.unmergeTINs)
	mux.HandleFunc("POST /v1/tins/{tin}/status", s.setLifecycle)
	mux.HandleFunc("GET /v1/tins/{tin}/status", s.getLifecycle)
	mux.HandleFunc("GET /v1/tins/{tin}/filing-eligibility", s.filingEligibility)
	mux.HandleFunc("POST /v1/dedup/scan", s.dedupScan)
	mux.HandleFunc("GET /v1/dedup/candidates", s.dedupCandidates)
	mux.HandleFunc("GET /v1/config/match-thresholds", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, http.StatusOK, s.cfg)
	})
	return mux
}

func (s *server) allEntities() []graph.Entity {
	var ents []graph.Entity
	if err := s.st.ListInto("entities", &ents); err != nil {
		return nil
	}
	return ents
}

type provisionReq struct {
	NIN        string            `json:"nin,omitempty"`
	CACRC      string            `json:"cac_rc,omitempty"`
	EntityType string            `json:"entity_type"`
	Name       string            `json:"name"`
	Phone      string            `json:"phone,omitempty"`
	Email      string            `json:"email,omitempty"`
	Address    string            `json:"address,omitempty"`
	Attrs      map[string]string `json:"attrs,omitempty"`
	// Company carries the full KYB profile (O7) for CAC-track provision.
	Company *graph.CompanyProfile `json:"company,omitempty"`
}

func (s *server) provision(w http.ResponseWriter, r *http.Request) {
	// identity provisioning is privileged (audit M-3)
	if !canAdministerTIN(r) {
		httpx.Errorf(w, http.StatusForbidden, "forbidden", "role nrs:officer or admin required to provision TINs")
		return
	}
	var req provisionReq
	if err := httpx.Decode(r, &req); err != nil {
		httpx.BadRequest(w, "invalid JSON: %v", err)
		return
	}
	tin, err := graph.ProvisionTIN(req.NIN, req.CACRC)
	if err != nil {
		httpx.BadRequest(w, "%v", err)
		return
	}
	tinHash := graph.HashTIN(tin)
	// idempotent: same TIN fusion returns the existing entity
	for _, e := range s.allEntities() {
		if e.TINHash == tinHash {
			httpx.JSON(w, http.StatusOK, map[string]any{
				"entity": e, "tin": tin, "tin_hash": tinHash, "note": "existing entity (fusion idempotent)"})
			return
		}
	}
	et := req.EntityType
	if et == "" {
		if req.NIN != "" {
			et = "individual"
		} else {
			et = "company"
		}
	}
	e := graph.Entity{
		ID:         graph.NewEntityID(),
		TIN:        tin,
		TINHash:    tinHash,
		CACRC:      req.CACRC,
		EntityType: et,
		Name:       req.Name,
		Phone:      req.Phone,
		Email:      req.Email,
		Address:    req.Address,
		Attrs:      req.Attrs,
		CreatedAt:  graph.NowRFC3339(),
	}
	if req.NIN != "" {
		e.NINHash = graph.HashValue(req.NIN)
	}
	// KYB: full company profile -> directors/shareholders + derived UBOs (O7)
	if req.Company != nil {
		cp := req.Company
		e.EntityType = "company"
		if cp.RCNumber != "" {
			e.CACRC = cp.RCNumber
		}
		if cp.CompanyName != "" {
			e.Name = cp.CompanyName
		}
		if cp.RegisteredAddress != "" {
			e.Address = cp.RegisteredAddress
		}
		e.Directors = cp.Directors
		e.Shareholders = cp.Shareholders
		e.UBOs = graph.DeriveUBOs(cp.Shareholders, nil)
		e.RegistryCrossCheck = cp.RegistryCrossCheck
		if e.RegistryCrossCheck == "" {
			e.RegistryCrossCheck = s.cacProvider()
		}
	}
	if err := s.st.Put("entities", e.ID, e); err != nil {
		httpx.Internal(w, "%v", err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"entity": e, "tin": tin, "tin_hash": tinHash, "ubos": e.UBOs})
}

func (s *server) verifyTIN(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TIN         string `json:"tin"`
		LawfulBasis string `json:"lawful_basis"`
	}
	if err := httpx.Decode(r, &req); err != nil || req.TIN == "" {
		httpx.BadRequest(w, "tin required")
		return
	}
	hash := graph.HashTIN(req.TIN)
	// C1: consent gate — verification requires lawful_basis + valid consent.
	if !s.gateVerification(w, r, hash, "tin_verification", req.LawfulBasis) {
		return
	}
	for _, e := range s.allEntities() {
		if e.TINHash == hash {
			httpx.JSON(w, http.StatusOK, map[string]any{
				"valid": true, "tin_hash": hash, "entity_id": e.ID, "entity_type": e.EntityType})
			return
		}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"valid": false, "tin_hash": hash})
}

func (s *server) verifyNIN(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NIN         string `json:"nin"`
		LawfulBasis string `json:"lawful_basis"`
	}
	if err := httpx.Decode(r, &req); err != nil || req.NIN == "" {
		httpx.BadRequest(w, "nin required")
		return
	}
	// C1: consent gate — verification requires lawful_basis + valid consent.
	if !s.gateVerification(w, r, graph.HashValue(req.NIN), "nin_verification", req.LawfulBasis) {
		return
	}
	res, err := s.nin.Verify(req.NIN)
	if err != nil {
		httpx.Internal(w, "%v", err)
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

func (s *server) verifyCAC(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RC string `json:"rc_number"`
	}
	if err := httpx.Decode(r, &req); err != nil || req.RC == "" {
		httpx.BadRequest(w, "rc_number required")
		return
	}
	res, err := s.cac.Verify(req.RC)
	if err != nil {
		httpx.Internal(w, "%v", err)
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

func (s *server) resolve(w http.ResponseWriter, r *http.Request) {
	var attrs graph.Attributes
	if err := httpx.Decode(r, &attrs); err != nil {
		httpx.BadRequest(w, "invalid JSON: %v", err)
		return
	}
	decision, cands := graph.Resolve(s.cfg, attrs, s.allEntities())
	if len(cands) > 5 {
		cands = cands[:5]
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"decision":   decision,
		"candidates": cands,
		"rule_pack":  "rp-identity-match-thresholds",
		"thresholds": s.cfg,
	})
}

// cacProvider reports which CAC adapter is wired (sim tag stays visible).
func (s *server) cacProvider() string {
	res, err := s.cac.Verify("RC00000")
	if err == nil {
		return res.Provider
	}
	return "unknown"
}

func (s *server) findEntity(id string) (graph.Entity, bool) {
	for _, e := range s.allEntities() {
		if e.ID == id {
			return e, true
		}
	}
	return graph.Entity{}, false
}

// entityUBOs exposes the derived/declared UBO set (>25%) to onboarding (O7).
func (s *server) entityUBOs(w http.ResponseWriter, r *http.Request) {
	e, ok := s.findEntity(r.PathValue("id"))
	if !ok {
		httpx.NotFound(w, "entity %s", r.PathValue("id"))
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"entity_id": e.ID, "entity_type": e.EntityType, "cac_rc": e.CACRC,
		"ubos": e.UBOs, "directors": e.Directors,
		"ubo_threshold_percent": graph.UBOThresholdPercent,
		"registry_cross_check":  e.RegistryCrossCheck,
	})
}

// updateKYB attaches/refreshes the KYB profile on an existing company
// entity (document refs + directors/shareholders; UBOs re-derived).
func (s *server) updateKYB(w http.ResponseWriter, r *http.Request) {
	e, ok := s.findEntity(r.PathValue("id"))
	if !ok {
		httpx.NotFound(w, "entity %s", r.PathValue("id"))
		return
	}
	var cp graph.CompanyProfile
	if err := httpx.Decode(r, &cp); err != nil {
		httpx.BadRequest(w, "invalid JSON: %v", err)
		return
	}
	if cp.CompanyName != "" {
		e.Name = cp.CompanyName
	}
	if cp.RegisteredAddress != "" {
		e.Address = cp.RegisteredAddress
	}
	e.Directors = cp.Directors
	e.Shareholders = cp.Shareholders
	e.UBOs = graph.DeriveUBOs(cp.Shareholders, e.UBOs)
	e.RegistryCrossCheck = s.cacProvider()
	if err := s.st.Put("entities", e.ID, e); err != nil {
		httpx.Internal(w, "%v", err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"entity": e, "ubos": e.UBOs})
}

func (s *server) entityGraph(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	gv := graph.BuildGraph(id, s.allEntities())
	if len(gv.Nodes) == 0 {
		httpx.NotFound(w, "entity %s", id)
		return
	}
	httpx.JSON(w, http.StatusOK, gv)
}
