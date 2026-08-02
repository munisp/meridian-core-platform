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

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,PATCH,DELETE,OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization,Content-Type,X-Dev-Role")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
