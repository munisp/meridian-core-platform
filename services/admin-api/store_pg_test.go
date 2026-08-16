package main

// store_pg_test.go — regression for the r9 pentest boot defect: admin-api
// must resolve its runtime DSN through the per-service-role substitution
// (DB_USER) and run boot DDL through the migration role (DB_MIGRATE_USER),
// matching the 0003/0004 least-privilege split. Pre-fix, databaseURL()
// returned DATABASE_URL verbatim, so booting as svc_admin_api attempted
// CREATE TABLE with CREATE revoked and the service failed closed.

import (
	"os"
	"strings"
	"testing"
)

func TestDatabaseURLSubstitutesPerServiceRole(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgresql://shared@db.example:5432/meridian")
	t.Setenv("DB_USER", "svc_admin_api")
	t.Setenv("DB_PASSWORD", "s3cret")
	t.Setenv("PROFILE", "dev")
	got := databaseURL()
	if !strings.Contains(got, "svc_admin_api") {
		t.Fatalf("runtime DSN must use the per-service role, got %q", got)
	}
	if strings.Contains(got, "shared@") {
		t.Fatalf("shared credentials must be replaced, got %q", got)
	}
}

func TestDatabaseURLEmptyWithoutDatabaseURL(t *testing.T) {
	os.Unsetenv("DATABASE_URL")
	if got := databaseURL(); got != "" {
		t.Fatalf("expected empty DSN without DATABASE_URL, got %q", got)
	}
}

func TestOpenPgUsersNilWithoutDatabaseURL(t *testing.T) {
	os.Unsetenv("DATABASE_URL")
	pg, err := openPgUsers()
	if err != nil || pg != nil {
		t.Fatalf("expected (nil, nil) without DATABASE_URL, got (%v, %v)", pg, err)
	}
}
