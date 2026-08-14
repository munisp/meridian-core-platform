package store

import (
	"strings"
	"testing"
)

// Regression (F-6, W4 HIGH): DATABASE_URL set but unreachable must be a boot
// error — never a silent fallback to the embedded JSON store.
func TestOpenFromEnvUnreachablePostgresFailsClosed(t *testing.T) {
	// Port 1 is reserved and never listens; connection fails fast.
	t.Setenv("DATABASE_URL", "postgres://meridian:meridian@127.0.0.1:1/meridian?connect_timeout=1")
	t.Setenv("PROFILE", "")
	_, err := OpenFromEnv(t.TempDir())
	if err == nil {
		t.Fatal("want startup error when DATABASE_URL is set but Postgres is unreachable; got silent embedded fallback")
	}
	if !strings.Contains(err.Error(), "refusing embedded fallback") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// Regression (F-6): PROFILE=prod without DATABASE_URL must refuse the
// embedded store.
func TestOpenFromEnvProdRequiresDatabaseURL(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("PROFILE", "prod")
	_, err := OpenFromEnv(t.TempDir())
	if err == nil {
		t.Fatal("want startup error for PROFILE=prod without DATABASE_URL")
	}
	if !strings.Contains(err.Error(), "requires DATABASE_URL") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// Embedded store remains allowed in non-prod when DATABASE_URL is unset.
func TestOpenFromEnvEmbeddedAllowedOutsideProd(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("PROFILE", "")
	if _, err := OpenFromEnv(t.TempDir()); err != nil {
		t.Fatalf("embedded store should open outside prod: %v", err)
	}
}
