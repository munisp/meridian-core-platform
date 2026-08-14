package store

import "testing"

func TestResolveDatabaseURLPerServiceUser(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://shared:oldpw@postgres:5432/meridian")
	t.Setenv("DB_USER", "svc_audit_evidence")
	t.Setenv("DB_PASSWORD", "s3cret")
	got := ResolveDatabaseURL()
	want := "postgres://svc_audit_evidence:s3cret@postgres:5432/meridian"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestResolveDatabaseURLFallbackDev(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://shared:oldpw@postgres:5432/meridian")
	t.Setenv("DB_USER", "")
	t.Setenv("PROFILE", "dev")
	if got := ResolveDatabaseURL(); got != "postgres://shared:oldpw@postgres:5432/meridian" {
		t.Fatalf("dev fallback should keep shared URL, got %q", got)
	}
}

func TestResolveDatabaseURLEmpty(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	if got := ResolveDatabaseURL(); got != "" {
		t.Fatalf("empty DATABASE_URL must stay empty, got %q", got)
	}
}
