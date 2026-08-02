// client.go — live Permify server client (Check API v1).
//
// Selected by PERMIFY_URL; when unset, callers keep the dev file-backed
// Checker (checker.go) and log that fact honestly. PROFILE=prod without
// PERMIFY_URL must fail closed at startup (enforced by each service's
// wiring, e.g. admin-api permify.go, tin-graph permify_gate.go).
//
// Contract: POST {PERMIFY_URL}/v1/tenants/{tenant}/permissions/check
//   {"entity":{"type","id"},"permission":p,"subject":{"type","id"}}
//   -> {"can":"RESULT_ALLOWED"|"RESULT_DENIED"}
// 2s timeout, ONE retry on 5xx, every failure is circuit-logged.
package permifymodels

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// Permission names checked by Meridian call sites. Every name here MUST
// exist in schemas/*.perm — enforced by schema_test.go.
const (
	PermTenantManage  = "manage"  // tenant: admin only
	PermTenantOperate = "operate" // tenant: admin + operator (tin-graph officer scope)
	PermTenantRead    = "read"    // tenant: admin + operator + auditor
	PermTenantGovern  = "govern"  // tenant: admin + board (pack publish / gate flips)
)

// rolePermission maps an admin-api RBAC role to the tenant permission a
// live Permify check must satisfy for that role.
var rolePermission = map[string]string{
	"admin":    PermTenantManage,
	"operator": PermTenantOperate,
	"auditor":  PermTenantRead,
	"board":    PermTenantGovern,
}

// RolePermission returns the Permify tenant permission equivalent of an
// RBAC role, and whether the role has a mapping.
func RolePermission(role string) (string, bool) {
	p, ok := rolePermission[role]
	return p, ok
}

// Client is a thin stdlib HTTP client for the Permify Check API.
type Client struct {
	base    string // PERMIFY_URL, no trailing slash
	tenant  string // Permify tenant id (PERMIFY_TENANT, default "t1")
	hc      *http.Client
	timeout time.Duration
	logf    func(format string, args ...any)
}

// NewClient builds a client for a Permify server at baseURL.
func NewClient(baseURL, tenant string) *Client {
	if tenant == "" {
		tenant = "t1"
	}
	return &Client{
		base:    strings.TrimRight(baseURL, "/"),
		tenant:  tenant,
		hc:      &http.Client{},
		timeout: 2 * time.Second,
		logf:    log.Printf,
	}
}

// NewClientFromEnv returns a live client when PERMIFY_URL is set, else nil
// (caller falls back to the dev file-backed checker).
func NewClientFromEnv() *Client {
	base := os.Getenv("PERMIFY_URL")
	if base == "" {
		return nil
	}
	return NewClient(base, os.Getenv("PERMIFY_TENANT"))
}

type ref struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type checkRequest struct {
	Entity     ref    `json:"entity"`
	Permission string `json:"permission"`
	Subject    ref    `json:"subject"`
}

type checkResponse struct {
	Can     string `json:"can"`     // Permify v1: RESULT_ALLOWED / RESULT_DENIED
	Allowed *bool  `json:"allowed"` // tolerate simpler proxies
}

// splitRef parses "type:id" into a Permify entity/subject reference.
func splitRef(s string) (ref, error) {
	i := strings.Index(s, ":")
	if i <= 0 || i == len(s)-1 {
		return ref{}, fmt.Errorf("permify reference %q must be type:id", s)
	}
	return ref{Type: s[:i], ID: s[i+1:]}, nil
}

// Check reports whether subject holds entity#permission on the Permify
// server. One retry on 5xx; transport errors and non-2xx statuses are
// circuit-logged and returned as errors (callers fail closed).
func (c *Client) Check(ctx context.Context, entity, permission, subject string) (bool, error) {
	ent, err := splitRef(entity)
	if err != nil {
		return false, err
	}
	sub, err := splitRef(subject)
	if err != nil {
		return false, err
	}
	body, _ := json.Marshal(checkRequest{Entity: ent, Permission: permission, Subject: sub})
	url := c.base + "/v1/tenants/" + c.tenant + "/permissions/check"

	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		allowed, retryable, err := c.do(ctx, url, body)
		if err == nil {
			return allowed, nil
		}
		lastErr = err
		c.logf("component=permify circuit: check %s#%s@%s attempt %d failed: %v", entity, permission, subject, attempt+1, err)
		if !retryable {
			break
		}
	}
	return false, lastErr
}

// do performs one check attempt. retryable is true only for 5xx.
func (c *Client) do(ctx context.Context, url string, body []byte) (allowed, retryable bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return false, false, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return false, false, fmt.Errorf("permify check transport: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 500 {
		return false, true, fmt.Errorf("permify check status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if resp.StatusCode != http.StatusOK {
		return false, false, fmt.Errorf("permify check status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out checkResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return false, false, fmt.Errorf("permify check decode: %w", err)
	}
	if out.Allowed != nil {
		return *out.Allowed, false, nil
	}
	return out.Can == "RESULT_ALLOWED", false, nil
}
