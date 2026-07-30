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
	st  *store.Store
	cfg graph.MatchConfig
	nin graph.NINAdapter
	cac graph.CACAdapter
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
	st, err := store.Open(dir)
	if err != nil {
		log.Fatal(err)
	}
	s := &server{st: st, cfg: loadMatchConfig(), nin: graph.NINSimulator{}, cac: graph.CACSimulator{}}

	mux := http.NewServeMux()
	httpx.RegisterStandard(mux, service, version, nil)
	mux.HandleFunc("POST /v1/tin/provision", s.provision)
	mux.HandleFunc("POST /v1/verify/tin", s.verifyTIN)
	mux.HandleFunc("POST /v1/verify/nin", s.verifyNIN)
	mux.HandleFunc("POST /v1/verify/cac", s.verifyCAC)
	mux.HandleFunc("POST /v1/entities/resolve", s.resolve)
	mux.HandleFunc("GET /v1/entities/{id}/graph", s.entityGraph)
	mux.HandleFunc("GET /v1/config/match-thresholds", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, http.StatusOK, s.cfg)
	})

	addr := ":" + httpx.Port("8003")
	log.Printf("%s %s (thresholds auto=%.2f review=%.2f)", service, version, s.cfg.AutoMatchThreshold, s.cfg.ReviewThreshold)
	log.Fatal(httpx.ListenAndServe(addr, auth.Middleware(mux)))
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
}

func (s *server) provision(w http.ResponseWriter, r *http.Request) {
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
	if err := s.st.Put("entities", e.ID, e); err != nil {
		httpx.Internal(w, "%v", err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"entity": e, "tin": tin, "tin_hash": tinHash})
}

func (s *server) verifyTIN(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TIN string `json:"tin"`
	}
	if err := httpx.Decode(r, &req); err != nil || req.TIN == "" {
		httpx.BadRequest(w, "tin required")
		return
	}
	hash := graph.HashTIN(req.TIN)
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
		NIN string `json:"nin"`
	}
	if err := httpx.Decode(r, &req); err != nil || req.NIN == "" {
		httpx.BadRequest(w, "nin required")
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

func (s *server) entityGraph(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	gv := graph.BuildGraph(id, s.allEntities())
	if len(gv.Nodes) == 0 {
		httpx.NotFound(w, "entity %s", id)
		return
	}
	httpx.JSON(w, http.StatusOK, gv)
}
