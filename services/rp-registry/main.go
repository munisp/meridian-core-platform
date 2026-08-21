// rp-registry — rule-pack registry service (SPEC 2).
// Stores rp-* packs (validated against the pack grammar), manages the
// draft->published lifecycle, emits nrs.rulepacks.published.v1 via the
// outbox, and tracks consumer pins for stale-consumer alerts.
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/munisp/meridian-core-platform/packages/events/auth"
	"github.com/munisp/meridian-core-platform/packages/events/bus"
	"github.com/munisp/meridian-core-platform/packages/events/envelope"
	"github.com/munisp/meridian-core-platform/packages/events/httpx"
	"github.com/munisp/meridian-core-platform/packages/events/outbox"
	"github.com/munisp/meridian-core-platform/packages/events/store"
	rpschema "github.com/munisp/meridian-core-platform/packages/rulepack-schema"
)

const (
	service = "rp-registry"
	version = "0.1.0"
)

// PackRecord is the stored form of one pack version.
type PackRecord struct {
	ID                 string              `json:"id"`
	Version            string              `json:"version"`
	Status             string              `json:"status"`
	EffectiveFrom      string              `json:"effective_from"`
	EffectiveTo        *string             `json:"effective_to"`
	SubjectToRegazette bool                `json:"subject_to_regazette"`
	Provenance         rpschema.Provenance `json:"provenance"`
	Signed             *rpschema.Signature `json:"signed"`
	RuleCount          int                 `json:"rules"`
	SHA256             string              `json:"sha256"`
	YAML               string              `json:"yaml"`
	Pack               map[string]any      `json:"pack"`
	RegisteredAt       string              `json:"registered_at"`
	PublishedAt        string              `json:"published_at,omitempty"`
}

// ConsumerPin records a consumer's pinned pack version.
type ConsumerPin struct {
	Consumer string `json:"consumer"`
	PackID   string `json:"pack_id"`
	Version  string `json:"version"`
	PinnedAt string `json:"pinned_at"`
}

type server struct {
	st  store.DocStore
	out outbox.Store
	// signKeys pins the rule-pack ceremony ed25519 public keys (key_id ->
	// pubkey), env-injected via RULEPACK_SIGNING_PUBKEYS (A1-09). prod is
	// PROFILE=prod.
	signKeys map[string]ed25519.PublicKey
	prod     bool
}

// verifyPackOnWrite enforces the A1-09 signature policy at publish time:
// any pack carrying a signed block is cryptographically verified when keys
// are pinned (fail-closed on mismatch); in prod, packs registered or
// published as status=published MUST carry a valid pinned-key signature.
func (s *server) verifyPackOnWrite(pack *rpschema.Pack) error {
	if pack.Signed == nil {
		if s.prod && pack.Status == "published" {
			return fmt.Errorf("PROFILE=prod refuses unsigned status=published pack (rule-injection guard, A1-09)")
		}
		if len(s.signKeys) == 0 {
			return nil // dev without pinned keys: schema format check only
		}
		return fmt.Errorf("unsigned pack cannot be registered while signing keys are pinned (A1-09)")
	}
	return rpschema.VerifyPackSignature(pack, s.signKeys)
}

func key(id, ver string) string { return id + "@" + ver }

func semverLess(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < 3; i++ {
		na, _ := strconv.Atoi(part(pa, i))
		nb, _ := strconv.Atoi(part(pb, i))
		if na != nb {
			return na < nb
		}
	}
	return false
}

func part(p []string, i int) string {
	if i < len(p) {
		return p[i]
	}
	return "0"
}

func main() {
	dir := httpx.Env("DATA_DIR", "./data")
	st, err := store.OpenFromEnv(dir)
	if err != nil {
		log.Fatal(err)
	}
	ob, err := outbox.NewFileStore(filepath.Join(dir, "outbox"))
	if err != nil {
		log.Fatal(err)
	}
	defer ob.Close()
	// A1-09: pinned rule-pack signing keys. PROFILE=prod refuses to boot
	// without them — registry store write access would otherwise allow
	// arbitrary rule injection with only a format check on the signature.
	signKeys, err := rpschema.ParseSigningKeys(httpx.Env("RULEPACK_SIGNING_PUBKEYS", ""))
	if err != nil {
		log.Fatalf("rp-registry: %v", err)
	}
	prod := httpx.Env("PROFILE", "dev") == "prod"
	if prod && len(signKeys) == 0 {
		log.Fatal("rp-registry: PROFILE=prod requires RULEPACK_SIGNING_PUBKEYS (env-injected pinned ed25519 keys); refusing to start (fail-closed, A1-09)")
	}
	s := &server{st: st, out: ob, signKeys: signKeys, prod: prod}

	b := bus.NewFromEnv()
	defer b.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	relay := outbox.Relay{Store: ob, Bus: b, Dir: filepath.Join(dir, "outbox")}
	go relay.Run(ctx)

	mux := http.NewServeMux()
	httpx.RegisterStandard(mux, service, version, nil)
	mux.HandleFunc("POST /v1/packs", s.registerPack)
	mux.HandleFunc("GET /v1/packs", s.listPacks)
	mux.HandleFunc("GET /v1/packs/{id}/latest", s.getLatest)
	mux.HandleFunc("GET /v1/packs/{id}/{version}", s.getPack)
	mux.HandleFunc("POST /v1/packs/{id}/{version}/publish",
		auth.RequireRole("board", s.publishPack))
	mux.HandleFunc("POST /v1/consumers", s.registerConsumer)
	mux.HandleFunc("GET /v1/consumers/stale", s.staleConsumers)

	addr := ":" + httpx.Port("8002")
	log.Printf("%s %s", service, version)
	log.Fatal(httpx.ListenAndServe(addr, auth.Middleware(mux)))
}

type registerReq struct {
	YAML string         `json:"yaml,omitempty"`
	Pack map[string]any `json:"pack,omitempty"`
}

func (s *server) registerPack(w http.ResponseWriter, r *http.Request) {
	var req registerReq
	if err := httpx.Decode(r, &req); err != nil {
		httpx.BadRequest(w, "invalid JSON: %v", err)
		return
	}
	var raw []byte
	if req.YAML != "" {
		raw = []byte(req.YAML)
	} else if req.Pack != nil {
		// JSON is valid YAML 1.2 — the validator accepts it directly.
		b, err := json.Marshal(req.Pack)
		if err != nil {
			httpx.BadRequest(w, "%v", err)
			return
		}
		raw = b
	} else {
		httpx.BadRequest(w, "provide yaml or pack")
		return
	}
	pack, errs := rpschema.ValidateYAML(raw)
	if len(errs) > 0 {
		msgs := make([]string, len(errs))
		for i, e := range errs {
			msgs[i] = e.Error()
		}
		httpx.JSON(w, http.StatusUnprocessableEntity, map[string]any{
			"type": "about:blank", "title": "pack_validation_failed", "status": 422,
			"errors": msgs,
		})
		return
	}
	// A1-09: real ed25519 verification on publish (previously the signature
	// was only format-checked — registry store write access meant arbitrary
	// rule injection).
	if err := s.verifyPackOnWrite(pack); err != nil {
		httpx.JSON(w, http.StatusUnprocessableEntity, map[string]any{
			"type": "about:blank", "title": "pack_signature_invalid", "status": 422,
			"errors": []string{err.Error()},
		})
		return
	}
	sum := sha256.Sum256(raw)
	rec := PackRecord{
		ID:                 pack.ID,
		Version:            pack.Version,
		Status:             pack.Status,
		EffectiveFrom:      pack.EffectiveFrom,
		EffectiveTo:        pack.EffectiveTo,
		SubjectToRegazette: pack.SubjectToRegazette,
		Provenance:         pack.Provenance,
		Signed:             pack.Signed,
		RuleCount:          len(pack.Rules),
		SHA256:             hex.EncodeToString(sum[:]),
		YAML:               string(raw),
		Pack:               pack.Raw,
		RegisteredAt:       time.Now().UTC().Format(time.RFC3339),
	}
	k := key(pack.ID, pack.Version)
	if existing, err := s.st.GetRaw("packs", k); err == nil && existing != nil {
		// idempotent re-registration of identical bytes
		var prev PackRecord
		if json.Unmarshal(existing, &prev) == nil && prev.SHA256 == rec.SHA256 {
			httpx.JSON(w, http.StatusOK, map[string]any{"pack": prev, "note": "identical pack already registered"})
			return
		}
		httpx.Conflict(w, "pack %s already registered with different content", k)
		return
	}
	if err := s.st.Put("packs", k, rec); err != nil {
		httpx.Internal(w, "%v", err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"pack": rec})
}

func (s *server) listPacks(w http.ResponseWriter, r *http.Request) {
	var recs []PackRecord
	if err := s.st.ListInto("packs", &recs); err != nil {
		httpx.Internal(w, "%v", err)
		return
	}
	type summary struct {
		ID                 string `json:"id"`
		Version            string `json:"version"`
		Status             string `json:"status"`
		Rules              int    `json:"rules"`
		SHA256             string `json:"sha256"`
		SubjectToRegazette bool   `json:"subject_to_regazette"`
		PublishedAt        string `json:"published_at,omitempty"`
	}
	out := make([]summary, 0, len(recs))
	for _, p := range recs {
		out = append(out, summary{p.ID, p.Version, p.Status, p.RuleCount, p.SHA256, p.SubjectToRegazette, p.PublishedAt})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"packs": out})
}

func (s *server) getPack(w http.ResponseWriter, r *http.Request) {
	k := key(r.PathValue("id"), r.PathValue("version"))
	var rec PackRecord
	if err := s.st.Get("packs", k, &rec); err != nil {
		httpx.NotFound(w, "pack %s", k)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"id": rec.ID, "version": rec.Version, "status": rec.Status,
		"sha256": rec.SHA256, "pack": rec.Pack, "yaml": rec.YAML,
		"provenance": rec.Provenance, "signed": rec.Signed,
		"subject_to_regazette": rec.SubjectToRegazette,
	})
}

func (s *server) latestPublished(id string) *PackRecord {
	var recs []PackRecord
	if err := s.st.ListInto("packs", &recs); err != nil {
		return nil
	}
	var best *PackRecord
	for i := range recs {
		p := &recs[i]
		if p.ID != id || p.Status != "published" {
			continue
		}
		if best == nil || semverLess(best.Version, p.Version) {
			cp := *p
			best = &cp
		}
	}
	return best
}

func (s *server) getLatest(w http.ResponseWriter, r *http.Request) {
	best := s.latestPublished(r.PathValue("id"))
	if best == nil {
		httpx.NotFound(w, "no published version of %s", r.PathValue("id"))
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"id": best.ID, "version": best.Version, "status": best.Status,
		"sha256": best.SHA256, "pack": best.Pack, "yaml": best.YAML,
		"provenance": best.Provenance, "signed": best.Signed,
		"subject_to_regazette": best.SubjectToRegazette,
	})
}

func (s *server) publishPack(w http.ResponseWriter, r *http.Request) {
	k := key(r.PathValue("id"), r.PathValue("version"))
	var rec PackRecord
	if err := s.st.Get("packs", k, &rec); err != nil {
		httpx.NotFound(w, "pack %s", k)
		return
	}
	switch rec.Status {
	case "published":
		httpx.JSON(w, http.StatusOK, map[string]any{"pack": rec, "note": "already published"})
		return
	case "retired":
		httpx.Conflict(w, "pack %s is retired", k)
		return
	}
	// A1-09: re-verify the pinned-key signature before promoting to
	// published — a tampered store record must not be publishable. Verified
	// against the pack AS REGISTERED (the signature covers the registered
	// body; ceremony signs the final published form).
	if rec.Signed != nil || s.prod {
		pack := &rpschema.Pack{ID: rec.ID, Version: rec.Version, Status: rec.Status, Signed: rec.Signed, Raw: rec.Pack}
		if err := s.verifyPackOnWrite(pack); err != nil {
			httpx.JSON(w, http.StatusUnprocessableEntity, map[string]any{
				"type": "about:blank", "title": "pack_signature_invalid", "status": 422,
				"errors": []string{err.Error()},
			})
			return
		}
	}
	rec.Status = "published"
	rec.PublishedAt = time.Now().UTC().Format(time.RFC3339)
	rec.Pack["status"] = "published"
	if err := s.st.Put("packs", k, rec); err != nil {
		httpx.Internal(w, "%v", err)
		return
	}
	claims, _ := auth.FromContext(r.Context())
	env, err := envelope.New("nrs.rulepacks.published.v1", service, claims.TenantID, k, map[string]any{
		"pack_id":              rec.ID,
		"version":              rec.Version,
		"ref":                  k,
		"sha256":               rec.SHA256,
		"effective_from":       rec.EffectiveFrom,
		"subject_to_regazette": rec.SubjectToRegazette,
		"provenance":           rec.Provenance,
		"published_by":         claims.Sub,
	})
	if err == nil && s.out != nil {
		if err := s.out.Append("nrs.rulepacks.published.v1", env); err != nil {
			log.Printf("outbox append: %v", err)
		}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"pack": rec, "event": "nrs.rulepacks.published.v1"})
}

type consumerReq struct {
	Consumer string `json:"consumer"`
	PackID   string `json:"pack_id"`
	Version  string `json:"version"`
}

func (s *server) registerConsumer(w http.ResponseWriter, r *http.Request) {
	var req consumerReq
	if err := httpx.Decode(r, &req); err != nil || req.Consumer == "" || req.PackID == "" || req.Version == "" {
		httpx.BadRequest(w, "consumer, pack_id and version are required")
		return
	}
	pin := ConsumerPin{Consumer: req.Consumer, PackID: req.PackID, Version: req.Version,
		PinnedAt: time.Now().UTC().Format(time.RFC3339)}
	if err := s.st.Put("consumers", req.Consumer+":"+req.PackID, pin); err != nil {
		httpx.Internal(w, "%v", err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"consumer": pin})
}

func (s *server) staleConsumers(w http.ResponseWriter, r *http.Request) {
	var pins []ConsumerPin
	if err := s.st.ListInto("consumers", &pins); err != nil {
		httpx.Internal(w, "%v", err)
		return
	}
	type stale struct {
		Consumer       string `json:"consumer"`
		PackID         string `json:"pack_id"`
		PinnedVersion  string `json:"pinned_version"`
		LatestVersion  string `json:"latest_version"`
		LatestSHA256   string `json:"latest_sha256"`
		StaleSinceDays int    `json:"stale_since_days"`
	}
	out := []stale{}
	for _, p := range pins {
		latest := s.latestPublished(p.PackID)
		if latest == nil || !semverLess(p.Version, latest.Version) {
			continue
		}
		days := 0
		if latest.PublishedAt != "" {
			if t, err := time.Parse(time.RFC3339, latest.PublishedAt); err == nil {
				days = int(time.Since(t).Hours() / 24)
			}
		}
		out = append(out, stale{p.Consumer, p.PackID, p.Version, latest.Version, latest.SHA256, days})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Consumer < out[j].Consumer })
	httpx.JSON(w, http.StatusOK, map[string]any{"stale": out})
}
