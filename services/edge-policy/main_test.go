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
