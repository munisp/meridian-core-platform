package index

import (
	"encoding/json"
	"testing"

	"github.com/munisp/meridian-core-platform/packages/events/envelope"
)

func mkEnv(t *testing.T, typ string, data any) envelope.Envelope {
	t.Helper()
	env, err := envelope.New(typ, "test", "t1", "", data)
	if err != nil {
		t.Fatal(err)
	}
	return env
}

func TestIndexAndSearch(t *testing.T) {
	ix := New(nil)
	ix.IndexEnvelope("nrs.rulepacks.published.v1", mkEnv(t, "nrs.rulepacks.published.v1",
		map[string]any{"pack_id": "rp-wht-2024", "version": "1.0.0", "published_by": "board"}))
	ix.IndexEnvelope("nrs.ledger.transfers.v1", mkEnv(t, "nrs.ledger.transfers.v1",
		map[string]any{"amount_kobo": 5000, "ledger": 200}))
	ix.IndexEnvelope("nrs.ledger.transfers.v1", mkEnv(t, "nrs.ledger.transfers.v1",
		map[string]any{"amount_kobo": 7000, "ledger": 200, "note": "wht remittance"}))

	hits := ix.Search("rp-wht-2024", 10)
	if len(hits) != 1 || hits[0].Doc.Type != "nrs.rulepacks.published.v1" {
		t.Fatalf("hits: %+v", hits)
	}
	// AND semantics: ledger + wht
	hits = ix.Search("200 wht", 10)
	if len(hits) != 1 {
		t.Fatalf("AND search: %+v", hits)
	}
	// idempotent re-index
	raw := ix.docs
	_ = raw
	hits = ix.Search("remittance", 10)
	if len(hits) != 1 {
		t.Fatalf("token search: %+v", hits)
	}
	if ix.Count() != 3 {
		t.Fatalf("count = %d", ix.Count())
	}
}

func TestIdempotentReindex(t *testing.T) {
	ix := New(nil)
	env := mkEnv(t, "nrs.x.v1", map[string]any{"k": "v"})
	ix.IndexEnvelope("t", env)
	ix.IndexEnvelope("t", env) // same id
	if ix.Count() != 1 {
		t.Fatalf("duplicate indexed, count=%d", ix.Count())
	}
}

func TestTokenizeAndJSONRoundTrip(t *testing.T) {
	toks := Tokenize("Hello, World! rp-wht-2024@1.0.0")
	want := map[string]bool{"hello": true, "world": true, "rp": true, "wht": true, "2024": true}
	for _, tok := range toks {
		delete(want, tok)
	}
	if len(want) != 0 {
		t.Fatalf("missing tokens: %v", want)
	}
	d := Doc{ID: "x", Text: "a b"}
	b, _ := json.Marshal(d)
	var back Doc
	if json.Unmarshal(b, &back) != nil || back.ID != "x" {
		t.Fatal("doc round trip")
	}
}
