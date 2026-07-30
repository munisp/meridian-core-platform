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
