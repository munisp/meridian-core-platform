package graph

import (
	"strings"
	"testing"
)

func TestProvisionTINDeterministic(t *testing.T) {
	tin1, err := ProvisionTIN("12345678901", "")
	if err != nil {
		t.Fatal(err)
	}
	tin2, _ := ProvisionTIN("12345678901", "")
	if tin1 != tin2 {
		t.Fatal("non-deterministic TIN")
	}
	if !strings.Contains(tin1, "-") || len(tin1) != 13 {
		t.Fatalf("bad tin format %q", tin1)
	}
	if _, err := ProvisionTIN("123", ""); err == nil {
		t.Fatal("short NIN accepted")
	}
	if _, err := ProvisionTIN("", ""); err == nil {
		t.Fatal("empty accepted")
	}
	tinCAC, err := ProvisionTIN("", "RC123456")
	if err != nil || tinCAC == tin1 {
		t.Fatalf("cac tin: %v %q", err, tinCAC)
	}
}

func TestHashPseudonymisation(t *testing.T) {
	h1 := HashTIN("12345678-0001")
	h2 := HashTIN("12345678-0001")
	if h1 != h2 || len(h1) != 64 {
		t.Fatal("tin_hash unstable")
	}
	if HashTIN("12345678-0002") == h1 {
		t.Fatal("hash collision on different tin")
	}
}

func TestNINSimulator(t *testing.T) {
	res, _ := NINSimulator{}.Verify("12345678901")
	if !res.Valid {
		t.Fatal("11-digit NIN should be valid")
	}
	res2, _ := NINSimulator{}.Verify("12345678901")
	if res.FirstName != res2.FirstName || res.DOB != res2.DOB {
		t.Fatal("simulator not deterministic")
	}
	bad, _ := NINSimulator{}.Verify("abc")
	if bad.Valid {
		t.Fatal("invalid NIN accepted")
	}
}

func TestCACSimulator(t *testing.T) {
	res, _ := CACSimulator{}.Verify("RC1234567")
	if !res.Valid || res.Company == "" {
		t.Fatalf("bad cac sim: %+v", res)
	}
	if bad, _ := (CACSimulator{}).Verify("nope"); bad.Valid {
		t.Fatal("invalid RC accepted")
	}
}

func TestStringSimilarity(t *testing.T) {
	if stringSimilarity("Adebayo Ogunlesi", "Adebayo Ogunlesi") != 1 {
		t.Fatal("identical strings")
	}
	if stringSimilarity("Adebayo Ogunlesi", "Adebayo Ogunlessi") < 0.8 {
		t.Fatal("typo should stay close")
	}
	if stringSimilarity("Adebayo", "Zubair") > 0.5 {
		t.Fatal("different names too close")
	}
}

func TestResolveThresholds(t *testing.T) {
	cfg := DefaultMatchConfig
	entities := []Entity{{
		ID: "ent-1", TINHash: HashTIN("12345678-0001"), Name: "Adaeze Okafor",
		Phone: "08031234567", Email: "ada@example.com", NINHash: HashValue("12345678901"),
	}}
	// strong match: same phone + similar name + same NIN
	dec, cands := Resolve(cfg, Attributes{
		Name: "Adaeze Okafor", Phone: "+2348031234567", NIN: "12345678901",
	}, entities)
	if dec != DecisionAutoMatch || len(cands) == 0 || cands[0].EntityID != "ent-1" {
		t.Fatalf("auto match: %s %+v", dec, cands)
	}
	// weak match: only vaguely similar name
	dec, cands = Resolve(cfg, Attributes{Name: "Adaeze Okonkwo"}, entities)
	if dec != DecisionReview {
		t.Fatalf("weak match decision: %s (score %.2f)", dec, cands[0].Score)
	}
	// no match
	dec, _ = Resolve(cfg, Attributes{Name: "Completely Different", Phone: "09099999999"}, entities)
	if dec != DecisionNew {
		t.Fatalf("no match decision: %s", dec)
	}
}

func TestBuildGraph(t *testing.T) {
	e1 := Entity{ID: "a", Phone: "08031234567", Email: "x@y.com"}
	e2 := Entity{ID: "b", Phone: "+2348031234567"} // shared phone
	e3 := Entity{ID: "c", Email: "X@y.com"}        // shared email
	e4 := Entity{ID: "d", Phone: "09011112222"}    // unrelated
	gv := BuildGraph("a", []Entity{e1, e2, e3, e4})
	if len(gv.Nodes) != 3 {
		t.Fatalf("nodes: %+v", gv.Nodes)
	}
	rels := map[string]bool{}
	for _, e := range gv.Edges {
		rels[e.Rel] = true
	}
	if !rels["shared_phone"] || !rels["shared_email"] {
		t.Fatalf("edges: %+v", gv.Edges)
	}
	if empty := BuildGraph("missing", []Entity{e1}); len(empty.Nodes) != 0 {
		t.Fatal("missing root should yield empty graph")
	}
}
