// permify.go — P0: centralized authorization via the Permify server.
//
// Env-selected like every other Meridian middleware (HARDENING H1/H3):
//   - PERMIFY_URL set    -> live Permify Check API; requireRole verifies
//     tenant:<tenant_id>#<permission>@user:<sub> against the server.
//   - PERMIFY_URL unset  -> dev fallback: JWT role-claim check (hasRole),
//     logged honestly at startup.
//   - PROFILE=prod (or AUTH_MODE != dev) + PERMIFY_URL unset -> startup
//     FAILS CLOSED: no silent decentralization of authz in prod.
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"

	permifymodels "github.com/munisp/meridian-core-platform/packages/permify-models"
)

// permifyFromEnv wires the Permify client. Returns (nil, nil) only in dev.
func permifyFromEnv(authMode string) (*permifymodels.Client, error) {
	if c := permifymodels.NewClientFromEnv(); c != nil {
		log.Printf("component=admin-api permify=live url=%s tenant=%s",
			os.Getenv("PERMIFY_URL"), envOr("PERMIFY_TENANT", "t1"))
		return c, nil
	}
	if os.Getenv("PROFILE") == "prod" || authMode != "dev" {
		return nil, fmt.Errorf("PERMIFY_URL is required when PROFILE=prod or AUTH_MODE != dev (centralized authz fail-closed; refusing to start on the dev role-claim checker)")
	}
	log.Printf("profile=dev component=admin-api WARNING: PERMIFY_URL unset; using dev role-claim authorization (Permify not consulted)")
	return nil, nil
}

// authorizeRole is the requireRole decision: live Permify check when a
// client is wired, else the dev role-claim check. A non-nil error means
// the authz backend failed and the request must be denied fail-closed.
func (a *app) authorizeRole(r *http.Request, role string) (bool, error) {
	c := getClaims(r)
	if a.perm == nil {
		return hasRole(c, role), nil
	}
	if c == nil || c.Sub == "" {
		return false, nil
	}
	perm, ok := permifymodels.RolePermission(role)
	if !ok {
		log.Printf("component=admin-api role %q has no Permify permission mapping; denying", role)
		return false, nil
	}
	tenant := c.TenantID
	if tenant == "" {
		tenant = "core"
	}
	return a.perm.Check(r.Context(), "tenant:"+tenant, perm, "user:"+c.Sub)
}
