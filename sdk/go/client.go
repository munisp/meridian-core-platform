// Package meridian provides thin, hand-written typed clients for the
// Meridian platform services (see api/*.yaml). Generate-free by design:
// the typed structs below are the contract, reviewed against the specs.
//
// Currently covered (highest-value services first): onboarding, tin-graph,
// ledger. Every mutating call supports idempotency keys; NewIdempotencyKey
// issues a unique key.
package meridian

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client is the shared HTTP core for all service clients.
type Client struct {
	BaseURL    string
	HTTPClient *http.Client
	// DevRole is sent as X-Dev-Role in dev-profile deployments.
	DevRole string
	// Token is a bearer token (prod profile, Keycloak).
	Token string
}

// NewClient builds a client for a base URL (e.g. "http://localhost:8101").
func NewClient(baseURL string) *Client {
	return &Client{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		HTTPClient: &http.Client{Timeout: 15 * time.Second},
		DevRole:    "operator",
	}
}

// NewIdempotencyKey returns a unique idempotency key (crypto-random hex).
func NewIdempotencyKey() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return "idem-" + hex.EncodeToString(b)
}

// Problem is the RFC-7807-ish error body used across services.
type Problem struct {
	Status int    `json:"status"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
}

func (p *Problem) Error() string {
	return fmt.Sprintf("meridian: %d %s: %s", p.Status, p.Title, p.Detail)
}

// request executes one JSON call. idempotencyKey (optional) is sent as the
// Idempotency-Key header on mutating calls.
func (c *Client) request(ctx context.Context, method, path string, body, out any, idempotencyKey string) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.DevRole != "" {
		req.Header.Set("X-Dev-Role", c.DevRole)
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		p := &Problem{Status: resp.StatusCode, Title: http.StatusText(resp.StatusCode)}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		var parsed Problem
		if json.Unmarshal(b, &parsed) == nil && parsed.Title != "" {
			p.Title = parsed.Title
			p.Detail = parsed.Detail
		} else {
			p.Detail = strings.TrimSpace(string(b))
		}
		return p
	}
	if out == nil {
		io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	return c.request(ctx, http.MethodGet, path, nil, out, "")
}

func (c *Client) post(ctx context.Context, path string, body, out any, idempotencyKey string) error {
	return c.request(ctx, http.MethodPost, path, body, out, idempotencyKey)
}

func (c *Client) patch(ctx context.Context, path string, body, out any) error {
	return c.request(ctx, http.MethodPatch, path, body, out, "")
}
