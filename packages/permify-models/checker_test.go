package permifymodels

import "testing"

func testChecker(t *testing.T) *Checker {
	t.Helper()
	tuples, err := LoadTuplesFile("tuples.example.json")
	if err != nil {
		t.Fatal(err)
	}
	defs := []RelationDef{
		{Entity: "tenant", Name: "manage", Union: []string{"admin"}},
		{Entity: "tenant", Name: "operate", Union: []string{"admin", "operator"}},
		{Entity: "tenant", Name: "read", Union: []string{"admin", "operator", "auditor"}},
		{Entity: "matter", Name: "view", Union: []string{"counsel", "client", "supervising_partner"}},
		{Entity: "matter", Name: "edit", Union: []string{"counsel", "supervising_partner"}},
		{Entity: "doc", Name: "privileged", Union: []string{"owner", "matter.counsel"}},
		{Entity: "doc", Name: "view", Union: []string{"owner", "matter.view"}},
		{Entity: "doc", Name: "share", Union: []string{"owner", "matter.supervising_partner"}},
	}
	return NewChecker(tuples, defs)
}

func TestDirectAndUnion(t *testing.T) {
	c := testChecker(t)
	if !c.Check("tenant:t1", "read", "user:ops@meridian.local") {
		t.Fatal("operator should read via union")
	}
	if c.Check("tenant:t1", "manage", "user:ops@meridian.local") {
		t.Fatal("operator must not manage")
	}
	if !c.Check("matter:m1", "view", "user:client@co.ng") {
		t.Fatal("client should view matter")
	}
	if c.Check("matter:m1", "edit", "user:client@co.ng") {
		t.Fatal("client must not edit")
	}
}

func TestDottedWalk(t *testing.T) {
	c := testChecker(t)
	// doc:d1 -> matter -> matter:m1; matter:m1#counsel@user:lawyer
	if !c.Check("doc:d1", "privileged", "user:lawyer@firm.ng") {
		t.Fatal("counsel should be privileged via matter walk")
	}
	if !c.Check("doc:d1", "view", "user:client@co.ng") {
		t.Fatal("client should view doc via matter.view walk")
	}
	if c.Check("doc:d1", "share", "user:client@co.ng") {
		t.Fatal("client must not share (needs supervising_partner)")
	}
	if c.Check("doc:d1", "privileged", "user:stranger@x.ng") {
		t.Fatal("stranger must not be privileged")
	}
}
