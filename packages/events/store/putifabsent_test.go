package store

// putifabsent_test.go — regression for the r9 pentest defect: WORM evidence
// writes must be insert-only (ON CONFLICT DO NOTHING / no in-place replace)
// so they work under append-only DB roles and can never overwrite an id.

import (
	"context"
	"os"
	"testing"
)

func TestPutIfAbsentEmbedded(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ok, err := st.PutIfAbsent("evidence", "id-1", map[string]any{"v": 1})
	if err != nil || !ok {
		t.Fatalf("first insert must succeed: ok=%v err=%v", ok, err)
	}
	ok, err = st.PutIfAbsent("evidence", "id-1", map[string]any{"v": 2})
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("second insert of same id must report not-inserted")
	}
	var got map[string]any
	if err := st.Get("evidence", "id-1", &got); err != nil {
		t.Fatal(err)
	}
	if got["v"].(float64) != 1 {
		t.Fatalf("original document must be preserved, got %v", got["v"])
	}
}

// TestPutIfAbsentPg exercises the insert-only path against a real Postgres
// when PG_TEST_DATABASE_URL is set (used by the r9 live harness); skipped
// otherwise.
func TestPutIfAbsentPg(t *testing.T) {
	url := os.Getenv("PG_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PG_TEST_DATABASE_URL unset")
	}
	pg, err := OpenPg(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	ok, err := pg.PutIfAbsent("evidence", "pia-1", map[string]any{"v": "a"})
	if err != nil || !ok {
		t.Fatalf("first insert must succeed: ok=%v err=%v", ok, err)
	}
	ok, err = pg.PutIfAbsent("evidence", "pia-1", map[string]any{"v": "b"})
	if err != nil {
		t.Fatalf("conflict insert must not error (INSERT-only role safe): %v", err)
	}
	if ok {
		t.Fatal("conflict insert must report not-inserted")
	}
}
