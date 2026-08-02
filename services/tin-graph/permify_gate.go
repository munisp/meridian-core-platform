// permify_gate.go — P0: centralized authorization for identity provisioning
// and taxpayer360 scoping via the Permify server.
//
// Env-selected (HARDENING H1/H3 convention):
//   - PERMIFY_URL set   -> live Permify Check API; officer scope becomes
//     tenant:<tenant_id>#operate@user:<sub> checked against the server.
//   - PERMIFY_URL unset -> dev fallback: JWT role claims (nrs:officer/admin),
//     logged honestly at startup.
//   - PROFILE=prod + PERMIFY_URL unset -> startup FAILS CLOSED (same contract
//     as the O8 verification adapters and the C1 consent gate).
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/munisp/meridian-core-platform/packages/events/auth"
	"github.com/munisp/meridian-core-platform/packages/events/httpx"
	permifymodels "github.com/munisp/meridian-core-platform/packages/permify-models"
)

// permChecker is non-nil when PERMIFY_URL selected the live Permify client.
var permChecker *permifymodels.Client

// permifyFromEnv wires the client; returns an error only for the prod
// fail-closed case (handled by main with log.Fatal).
func permifyFromEnv(profile string) (*permifymodels.Client, error) {
	if c := permifymodels.NewClientFromEnv(); c != nil {
		log.Printf("component=tin-graph permify=live url=%s tenant=%s",
			os.Getenv("PERMIFY_URL"), httpx.Env("PERMIFY_TENANT", "t1"))
		return c, nil
	}
	if profile == "prod" {
		return nil, fmt.Errorf("profile=prod FATAL: PERMIFY_URL is required (centralized authz fail-closed; refusing the dev role-claim checker)")
	}
	log.Printf("profile=dev component=tin-graph WARNING: PERMIFY_URL unset; using dev role-claim authorization (Permify not consulted)")
	return nil, nil
}

// canAdministerTIN reports whether the caller may provision identities or
// read arbitrary taxpayer records (audit M-3). Live mode: Permify tenant
// "operate" permission. Dev fallback: nrs:officer/admin role claims.
// Regular taxpayers may only read their OWN record (enforced per-request
// in taxpayer360Handler).
func canAdministerTIN(r *http.Request) bool {
	claims, ok := auth.FromContext(r.Context())
	if !ok {
		return false
	}
	if permChecker == nil {
		return claims.HasRole("nrs:officer") || claims.HasRole("admin")
	}
	if claims.Sub == "" {
		return false
	}
	tenant := claims.TenantID
	if tenant == "" {
		tenant = "core"
	}
	allowed, err := permChecker.Check(r.Context(), "tenant:"+tenant, permifymodels.PermTenantOperate, "user:"+claims.Sub)
	if err != nil {
		// authz backend unreachable: fail closed
		log.Printf("component=tin-graph permify check failed (%v); request denied (fail-closed)", err)
		return false
	}
	return allowed
}
