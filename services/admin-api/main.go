package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sync"

	"github.com/munisp/meridian-core-platform/packages/events/otelx"
	"github.com/munisp/meridian-core-platform/packages/events/httpx"
	"os"
	"time"

	permifymodels "github.com/munisp/meridian-core-platform/packages/permify-models"
	"github.com/munisp/meridian-core-platform/packages/temporal-sdkx"
)

const version = "0.1.0"

type app struct {
	store     *Store
	client    *http.Client // downstream calls, short timeout
	jwtSecret string
	authMode  string
	pg        *pgUsers                 // non-nil when DATABASE_URL is configured (A6)
	perm      *permifymodels.Client    // non-nil when PERMIFY_URL is configured (P0 authz)
	wfRunner  sdkx.Runner              // env-selected workflow runner (temporal | dev-inproc)
	wfExec    map[string]sdkx.Workflow // triggerable def id -> executable workflow
	// B4-6: audit events that failed to reach the WORM audit-evidence
	// service are queued and retried (never silently dropped).
	auditMu    sync.Mutex
	auditQueue []AuditEvent
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func validateAuthConfig(authMode, jwtSecret string) error {
	// A1-04: fail-closed prod gate (mirrors compliance authx.go:359-361).
	// PROFILE=prod refuses AUTH_MODE=dev (or unset, which defaults to dev):
	// the default HS256 secret and the X-Dev-Role header make auth fully
	// forgeable. Prod must run AUTH_MODE=keycloak with an explicit audience.
	if os.Getenv("PROFILE") == "prod" {
		if authMode != "keycloak" {
			return fmt.Errorf("PROFILE=prod refuses AUTH_MODE=%q; configure AUTH_MODE=keycloak (dev secret / X-Dev-Role auth is forgeable)", authMode)
		}
		if os.Getenv("KEYCLOAK_AUDIENCE") == "" {
			return fmt.Errorf("PROFILE=prod requires KEYCLOAK_AUDIENCE (fail-closed)")
		}
	}
	if authMode != "dev" && authMode != "keycloak" {
		return fmt.Errorf("unknown AUTH_MODE %q", authMode)
	}
	if jwtSecret == "meridian-dev-secret-change-me" && os.Getenv("PROFILE") == "prod" {
		return fmt.Errorf("PROFILE=prod refuses the default dev JWT secret")
	}
	// B4-6: prod must never run with the volatile in-memory audit trail as
	// the only sink — every privileged mutation must reach the WORM
	// audit-evidence service.
	if os.Getenv("PROFILE") == "prod" && os.Getenv("AUDIT_EVIDENCE_URL") == "" {
		return fmt.Errorf("PROFILE=prod requires AUDIT_EVIDENCE_URL (refusing in-memory-only audit mode)")
	}
	return nil
}

func main() {
	// OTel bootstrap (DESIGN-CONTRACT): fail-soft — no OTLP endpoint means
	// no-op providers; PROFILE=prod without one logs a loud warning.
	otelProv := otelx.InitProvidersFor(context.Background(), "admin-api", version)
	defer otelProv.Shutdown(context.Background())

	a := &app{
		store:     NewStore(),
		client:    &http.Client{Timeout: 1200 * time.Millisecond},
		jwtSecret: envOr("MERIDIAN_DEV_JWT_SECRET", "meridian-dev-secret-change-me"),
		authMode:  envOr("AUTH_MODE", "dev"),
	}
	if err := validateAuthConfig(a.authMode, a.jwtSecret); err != nil {
		log.Fatalf("component=admin-api FATAL: %v", err)
	}
	// A6: Postgres-backed user persistence when DATABASE_URL is set.
	if pg, err := openPgUsers(); err != nil {
		// prod must not silently degrade to in-mem user state
		if a.authMode != "dev" {
			log.Fatalf("component=admin-api FATAL: DATABASE_URL set but Postgres connect failed (%v); failing closed", err)
		}
		log.Printf("component=admin-api postgres unavailable (%v); dev in-mem fallback", err)
	} else if pg == nil && os.Getenv("PROFILE") == "prod" {
		// F-7: prod must never run user state on the seeded in-mem store.
		log.Fatalf("component=admin-api FATAL: PROFILE=prod requires DATABASE_URL; refusing the in-mem store")
	} else if pg != nil {
		a.pg = pg
		defer pg.conn.Close(context.Background())
		if _, err := pg.hydrate(a.store); err != nil {
			log.Fatalf("component=admin-api FATAL: postgres hydrate failed (%v)", err)
		}
	}
	// P0: Permify centralized authz (fail-closed in prod without PERMIFY_URL).
	perm, err := permifyFromEnv(a.authMode)
	if err != nil {
		log.Fatalf("component=admin-api FATAL: %v", err)
	}
	a.perm = perm
	// apply env URL overrides to the service registry
	a.store.mu.Lock()
	for _, svc := range a.store.Services {
		if svc.URLEnv != "" {
			if v := os.Getenv(svc.URLEnv); v != "" {
				svc.BaseURL = v
			}
		}
	}
	a.store.mu.Unlock()

	// Temporal worker wiring (docs/temporal-migration.md reference): executes
	// triggerable workflow defs through the env-selected sdkx runner.
	a.initWorkflows()

	// B4-6: background retry of WORM audit forwards that failed (queued,
	// never silently dropped).
	go a.auditFlushLoop()

	mux := http.NewServeMux()

	// public
	mux.HandleFunc("GET /healthz", a.handleHealthz)
	mux.HandleFunc("GET /readyz", a.handleHealthz)
	mux.HandleFunc("POST /v1/admin/login", a.handleLogin)

	// authenticated admin surface
	auth := func(p string, h http.HandlerFunc) {
		mux.Handle(p, a.requireAuth(h))
	}
	auth("GET /v1/admin/me", a.handleMe)
	auth("GET /v1/admin/overview", a.handleOverview)

	auth("GET /v1/admin/tenants", a.handleTenants)
	auth("POST /v1/admin/tenants", a.requireRole("operator", a.handleTenantCreate))
	auth("GET /v1/admin/tenants/{id}", a.handleTenantGet)
	auth("PUT /v1/admin/tenants/{id}", a.requireRole("operator", a.handleTenantUpdate))
	auth("DELETE /v1/admin/tenants/{id}", a.requireRole("admin", a.handleTenantDelete))

	auth("GET /v1/admin/users", a.handleUsers)
	auth("POST /v1/admin/users", a.requireRole("admin", a.handleUserCreate))
	auth("PUT /v1/admin/users/{id}", a.requireRole("admin", a.handleUserUpdate))
	auth("DELETE /v1/admin/users/{id}", a.requireRole("admin", a.handleUserDelete))
	auth("GET /v1/admin/identity/relations", a.handleRelations)

	auth("GET /v1/admin/services", a.handleServices)
	auth("POST /v1/admin/services/{id}/toggle", a.requireRole("operator", a.handleServiceToggle))

	auth("GET /v1/admin/packs", a.handlePacks)
	auth("GET /v1/admin/packs/{id}", a.handlePackGet)
	auth("POST /v1/admin/packs/{id}/{ver}/publish", a.requireRole("board", a.handlePackPublish))

	auth("GET /v1/admin/gates", a.handleGates)
	auth("POST /v1/admin/gates/{id}/flip", a.requireRole("board", a.handleGateFlip))
	auth("GET /v1/admin/gazette-watch", a.handleGazetteWatch)

	auth("GET /v1/admin/audit/events", a.handleAuditEvents)
	auth("POST /v1/admin/audit/events", a.requireRole("operator", a.handleAuditAppend)) // A1-11
	auth("GET /v1/admin/evidence", a.handleEvidenceList)
	auth("GET /v1/admin/evidence/{id}", a.handleEvidenceGet)
	auth("POST /v1/admin/evidence", a.requireRole("operator", a.handleEvidenceCreate)) // A1-11
	auth("POST /v1/admin/tat/assemble", a.requireRole("admin", a.handleTATAssemble)) // A1-11

	auth("GET /v1/admin/flows/matrix", a.handleFlowMatrix)
	auth("GET /v1/admin/flows/receipts", a.handleFlowReceipts)
	auth("POST /v1/admin/flows/receipts", a.requireRole("operator", a.handleFlowReceiptAppend)) // A1-11
	auth("GET /v1/admin/flows/forbidden", a.handleForbiddenFlows)

	auth("GET /v1/admin/ledger/accounts", a.handleLedgerAccounts)
	auth("GET /v1/admin/ledger/accounts/{id}/balance", a.handleLedgerBalance)
	// B2-#11: maker/checker separation on ledger money routes — the maker
	// (operator) creates a pending transfer; only a distinct checker role
	// (admin) may post or void it. Same-role maker+checker is rejected.
	auth("POST /v1/admin/ledger/transfers", a.requireRole("operator", a.handleLedgerTransfer))
	auth("POST /v1/admin/ledger/transfers/{id}/post", a.requireRole("admin", a.handleLedgerPost))
	auth("POST /v1/admin/ledger/transfers/{id}/void", a.requireRole("admin", a.handleLedgerVoid))
	auth("GET /v1/admin/ledger/recon-breaks", a.handleReconBreaks)

	auth("GET /v1/admin/workflows", a.handleWorkflows)
	auth("POST /v1/admin/workflows/{id}/trigger", a.requireRole("operator", a.handleWorkflowTrigger))
	auth("GET /v1/admin/workflow-runs", a.handleWorkflowRuns)

	auth("GET /v1/admin/settings/flags", a.handleFlags)
	auth("PUT /v1/admin/settings/flags", a.requireRole("admin", a.handleFlagsUpdate))
	auth("GET /v1/admin/settings/api-keys", a.handleAPIKeys)
	auth("POST /v1/admin/settings/api-keys", a.requireRole("admin", a.handleAPIKeyCreate))
	auth("POST /v1/admin/settings/api-keys/{id}/revoke", a.requireRole("admin", a.handleAPIKeyRevoke))
	auth("GET /v1/admin/settings/notifications", a.handleNotifProviders)
	auth("GET /v1/admin/settings/routes", a.handleRoutes)
	auth("POST /v1/admin/settings/waf-mode", a.requireRole("admin", a.handleWAFMode))

	// reverse-proxy pass-through (SPEC §2)
	mux.Handle("/v1/admin/proxy/{service}/{path...}", a.requireAuth(http.HandlerFunc(a.handleProxy)))

	port := envOr("PORT", "8095")
	log.Printf("admin-api %s listening on :%s (AUTH_MODE=%s)", version, port, a.authMode)
	// F-5: graceful shutdown on SIGTERM/SIGINT + full server timeouts.
	httpx.InitMetrics("admin-api", version)
	httpx.StartMetricsServer()
	log.Fatal(httpx.ListenAndServe(":"+port, withCORS(httpx.Instrument(mux))))
}

func (a *app) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": "admin-api", "version": version})
}

func (a *app) serviceURL(id string) (string, bool) {
	a.store.mu.Lock()
	defer a.store.mu.Unlock()
	svc, ok := a.store.Services[id]
	if !ok {
		return "", false
	}
	return svc.BaseURL, true
}
