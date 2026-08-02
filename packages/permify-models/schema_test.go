// schema_test.go — consistency guard: every permission string checked in
// code (this repo's exported constants and the compliance case-mgmt /
// gov enclave-gateway call sites, whose canonical DSL also lives here)
// MUST exist in schemas/*.perm, the schema family synced to the Permify
// server. Add a permission to code without the schema and this fails.
package permifymodels

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"testing"
)

var permRe = regexp.MustCompile(`(?m)^\s*permission\s+([a-z_]+)\s*=`)

// schemaPermissions parses all schemas/*.perm files and returns the set of
// declared permission names.
func schemaPermissions(t *testing.T) map[string]bool {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("schemas", "*.perm"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no schema files found: %v", err)
	}
	set := map[string]bool{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range permRe.FindAllSubmatch(b, -1) {
			set[string(m[1])] = true
		}
	}
	return set
}

func TestSchemaCoversCodePermissions(t *testing.T) {
	set := schemaPermissions(t)

	// Permissions checked by Go code via the exported constants.
	codePerms := []string{PermTenantManage, PermTenantOperate, PermTenantRead, PermTenantGovern}
	// Every RBAC role mapping must resolve to a schema permission.
	for _, role := range []string{"admin", "operator", "auditor", "board"} {
		p, ok := RolePermission(role)
		if !ok {
			t.Errorf("role %q has no permission mapping", role)
			continue
		}
		codePerms = append(codePerms, p)
	}
	// Permissions checked by the compliance case-mgmt call site
	// (matter read/write, doc read/privileged/share — see
	// compliance-suite case-mgmt store.go/handlers.go).
	codePerms = append(codePerms, "read", "write", "privileged", "share")
	// Permissions checked by the gov enclave-gateway call site
	// (scope "flow:<id>:send" / "flow:<id>:read" / "receipts:read").
	codePerms = append(codePerms, "send")

	for _, p := range codePerms {
		if !set[p] {
			t.Errorf("permission %q checked in code but missing from schemas/*.perm", p)
		}
	}
}

func TestSchemaFilesParse(t *testing.T) {
	set := schemaPermissions(t)
	var names []string
	for p := range set {
		names = append(names, p)
	}
	sort.Strings(names)
	t.Logf("schema permissions: %v", names)
	if len(names) == 0 {
		t.Fatal("no permissions parsed from schemas/*.perm")
	}
}
