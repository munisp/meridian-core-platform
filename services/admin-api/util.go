package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// ---------- RFC7807 problem+json ----------

type problem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail,omitempty"`
}

func writeProblem(w http.ResponseWriter, status int, title, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(problem{
		Type:   "about:blank",
		Title:  title,
		Status: status,
		Detail: detail,
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 4<<20))
	if err := dec.Decode(v); err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid JSON body", err.Error())
		return false
	}
	return true
}

// ---------- ids / time ----------

// newID returns a ULID-ish unique id (not a strict ULID; sortable prefix + random).
func newID(prefix string) string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s-%x-%s", prefix, time.Now().UnixMilli(), hex.EncodeToString(b))
}

func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339) }

// ---------- passwords ----------

// generatePassword returns a cryptographically random password (24 chars,
// base64url). Used instead of any shared/default credential.
func generatePassword() string {
	b := make([]byte, 18)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// seedPersonaPassword resolves the dev seed persona's password:
// ADMIN_SEED_PASSWORD env wins; otherwise a random one is generated.
// In dev it is logged exactly once (at boot) so the persona remains
// loggable; in prod (AUTH_MODE != dev) it is generated and never logged,
// leaving the persona effectively disabled for password login.
func seedPersonaPassword(email string) string {
	if v := os.Getenv("ADMIN_SEED_PASSWORD"); v != "" {
		return v
	}
	pw := generatePassword()
	if envOr("AUTH_MODE", "dev") == "dev" {
		log.Printf("component=admin-api dev-only: generated seed password for %s: %s", email, pw)
	}
	return pw
}

// ---------- downstream fetch with graceful degradation ----------

// fetchJSON GETs url with a short timeout and decodes into out.
// Returns nil on success; error otherwise (caller falls back to dev seed).
func fetchJSON(client *http.Client, url string, out any) error {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("downstream %s returned %d", url, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(out)
}

func postJSON(client *http.Client, url string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = strings.NewReader(string(b))
	}
	req, err := http.NewRequest(http.MethodPost, url, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("downstream %s returned %d: %s", url, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(out)
}

// ---------- CORS ----------

// withCORS applies an explicit origin allowlist (F-2). Previously it set
// Access-Control-Allow-Origin: * unconditionally while allowing the
// Authorization header — any website could issue credentialed cross-origin
// reads to admin-api from a browser.
//
// Policy:
//   - CORS_ALLOWED_ORIGINS is a comma-separated allowlist; a matching
//     request Origin is echoed (with Vary: Origin) and may send
//     Authorization.
//   - PROFILE=prod REQUIRES CORS_ALLOWED_ORIGINS (fail-closed at startup);
//     wildcard is never allowed with Authorization.
//   - Outside prod an unset allowlist falls back to "*" but WITHOUT the
//     Authorization/X-Dev-Role headers (credentialed dev flows must set the
//     allowlist explicitly, e.g. http://localhost:3000).
func withCORS(next http.Handler) http.Handler {
	prod := os.Getenv("PROFILE") == "prod"
	allow := map[string]bool{}
	for _, o := range strings.Split(os.Getenv("CORS_ALLOWED_ORIGINS"), ",") {
		if o = strings.TrimSpace(o); o != "" && o != "*" {
			allow[o] = true
		}
	}
	if prod && len(allow) == 0 {
		log.Fatalf("component=admin-api FATAL: PROFILE=prod requires CORS_ALLOWED_ORIGINS (explicit origins; wildcard with Authorization refused)")
	}
	allowHeaders := "Content-Type"
	if len(allow) > 0 {
		allowHeaders = "Authorization,Content-Type,X-Dev-Role"
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		switch {
		case origin != "" && allow[origin]:
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
		case !prod && len(allow) == 0:
			// dev wildcard — no credentialed headers allowed
			w.Header().Set("Access-Control-Allow-Origin", "*")
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,PATCH,DELETE,OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", allowHeaders)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// fetchJSONToken / postJSONToken are the ledger-call variants carrying a
// DISTINCT env-injected service token (B2-#12 repair): the maker token
// (MERIDIAN_LEDGER_MAKER_TOKEN / LEDGER_MAKER_TOKEN, ledger:post only) for
// reads and pending-create; the settle token (MERIDIAN_LEDGER_SETTLE_TOKEN
// / LEDGER_SETTLE_TOKEN, ledger:settle only) for post/void. An empty token
// sends no header (dev profile).
func serviceToken(names ...string) string {
	for _, n := range names {
		if v := os.Getenv(n); v != "" {
			return v
		}
	}
	return ""
}

func makerServiceToken() string {
	return serviceToken("MERIDIAN_LEDGER_MAKER_TOKEN", "LEDGER_MAKER_TOKEN")
}

func settleServiceToken() string {
	return serviceToken("MERIDIAN_LEDGER_SETTLE_TOKEN", "LEDGER_SETTLE_TOKEN")
}

func fetchJSONToken(client *http.Client, url, token string, out any) error {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("X-Service-Token", token)
		req.Header.Set("X-Service-Name", "admin-api")
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("downstream %s returned %d", url, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(out)
}

func postJSONToken(client *http.Client, url, token string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = strings.NewReader(string(b))
	}
	req, err := http.NewRequest(http.MethodPost, url, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("X-Service-Token", token)
		req.Header.Set("X-Service-Name", "admin-api")
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("downstream %s returned %d: %s", url, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(out)
}
