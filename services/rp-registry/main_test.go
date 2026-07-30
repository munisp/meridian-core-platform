package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/munisp/meridian-core-platform/packages/events/auth"
	"github.com/munisp/meridian-core-platform/packages/events/store"
)

const packV1 = `
id: rp-test-pack
version: 1.0.0
effective_from: 2025-01-01
effective_to: null
status: draft
subject_to_regazette: true
provenance:
  as_passed: "passed"
  as_gazetted: null
  source_citation: "Test Regulations 2025"
rules:
  - id: test.rule
    when: { a: 1 }
    then: { rate_bps: 100 }
`

const packV2 = `
id: rp-test-pack
version: 1.1.0
effective_from: 2025-02-01
effective_to: null
status: draft
subject_to_regazette: true
provenance:
  as_passed: "passed"
  as_gazetted: null
  source_citation: "Test Regulations 2025"
rules:
  - id: test.rule
    when: { a: 1 }
    then: { rate_bps: 200 }
`

func newTestServer(t *testing.T) (*server, http.Handler) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := &server{st: st}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/packs", s.registerPack)
	mux.HandleFunc("GET /v1/packs", s.listPacks)
	mux.HandleFunc("GET /v1/packs/{id}/latest", s.getLatest)
	mux.HandleFunc("GET /v1/packs/{id}/{version}", s.getPack)
	mux.HandleFunc("POST /v1/packs/{id}/{version}/publish", auth.RequireRole("board", s.publishPack))
	mux.HandleFunc("POST /v1/consumers", s.registerConsumer)
	mux.HandleFunc("GET /v1/consumers/stale", s.staleConsumers)
	return s, auth.Middleware(mux)
}

func do(t *testing.T, h http.Handler, method, path, body, role string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *strings.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	} else {
		rdr = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, rdr)
	if role != "" {
		req.Header.Set("X-Dev-Role", role)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRegistryLifecycle(t *testing.T) {
	_, h := newTestServer(t)

	// register v1
	body, _ := json.Marshal(map[string]string{"yaml": packV1})
	rec := do(t, h, "POST", "/v1/packs", string(body), "operator")
	if rec.Code != http.StatusCreated {
		t.Fatalf("register v1: %d %s", rec.Code, rec.Body.String())
	}
	// idempotent re-register
	rec = do(t, h, "POST", "/v1/packs", string(body), "operator")
	if rec.Code != http.StatusOK {
		t.Fatalf("re-register: %d", rec.Code)
	}
	// conflicting content for same id@version
	bad := strings.Replace(packV1, "rate_bps: 100", "rate_bps: 300", 1)
	body, _ = json.Marshal(map[string]string{"yaml": bad})
	if rec = do(t, h, "POST", "/v1/packs", string(body), "operator"); rec.Code != http.StatusConflict {
		t.Fatalf("conflict: %d", rec.Code)
	}
	// invalid pack rejected
	body, _ = json.Marshal(map[string]string{"yaml": "id: NOPE\nversion: 1\n"})
	if rec = do(t, h, "POST", "/v1/packs", string(body), "operator"); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid pack: %d", rec.Code)
	}
	// latest before publish -> 404
	if rec = do(t, h, "GET", "/v1/packs/rp-test-pack/latest", "", "operator"); rec.Code != http.StatusNotFound {
		t.Fatalf("latest pre-publish: %d", rec.Code)
	}
	// publish requires board role
	if rec = do(t, h, "POST", "/v1/packs/rp-test-pack/1.0.0/publish", "", "operator"); rec.Code != http.StatusForbidden {
		t.Fatalf("publish as operator: %d", rec.Code)
	}
	rec = do(t, h, "POST", "/v1/packs/rp-test-pack/1.0.0/publish", "", "board")
	if rec.Code != http.StatusOK {
		t.Fatalf("publish as board: %d %s", rec.Code, rec.Body.String())
	}
	// latest now resolves
	rec = do(t, h, "GET", "/v1/packs/rp-test-pack/latest", "", "auditor")
	if rec.Code != http.StatusOK {
		t.Fatalf("latest: %d", rec.Code)
	}
	var latest struct {
		Version string `json:"version"`
	}
	json.Unmarshal(rec.Body.Bytes(), &latest)
	if latest.Version != "1.0.0" {
		t.Fatalf("latest version = %s", latest.Version)
	}
	// register + publish v2, latest flips
	body, _ = json.Marshal(map[string]string{"yaml": packV2})
	do(t, h, "POST", "/v1/packs", string(body), "operator")
	do(t, h, "POST", "/v1/packs/rp-test-pack/1.1.0/publish", "", "board")
	rec = do(t, h, "GET", "/v1/packs/rp-test-pack/latest", "", "auditor")
	json.Unmarshal(rec.Body.Bytes(), &latest)
	if latest.Version != "1.1.0" {
		t.Fatalf("latest after v2 = %s", latest.Version)
	}
	// stale consumers
	do(t, h, "POST", "/v1/consumers", `{"consumer":"wht-svc","pack_id":"rp-test-pack","version":"1.0.0"}`, "operator")
	do(t, h, "POST", "/v1/consumers", `{"consumer":"fresh-svc","pack_id":"rp-test-pack","version":"1.1.0"}`, "operator")
	rec = do(t, h, "GET", "/v1/consumers/stale", "", "auditor")
	var staleResp struct {
		Stale []struct {
			Consumer      string `json:"consumer"`
			PinnedVersion string `json:"pinned_version"`
			LatestVersion string `json:"latest_version"`
		} `json:"stale"`
	}
	json.Unmarshal(rec.Body.Bytes(), &staleResp)
	if len(staleResp.Stale) != 1 || staleResp.Stale[0].Consumer != "wht-svc" || staleResp.Stale[0].LatestVersion != "1.1.0" {
		t.Fatalf("stale: %+v", staleResp.Stale)
	}
}

func TestGetFullPack(t *testing.T) {
	_, h := newTestServer(t)
	body, _ := json.Marshal(map[string]string{"yaml": packV1})
	do(t, h, "POST", "/v1/packs", string(body), "operator")
	rec := do(t, h, "GET", "/v1/packs/rp-test-pack/1.0.0", "", "auditor")
	if rec.Code != http.StatusOK {
		t.Fatalf("get: %d", rec.Code)
	}
	var resp struct {
		SHA256 string         `json:"sha256"`
		Pack   map[string]any `json:"pack"`
		YAML   string         `json:"yaml"`
	}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.SHA256) != 64 || resp.Pack["id"] != "rp-test-pack" || resp.YAML == "" {
		t.Fatalf("bad full pack: %+v", resp)
	}
}
