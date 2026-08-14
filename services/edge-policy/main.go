// edge-policy — APISIX route policy distribution (SPEC 2).
// Generates the APISIX route table YAML per plane from the service registry
// and manages the WAF mode (detect|enforce), persisted across restarts.
package main

import (
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"

	"github.com/munisp/meridian-core-platform/packages/events/auth"
	"github.com/munisp/meridian-core-platform/packages/events/httpx"
	"github.com/munisp/meridian-core-platform/packages/events/store"
)

const (
	service = "edge-policy"
	version = "0.1.0"
)

// RouteSpec is one edge route: public path prefix -> upstream service.
type RouteSpec struct {
	ID         string   `json:"id"`
	Plane      string   `json:"plane"` // core|market|sovereign
	Name       string   `json:"name"`
	PathPrefix string   `json:"path_prefix"`
	Methods    []string `json:"methods"`
	Upstream   string   `json:"upstream"` // host:port
	Service    string   `json:"service"`
	Auth       bool     `json:"auth"`
}

// defaultRoutes is the core route table (planes append their own entries
// via ROUTES_EXTRA_JSON in deployments).
var defaultRoutes = []RouteSpec{
	{ID: "core-rp-registry", Plane: "core", Name: "rp-registry", PathPrefix: "/v1/packs", Methods: []string{"GET", "POST"}, Upstream: "rp-registry:8002", Service: "rp-registry", Auth: true},
	{ID: "core-rp-consumers", Plane: "core", Name: "rp-registry-consumers", PathPrefix: "/v1/consumers", Methods: []string{"GET", "POST"}, Upstream: "rp-registry:8002", Service: "rp-registry", Auth: true},
	{ID: "core-tin-graph", Plane: "core", Name: "tin-graph", PathPrefix: "/v1/tin", Methods: []string{"POST"}, Upstream: "tin-graph:8003", Service: "tin-graph", Auth: true},
	{ID: "core-tin-verify", Plane: "core", Name: "tin-graph-verify", PathPrefix: "/v1/verify", Methods: []string{"POST"}, Upstream: "tin-graph:8003", Service: "tin-graph", Auth: true},
	{ID: "core-tin-entities", Plane: "core", Name: "tin-graph-entities", PathPrefix: "/v1/entities", Methods: []string{"GET", "POST"}, Upstream: "tin-graph:8003", Service: "tin-graph", Auth: true},
	{ID: "core-rules-engine", Plane: "core", Name: "rules-engine", PathPrefix: "/v1/evaluate", Methods: []string{"POST"}, Upstream: "rules-engine:8001", Service: "rules-engine", Auth: true},
	{ID: "core-ledger-accounts", Plane: "core", Name: "ledger-accounts", PathPrefix: "/v1/accounts", Methods: []string{"GET", "POST"}, Upstream: "ledger:8010", Service: "ledger", Auth: true},
	{ID: "core-ledger-transfers", Plane: "core", Name: "ledger-transfers", PathPrefix: "/v1/transfers", Methods: []string{"GET", "POST"}, Upstream: "ledger:8010", Service: "ledger", Auth: true},
	{ID: "core-audit", Plane: "core", Name: "audit-evidence", PathPrefix: "/v1/audit", Methods: []string{"GET", "POST"}, Upstream: "audit-evidence:8004", Service: "audit-evidence", Auth: true},
	{ID: "core-evidence", Plane: "core", Name: "evidence-worm", PathPrefix: "/v1/evidence", Methods: []string{"GET", "POST"}, Upstream: "audit-evidence:8004", Service: "audit-evidence", Auth: true},
	{ID: "core-tat", Plane: "core", Name: "tat", PathPrefix: "/v1/tat", Methods: []string{"POST"}, Upstream: "audit-evidence:8004", Service: "audit-evidence", Auth: true},
	{ID: "core-geo", Plane: "core", Name: "geo", PathPrefix: "/v1/attribution", Methods: []string{"POST"}, Upstream: "geo:8005", Service: "geo", Auth: true},
	{ID: "core-geo-boundaries", Plane: "core", Name: "geo-boundaries", PathPrefix: "/v1/boundaries", Methods: []string{"GET"}, Upstream: "geo:8005", Service: "geo", Auth: true},
	{ID: "core-notification", Plane: "core", Name: "notification", PathPrefix: "/v1/send", Methods: []string{"POST"}, Upstream: "notification:8006", Service: "notification", Auth: true},
	{ID: "core-consent", Plane: "core", Name: "consent", PathPrefix: "/v1/consents", Methods: []string{"GET", "POST"}, Upstream: "consent:8007", Service: "consent", Auth: true},
	{ID: "core-regwatch", Plane: "core", Name: "reg-watch", PathPrefix: "/v1/gates", Methods: []string{"GET", "POST"}, Upstream: "reg-watch:8011", Service: "reg-watch", Auth: true},
	{ID: "core-features", Plane: "core", Name: "feature-store", PathPrefix: "/v1/features", Methods: []string{"GET", "POST"}, Upstream: "feature-store:8012", Service: "feature-store", Auth: true},
	{ID: "core-settlement", Plane: "core", Name: "settlement", PathPrefix: "/v1/recon", Methods: []string{"GET", "POST"}, Upstream: "settlement:8013", Service: "settlement", Auth: true},
	{ID: "core-search", Plane: "core", Name: "search-indexer", PathPrefix: "/v1/search", Methods: []string{"GET"}, Upstream: "search-indexer:8008", Service: "search-indexer", Auth: true},
	{ID: "core-edge", Plane: "core", Name: "edge-policy", PathPrefix: "/v1/routes", Methods: []string{"GET"}, Upstream: "edge-policy:8009", Service: "edge-policy", Auth: true},
	// market + sovereign plane anchor routes (plane services register their own)
	{ID: "market-einvoicing", Plane: "market", Name: "einvoicing", PathPrefix: "/v1/invoices", Methods: []string{"GET", "POST"}, Upstream: "einvoicing:8101", Service: "einvoicing", Auth: true},
	{ID: "market-wht", Plane: "market", Name: "wht", PathPrefix: "/v1/wht", Methods: []string{"GET", "POST"}, Upstream: "wht:8103", Service: "wht", Auth: true},
	{ID: "market-pos-vat", Plane: "market", Name: "pos-vat", PathPrefix: "/v1/pos", Methods: []string{"GET", "POST"}, Upstream: "pos-vat:8106", Service: "pos-vat", Auth: true},
	{ID: "sovereign-enclave-gateway", Plane: "sovereign", Name: "enclave-gateway", PathPrefix: "/v1/enclave", Methods: []string{"GET", "POST"}, Upstream: "enclave-gateway:8204", Service: "enclave-gateway", Auth: true},
}

type server struct {
	st store.DocStore
}

// wafMode returns the persisted WAF mode (default detect).
func (s *server) wafMode() string {
	var v struct {
		Mode string `json:"mode"`
	}
	if err := s.st.Get("config", "waf_mode", &v); err != nil || (v.Mode != "enforce" && v.Mode != "detect") {
		return "detect"
	}
	return v.Mode
}

// wafUADenylist is the scanner/bot User-Agent blocklist applied to every
// route in enforce mode (WAF class L4). Entries are matched by APISIX
// ua-restriction (Lua pattern semantics against the User-Agent header).
var wafUADenylist = []string{
	"blocked-agent", // legacy entry, kept for backwards compatibility
	"sqlmap", "nikto", "nmap", "masscan", "zgrab", "nuclei",
	"acunetix", "nessus", "openvas", "appscan",
	"hydra", "metasploit", "burp", "wpscan",
	"gobuster", "dirbuster", "dirsearch", "ffuf",
}

// wafServerlessLua is the serverless-pre-function body rendered in enforce
// mode. It matches common SQLi (L1), XSS (L2) and path-traversal (L3)
// signatures against the normalized URI + query args and short-circuits
// with 403 before the request reaches any upstream.
const wafServerlessLua = `return function(conf, ctx)
	local lower = string.lower
	local uri = lower(ngx.unescape_uri(ngx.var.uri or ""))
	local args = lower(ngx.unescape_uri(ngx.var.args or ""))
	local target = uri .. "?" .. args
	local signatures = {
		-- SQLi (L1)
		"union%s+select", "select.+from.+information_schema",
		"or%s+1%s*=%s*1", "and%s+1%s*=%s*1", "'%s*or%s*'",
		"drop%s+table", "sleep%s*%(", "benchmark%s*%(",
		"load_file%s*%(", "into%s+outfile",
		-- XSS (L2)
		"<%s*script", "javascript:", "onerror%s*=", "onload%s*=",
		"onfocus%s*=", "document%.cookie", "document%.location",
		"<%s*iframe", "eval%s*%(",
		-- path traversal (L3)
		"%.%./", "%.%.\\", "etc/passwd", "etc/shadow",
		"boot%.ini", "win%.ini", "proc/self/environ",
	}
	for _, sig in ipairs(signatures) do
		if string.find(target, sig) then
			ngx.log(ngx.WARN, "meridian-waf: blocked request matching ", sig)
			ngx.exit(403)
		end
	end
end`

// limitPolicy is the per-route-class rate-limiting / load-shedding policy
// (F-1: previously documentation-only in infra/apisix/limit-plugins.yaml —
// nothing applied it. renderAPISIX now renders it per route so APISIX
// actually enforces SPEC B §3/§4). Counter keys match the YAML policy:
// per-TIN via the X-TIN header injected from JWT claims, else remote_addr.
type limitPolicy struct {
	// limit-req (leaky bucket per key)
	Rate int // requests/second
	Burst int
	Key  string // APISIX key expression, e.g. remote_addr / http_x_tin
	// limit-count (fixed window); zero disables
	Count      int
	TimeWindow int // seconds
	// api-breaker
	APIBreaker bool
	// X-Load-Class header for load shedding (SPEC B §4)
	LoadClass string
}

// defaultLimitPolicy is the baseline applied to every public route
// (mirrors default_route_plugins in infra/apisix/limit-plugins.yaml).
var defaultLimitPolicy = limitPolicy{Rate: 20, Burst: 40, Key: "remote_addr", APIBreaker: true, LoadClass: "standard"}

// limitClassRules are the route-class overrides, first match wins
// (mirrors the routes table in infra/apisix/limit-plugins.yaml).
var limitClassRules = []struct {
	Prefix string
	Method string // "" = any
	Policy limitPolicy
}{
	// OTP: 100/day per TIN + 2 r/s per IP (SPEC B §3).
	{"/v1/auth/otp", "POST", limitPolicy{Rate: 2, Burst: 4, Key: "remote_addr", Count: 100, TimeWindow: 86400, LoadClass: "standard"}},
	// Filings/payments: 20 r/s per TIN, never shed below the hard cap.
	{"/v1/filings", "POST", limitPolicy{Rate: 20, Burst: 40, Key: "http_x_tin", APIBreaker: true, LoadClass: "critical"}},
	{"/v1/payments", "POST", limitPolicy{Rate: 20, Burst: 40, Key: "http_x_tin", APIBreaker: true, LoadClass: "critical"}},
	// Shed-first classes (SPEC B §4): batch/export, then autocomplete.
	{"/v1/exports", "", limitPolicy{Rate: 5, Burst: 5, Key: "http_x_tin", LoadClass: "sheddable-batch"}},
	{"/v1/search/autocomplete", "GET", limitPolicy{Rate: 50, Burst: 50, Key: "remote_addr", LoadClass: "sheddable-search"}},
}

// limitPolicyFor resolves the rate-limit policy for one route.
func limitPolicyFor(r RouteSpec) limitPolicy {
	for _, rule := range limitClassRules {
		if !strings.HasPrefix(r.PathPrefix, rule.Prefix) {
			continue
		}
		if rule.Method == "" {
			return rule.Policy
		}
		for _, m := range r.Methods {
			if strings.EqualFold(m, rule.Method) {
				return rule.Policy
			}
		}
	}
	return defaultLimitPolicy
}

// renderLimitPlugins writes the rate-limit / load-shedding plugin block for
// one route (F-1).
func renderLimitPlugins(b *strings.Builder, p limitPolicy) {
	fmt.Fprintf(b, "      limit-req:\n        rate: %d\n        burst: %d\n        key: %s\n        rejected_code: 429\n",
		p.Rate, p.Burst, p.Key)
	if p.Count > 0 {
		fmt.Fprintf(b, "      limit-count:\n        count: %d\n        time_window: %d\n        key: http_x_tin\n        rejected_code: 429\n",
			p.Count, p.TimeWindow)
	}
	if p.APIBreaker {
		b.WriteString("      api-breaker:\n        break_response_code: 503\n        unhealthy: {http_statuses: [500], failures: 3}\n        healthy: {successes: 2, timeout: 30}\n")
	}
	fmt.Fprintf(b, "      proxy-rewrite:\n        headers: {X-Load-Class: %s}\n", p.LoadClass)
}

// renderWAFPlugins writes the enforce-mode WAF plugin block for one route.
func renderWAFPlugins(b *strings.Builder) {
	b.WriteString("      ua-restriction:\n        denylist: [\"")
	b.WriteString(strings.Join(wafUADenylist, "\", \""))
	b.WriteString("\"]\n")
	b.WriteString("      serverless-pre-function:\n        phase: rewrite\n        functions:\n          - |\n")
	for _, line := range strings.Split(wafServerlessLua, "\n") {
		b.WriteString("            " + line + "\n")
	}
}

// renderAPISIX renders the route table + WAF mode as APISIX standalone YAML.
func renderAPISIX(routes []RouteSpec, wafMode string) string {
	var b strings.Builder
	b.WriteString("# Generated by meridian edge-policy — APISIX standalone config\n")
	b.WriteString("# WAF mode: " + wafMode + "\n")
	b.WriteString("routes:\n")
	sorted := append([]RouteSpec(nil), routes...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	for _, r := range sorted {
		host, port := splitUpstream(r.Upstream)
		fmt.Fprintf(&b, "  - id: %s\n    name: %s\n    uris:\n      - %s\n      - %s/*\n    methods: [%s]\n",
			r.ID, r.Name, r.PathPrefix, r.PathPrefix, strings.Join(r.Methods, ", "))
		b.WriteString("    plugins:\n")
		if r.Auth {
			b.WriteString("      jwt-auth: {}\n")
		}
		// F-1: rate limiting is enforced per route class, not docs-only.
		renderLimitPlugins(&b, limitPolicyFor(r))
		if wafMode == "enforce" {
			renderWAFPlugins(&b)
		}
		fmt.Fprintf(&b, "    upstream:\n      type: roundrobin\n      nodes:\n        \"%s\": %s\n", host, port)
	}
	b.WriteString("#END\n")
	return b.String()
}

func splitUpstream(u string) (string, string) {
	parts := strings.SplitN(u, ":", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return u, "80"
}

func main() {
	dir := httpx.Env("DATA_DIR", "./data")
	st, err := store.OpenFromEnv(dir)
	if err != nil {
		log.Fatal(err)
	}
	s := &server{st: st}

	mux := http.NewServeMux()
	httpx.RegisterStandard(mux, service, version, nil)
	mux.HandleFunc("GET /v1/routes", s.routes)
	mux.HandleFunc("POST /v1/waf/mode", auth.RequireRole("admin", s.setWAF))
	mux.HandleFunc("GET /v1/waf/mode", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, http.StatusOK, map[string]any{"mode": s.wafMode()})
	})

	addr := ":" + httpx.Port("8009")
	log.Printf("%s %s (waf=%s)", service, version, s.wafMode())
	log.Fatal(httpx.ListenAndServe(addr, auth.Middleware(mux)))
}

func (s *server) routes(w http.ResponseWriter, r *http.Request) {
	plane := r.URL.Query().Get("plane")
	format := r.URL.Query().Get("format")
	routes := []RouteSpec{}
	for _, rs := range defaultRoutes {
		if plane == "" || rs.Plane == plane {
			routes = append(routes, rs)
		}
	}
	if format == "json" {
		httpx.JSON(w, http.StatusOK, map[string]any{
			"waf_mode": s.wafMode(), "routes": routes, "count": len(routes),
		})
		return
	}
	w.Header().Set("Content-Type", "application/yaml")
	w.Write([]byte(renderAPISIX(routes, s.wafMode())))
}

func (s *server) setWAF(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mode string `json:"mode"`
	}
	if err := httpx.Decode(r, &req); err != nil || (req.Mode != "detect" && req.Mode != "enforce") {
		httpx.BadRequest(w, "mode must be detect|enforce")
		return
	}
	claims, _ := auth.FromContext(r.Context())
	if err := s.st.Put("config", "waf_mode", map[string]any{
		"mode": req.Mode, "set_by": claims.Sub, "set_at": httpx.Env("HOSTNAME", "dev"),
	}); err != nil {
		httpx.Internal(w, "%v", err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"mode": req.Mode})
}
