package evidence

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/munisp/meridian-core-platform/packages/events/store"
)

func openTest(t *testing.T) (*AuditLog, *WormStore, string) {
	t.Helper()
	dir := t.TempDir()
	al, err := OpenAuditLog(dir)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ws, err := NewWormStore(filepath.Join(dir, "worm"), st)
	if err != nil {
		t.Fatal(err)
	}
	return al, ws, dir
}

func TestHashChain(t *testing.T) {
	al, _, dir := openTest(t)
	e1, _ := al.Append(AuditEvent{Actor: "u1", Subject: "s1", Action: "view", Type: "audit.view"})
	e2, _ := al.Append(AuditEvent{Actor: "u2", Subject: "s1", Action: "edit", Type: "audit.edit", RulePackVersion: "rp-x@1.0.0"})
	if e1.PrevHash != "" || e2.PrevHash != e1.Hash {
		t.Fatalf("chain links broken: %+v %+v", e1, e2)
	}
	if broken, _ := al.VerifyChain(); broken != 0 {
		t.Fatalf("chain invalid at seq %d", broken)
	}
	// reload: tail recovered, chain still valid
	al2, err := OpenAuditLog(dir)
	if err != nil {
		t.Fatal(err)
	}
	e3, _ := al2.Append(AuditEvent{Actor: "u3", Subject: "s2", Action: "view", Type: "audit.view"})
	if e3.PrevHash != e2.Hash {
		t.Fatal("tail not recovered on reopen")
	}
	if broken, _ := al2.VerifyChain(); broken != 0 {
		t.Fatalf("reopened chain invalid at %d", broken)
	}
}

func TestChainDetectsTampering(t *testing.T) {
	al, _, dir := openTest(t)
	al.Append(AuditEvent{Actor: "u1", Subject: "s1", Action: "view", Type: "t"})
	al.Append(AuditEvent{Actor: "u1", Subject: "s1", Action: "view", Type: "t"})
	// tamper with the raw log file
	path := filepath.Join(dir, "audit.jsonl")
	b, _ := os.ReadFile(path)
	var recs [][]byte
	start := 0
	for i, c := range b {
		if c == '\n' {
			recs = append(recs, b[start:i])
			start = i + 1
		}
	}
	var m map[string]any
	json.Unmarshal(recs[0], &m)
	m["actor"] = "mallory"
	tampered, _ := json.Marshal(m)
	out := append(tampered, '\n')
	for _, r := range recs[1:] {
		out = append(out, r...)
		out = append(out, '\n')
	}
	os.WriteFile(path, out, 0o644)
	al2, _ := OpenAuditLog(dir)
	broken, _ := al2.VerifyChain()
	if broken == 0 {
		t.Fatal("tampering not detected")
	}
}

func TestQuery(t *testing.T) {
	al, _, _ := openTest(t)
	al.Append(AuditEvent{Subject: "s1", Action: "a", Type: "t1"})
	al.Append(AuditEvent{Subject: "s2", Action: "a", Type: "t2"})
	al.Append(AuditEvent{Subject: "s1", Action: "b", Type: "t2"})
	res, _ := al.Query("s1", "", "", "")
	if len(res) != 2 {
		t.Fatalf("subject query: %d", len(res))
	}
	res, _ = al.Query("s1", "t2", "", "")
	if len(res) != 1 || res[0].Action != "b" {
		t.Fatalf("type query: %+v", res)
	}
}

func TestWormStore(t *testing.T) {
	_, ws, _ := openTest(t)
	obj, err := ws.Put("", []byte("evidence bytes"), "text/plain", map[string]any{"case": "c1"})
	if err != nil {
		t.Fatal(err)
	}
	if obj.SHA256 == "" || obj.WormURI == "" || !obj.Immutable {
		t.Fatalf("bad object: %+v", obj)
	}
	// overwrite rejected
	if _, err := ws.Put(obj.ID, []byte("different"), "text/plain", nil); err != ErrImmutable() {
		t.Fatalf("overwrite: %v", err)
	}
	ok, got, err := ws.Verify(obj.ID)
	if err != nil || !ok || got != obj.SHA256 {
		t.Fatalf("verify: %v %v %s", ok, err, got)
	}
	// tamper file -> verify fails
	os.Chmod(obj.StoragePath, 0o644)
	os.WriteFile(obj.StoragePath, []byte("tampered"), 0o644)
	ok, _, _ = ws.Verify(obj.ID)
	if ok {
		t.Fatal("tampered WORM object verified")
	}
}

func TestAssembleTAT(t *testing.T) {
	al, ws, _ := openTest(t)
	obj, _ := ws.Put("", []byte("x"), "text/plain", nil)
	al.Append(AuditEvent{Actor: "u1", Subject: "s1", Action: "view", Type: "t",
		RulePackVersion: "rp-wht-2024@1.0.0", Details: map[string]any{"evidence_id": obj.ID}})
	al.Append(AuditEvent{Actor: "u2", Subject: "s1", Action: "export", Type: "t"})
	al.Append(AuditEvent{Actor: "u9", Subject: "other", Action: "view", Type: "t"})
	tat, err := AssembleTAT(al, ws, "s1", "", "", "auditor-1", "key")
	if err != nil {
		t.Fatal(err)
	}
	if len(tat.Events) != 2 || !tat.ChainValid || tat.Seal == "" {
		t.Fatalf("bad tat: %+v", tat)
	}
	if len(tat.RulePacksSeen) != 1 || tat.RulePacksSeen[0] != "rp-wht-2024@1.0.0" {
		t.Fatalf("rule packs: %+v", tat.RulePacksSeen)
	}
	if len(tat.EvidenceRefs) != 1 || tat.EvidenceRefs[0] != obj.ID {
		t.Fatalf("evidence refs: %+v", tat.EvidenceRefs)
	}
}
