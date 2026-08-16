package postgis

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/munisp/meridian-core-platform/services/geo/internal/geojson"
)

// TestPostGISAttribution runs the real ST_Covers query against a real
// Postgres. It requires GEO_TEST_DATABASE_URL (or DATABASE_URL) pointing at
// a database with the PostGIS extension available. When either precondition
// is missing the test SKIPS HONESTLY — no sqlite stand-in, no fake.
func TestPostGISAttribution(t *testing.T) {
	dsn := firstEnv("GEO_TEST_DATABASE_URL", "DATABASE_URL")
	if dsn == "" {
		t.Skip("honest skip: no GEO_TEST_DATABASE_URL/DATABASE_URL; no Postgres available")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Skipf("honest skip: cannot connect to %q: %v", redactDSN(dsn), err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS postgis"); err != nil {
		t.Skipf("honest skip: PostGIS extension not available in this Postgres: %v", err)
	}

	ds, err := geojson.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	eng, err := Open(ctx, dsn, ds)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer eng.Close()

	// Lagos Island must attribute to Lagos through the real ST_Covers path.
	att, err := eng.AttributePoint(ctx, 6.45, 3.40)
	if err != nil {
		t.Fatal(err)
	}
	if att == nil || att.State != "Lagos" {
		t.Fatalf("attribution = %+v, want Lagos", att)
	}
	// London is outside every seed state polygon.
	att, err = eng.AttributePoint(ctx, 51.5, -0.12)
	if err != nil {
		t.Fatal(err)
	}
	if att != nil {
		t.Fatalf("london attributed to %s", att.State)
	}
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := getEnv(k); v != "" {
			return v
		}
	}
	return ""
}

func getEnv(k string) string {
	return strings.TrimSpace(os.Getenv(k))
}

// redactDSN strips credentials from a DSN for test output.
func redactDSN(dsn string) string {
	if i := strings.Index(dsn, "@"); i >= 0 {
		if j := strings.LastIndex(dsn[:i], "://"); j >= 0 {
			return dsn[:j+3] + "***" + dsn[i:]
		}
	}
	return dsn
}
