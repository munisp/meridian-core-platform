// migration — legacy data-migration service (M1).
//
// HTTP API:
//
//	POST /v1/migration/batches?mode=dry_run|commit&strict_checksum=true
//	     multipart form with file parts "taxpayers", "filings", "payments"
//	     (.csv or .jsonl); returns the signed reconciliation manifest.
//	GET  /v1/migration/batches/{id}          -> stored manifest
//	POST /v1/migration/batches/{id}/verify   -> PASS/FAIL proof (JSON)
//	GET  /v1/migration/batches/{id}/proof?format=text -> human summary
//
// Fail-closed: PROFILE=prod requires MIGRATION_SIGNING_KEY and TIN_GRAPH_URL.
// Duplicate and referential checks run against live tin-graph when
// TIN_GRAPH_URL is set (POST /v1/verify/tin); without it the service refuses
// to boot under PROFILE=prod, and in dev the manifest carries an explicit
// [simulated] note.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"

	"github.com/munisp/meridian-core-platform/packages/events/httpx"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/munisp/meridian-core-platform/services/migration/internal/mig"
)

const (
	service = "migration"
	version = "0.1.0"
)

// --- durable JSON document store (atomic writes, dev-durable in DATA_DIR) ---

type jsonStore struct {
	dir string
	mu  sync.Mutex
}

func openJSONStore(dir string) (*jsonStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &jsonStore{dir: dir}, nil
}

func (s *jsonStore) path(coll, id string) string {
	safe := strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == 0 {
			return -1
		}
		return r
	}, id)
	return filepath.Join(s.dir, coll, safe+".json")
}

func (s *jsonStore) Get(coll, id string) (json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := os.ReadFile(s.path(coll, id))
	if os.IsNotExist(err) {
		return nil, mig.ErrNotFound
	}
	return b, err
}

func (s *jsonStore) Put(coll, id string, doc json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.path(coll, id)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, doc, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p) // atomic on POSIX
}

func (s *jsonStore) Delete(coll, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := os.Remove(s.path(coll, id))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (s *jsonStore) List(coll string) ([]json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ents, err := os.ReadDir(filepath.Join(s.dir, coll))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []json.RawMessage
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.dir, coll, e.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

// --- live tin-graph index (HTTP /v1/verify/tin) ---

type httpLiveIndex struct {
	base  string
	token string // optional bearer (tin-graph sits behind auth middleware)
	hc    *http.Client
}

func (h *httpLiveIndex) LookupTIN(tin string) (bool, string, error) {
	body, _ := json.Marshal(map[string]string{"tin": tin})
	req, err := http.NewRequest(http.MethodPost, h.base+"/v1/verify/tin", bytes.NewReader(body))
	if err != nil {
		return false, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if h.token != "" {
		req.Header.Set("Authorization", "Bearer "+h.token)
	}
	resp, err := h.hc.Do(req)
	if err != nil {
		return false, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, "", fmt.Errorf("tin-graph verify status %d", resp.StatusCode)
	}
	var out struct {
		Valid    bool   `json:"valid"`
		EntityID string `json:"entity_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return false, "", err
	}
	return out.Valid, out.EntityID, nil
}

// --- server ---

type server struct {
	st    *jsonStore
	live  mig.LiveIndex
	key   []byte
	keyID string
}

func signingKey() ([]byte, string) {
	k := os.Getenv("MIGRATION_SIGNING_KEY")
	if k == "" {
		if os.Getenv("PROFILE") == "prod" {
			log.Fatal("profile=prod FATAL: MIGRATION_SIGNING_KEY is required (fail-closed, no dev default)")
		}
		return []byte("meridian-dev-migration-signing-key"), "dev-hmac [simulated]"
	}
	return []byte(k), "env:migration-signing-key"
}

func main() {
	dir := env("DATA_DIR", "./data")
	st, err := openJSONStore(dir)
	if err != nil {
		log.Fatal(err)
	}
	var live mig.LiveIndex
	if base := os.Getenv("TIN_GRAPH_URL"); base != "" {
		live = &httpLiveIndex{base: strings.TrimRight(base, "/"), token: os.Getenv("TIN_GRAPH_TOKEN"), hc: &http.Client{Timeout: 10 * time.Second}}
		log.Printf("live tin-graph checks via %s", base)
	} else {
		if os.Getenv("PROFILE") == "prod" {
			log.Fatal("profile=prod FATAL: TIN_GRAPH_URL is required — refusing to run migration with duplicate/referential checks skipped (fail-closed)")
		}
		log.Printf("TIN_GRAPH_URL unset: live duplicate/referential checks skipped [simulated]")
	}
	key, keyID := signingKey()
	s := &server{st: st, live: live, key: key, keyID: keyID}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": service, "version": version})
	})
	mux.HandleFunc("POST /v1/migration/batches", s.importBatch)
	mux.HandleFunc("GET /v1/migration/batches/{id}", s.getManifest)
	mux.HandleFunc("POST /v1/migration/batches/{id}/verify", s.verifyBatch)
	mux.HandleFunc("GET /v1/migration/batches/{id}/proof", s.getProof)

	addr := ":" + env("PORT", "8011")
	log.Printf("%s %s on %s (key=%s)", service, version, addr, keyID)
	// F-5: graceful shutdown on SIGTERM/SIGINT + full server timeouts.
	log.Fatal(httpx.ListenAndServe(addr, mux))
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func problem(w http.ResponseWriter, status int, title, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"type": "about:blank", "title": title, "status": status, "detail": detail})
}

// importBatch handles POST /v1/migration/batches — multipart parts named
// taxpayers/filings/payments; format inferred from filename (.csv/.jsonl).
func (s *server) importBatch(w http.ResponseWriter, r *http.Request) {
	mode := mig.Mode(r.URL.Query().Get("mode"))
	if mode == "" {
		mode = mig.DryRun
	}
	strict := r.URL.Query().Get("strict_checksum") == "true"
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		problem(w, http.StatusBadRequest, "bad_request", "multipart form required: "+err.Error())
		return
	}
	var files []mig.SourceFile
	for _, part := range []string{"taxpayers", "filings", "payments"} {
		f, hdr, err := r.FormFile(part)
		if err != nil {
			continue // entity file not present in this batch
		}
		defer f.Close()
		format := ""
		switch {
		case strings.HasSuffix(hdr.Filename, ".csv"):
			format = "csv"
		case strings.HasSuffix(hdr.Filename, ".jsonl"), strings.HasSuffix(hdr.Filename, ".ndjson"):
			format = "jsonl"
		default:
			problem(w, http.StatusBadRequest, "bad_request", hdr.Filename+": extension must be .csv or .jsonl")
			return
		}
		data, err := io.ReadAll(f)
		if err != nil {
			problem(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		files = append(files, mig.SourceFile{Entity: mig.EntityKind(part), Format: format, Name: hdr.Filename, R: bytes.NewReader(data)})
	}
	res, err := mig.Import(mig.Deps{Store: s.st, Live: s.live, SigningKey: s.key, KeyID: s.keyID},
		files, mig.Options{Mode: mode, StrictChecksum: strict}, time.Now())
	if err != nil {
		problem(w, http.StatusBadRequest, "import_failed", err.Error())
		return
	}
	status := http.StatusOK
	if mode == mig.Commit {
		status = http.StatusCreated
	}
	writeJSON(w, status, res)
}

func (s *server) loadManifest(w http.ResponseWriter, id string) *mig.Manifest {
	raw, err := s.st.Get("mig_batches", id)
	if err != nil {
		problem(w, http.StatusNotFound, "not_found", "batch "+id)
		return nil
	}
	var m mig.Manifest
	if json.Unmarshal(raw, &m) != nil {
		problem(w, http.StatusInternalServerError, "internal_error", "corrupt manifest "+id)
		return nil
	}
	return &m
}

func (s *server) getManifest(w http.ResponseWriter, r *http.Request) {
	if m := s.loadManifest(w, r.PathValue("id")); m != nil {
		writeJSON(w, http.StatusOK, m)
	}
}

// verifyBatch re-computes the reconciliation against the live store and
// emits the PASS/FAIL proof document.
func (s *server) verifyBatch(w http.ResponseWriter, r *http.Request) {
	m := s.loadManifest(w, r.PathValue("id"))
	if m == nil {
		return
	}
	proof := mig.Verify(s.st, m, s.key, time.Now())
	status := http.StatusOK
	if proof.Verdict == "FAIL" {
		status = http.StatusConflict // reconciliation failure is signalled loudly
	}
	writeJSON(w, status, proof)
}

func (s *server) getProof(w http.ResponseWriter, r *http.Request) {
	m := s.loadManifest(w, r.PathValue("id"))
	if m == nil {
		return
	}
	proof := mig.Verify(s.st, m, s.key, time.Now())
	if r.URL.Query().Get("format") == "text" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(proof.Summary))
		return
	}
	writeJSON(w, http.StatusOK, proof)
}
