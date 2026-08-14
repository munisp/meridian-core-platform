package main

import (
	"strings"
	"testing"
)

func TestRenderAPISIX(t *testing.T) {
	y := renderAPISIX(defaultRoutes, "enforce")
	for _, want := range []string{
		"routes:", "id: core-ledger-transfers", "uris:", "/v1/transfers/*",
		"jwt-auth: {}", "upstream:", "ua-restriction", "#END",
	} {
		if !strings.Contains(y, want) {
			t.Fatalf("rendered YAML missing %q:\n%s", want, y)
		}
	}
	y2 := renderAPISIX(defaultRoutes, "detect")
	if strings.Contains(y2, "ua-restriction") {
		t.Fatal("detect mode must not enforce WAF plugin")
	}
}

// TestRenderAPISIXWAFEnforce asserts the real WAF protections render on every
// route in enforce mode: SQLi/XSS/path-traversal blocking via
// serverless-pre-function and the expanded scanner-UA blocklist.
func TestRenderAPISIXWAFEnforce(t *testing.T) {
	y := renderAPISIX(defaultRoutes, "enforce")
	for _, want := range []string{
		"serverless-pre-function", "phase: rewrite",
		"ngx.exit(403)",                                // blocking action
		"union%s+select",                               // SQLi signature
		"<%s*script",                                   // XSS signature
		"%.%./",                                        // path-traversal signature
		"sqlmap", "nikto", "nmap", "masscan", "nuclei", // scanner UA denylist
	} {
		if !strings.Contains(y, want) {
			t.Fatalf("enforce render missing WAF artifact %q", want)
		}
	}
	// Every route must carry the WAF block, not just one.
	routes := strings.Count(y, "    upstream:\n")
	if got := strings.Count(y, "serverless-pre-function"); got != routes {
		t.Fatalf("serverless-pre-function on %d/%d routes", got, routes)
	}
	if got := strings.Count(y, "ua-restriction"); got != routes {
		t.Fatalf("ua-restriction on %d/%d routes", got, routes)
	}
}

// TestRenderAPISIXWAFNotEnforced asserts no WAF plugins render in shadow
// (detect) or any non-enforce mode.
func TestRenderAPISIXWAFNotEnforced(t *testing.T) {
	for _, mode := range []string{"detect", "shadow", "disabled", ""} {
		y := renderAPISIX(defaultRoutes, mode)
		for _, banned := range []string{"serverless-pre-function", "ua-restriction", "sqlmap"} {
			if strings.Contains(y, banned) {
				t.Fatalf("mode %q must not render WAF plugin %q", mode, banned)
			}
		}
	}
}

func TestSplitUpstream(t *testing.T) {
	h, p := splitUpstream("ledger:8010")
	if h != "ledger" || p != "8010" {
		t.Fatalf("%s %s", h, p)
	}
	h, p = splitUpstream("nohost")
	if p != "80" {
		t.Fatalf("%s %s", h, p)
	}
}

func TestEveryRouteHasUpstreamAndPath(t *testing.T) {
	for _, r := range defaultRoutes {
		if r.Upstream == "" || r.PathPrefix == "" || len(r.Methods) == 0 || r.Plane == "" {
			t.Fatalf("bad route: %+v", r)
		}
	}
}

// Regression (F-1, W4 HIGH): rate limiting must be enforced, not
// documentation-only. Every rendered route (both detect and enforce mode)
// must carry a limit-req plugin, and the rendered config must reference the
// plugins APISIX loads.
func TestRenderAPISIXRateLimitPerRoute(t *testing.T) {
	for _, mode := range []string{"detect", "enforce"} {
		y := renderAPISIX(defaultRoutes, mode)
		if !strings.Contains(y, "limit-req:") {
			t.Fatalf("mode=%s: rendered config must reference the limit-req plugin", mode)
		}
		// one limit-req block per route
		got := strings.Count(y, "limit-req:")
		if got != len(defaultRoutes) {
			t.Fatalf("mode=%s: want limit-req on every route (%d), got %d", mode, len(defaultRoutes), got)
		}
		if !strings.Contains(y, "rejected_code: 429") {
			t.Fatalf("mode=%s: limit-req must reject with 429", mode)
		}
	}
}

// OTP routes additionally get limit-count (100/day per SPEC B §3).
func TestRenderAPISIXOTPClass(t *testing.T) {
	routes := append([]RouteSpec(nil), defaultRoutes...)
	routes = append(routes, RouteSpec{
		ID: "core-otp", Plane: "core", Name: "otp", PathPrefix: "/v1/auth/otp",
		Methods: []string{"POST"}, Upstream: "admin-api:8095", Service: "admin-api", Auth: true,
	})
	y := renderAPISIX(routes, "enforce")
	if !strings.Contains(y, "limit-count:") || !strings.Contains(y, "count: 100") || !strings.Contains(y, "time_window: 86400") {
		t.Fatalf("OTP class must render limit-count 100/day:\n%s", y)
	}
}

// Route-class policy resolution: critical vs sheddable vs default.
func TestLimitPolicyFor(t *testing.T) {
	cases := []struct {
		prefix, method, loadClass, key string
	}{
		{"/v1/filings", "POST", "critical", "http_x_tin"},
		{"/v1/payments", "POST", "critical", "http_x_tin"},
		{"/v1/exports", "POST", "sheddable-batch", "http_x_tin"},
		{"/v1/search/autocomplete", "GET", "sheddable-search", "remote_addr"},
		{"/v1/anything", "GET", "standard", "remote_addr"},
	}
	for _, c := range cases {
		p := limitPolicyFor(RouteSpec{PathPrefix: c.prefix, Methods: []string{c.method}})
		if p.LoadClass != c.loadClass || p.Key != c.key {
			t.Fatalf("%s %s: got %+v", c.method, c.prefix, p)
		}
	}
	// OTP class carries limit-count.
	p := limitPolicyFor(RouteSpec{PathPrefix: "/v1/auth/otp", Methods: []string{"POST"}})
	if p.Count != 100 || p.TimeWindow != 86400 {
		t.Fatalf("OTP policy: %+v", p)
	}
}
