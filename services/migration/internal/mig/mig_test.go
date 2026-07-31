// mig_test.go — M1: ingest, validation, rollback, dedup, proofs, idempotency.
package mig

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

var testNow = time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
var testKey = []byte("test-signing-key")

// memStore is the in-memory Store for tests.
type memStore struct {
	docs    map[string]json.RawMessage
	failAt  int // fail the Nth Put (1-based); 0 = never
	puts    int
	deleted []string
}

func newMemStore() *memStore { return &memStore{docs: map[string]json.RawMessage{}} }

func (s *memStore) key(coll, id string) string { return coll + "/" + id }

func (s *memStore) Get(coll, id string) (json.RawMessage, error) {
	if d, ok := s.docs[s.key(coll, id)]; ok {
		return d, nil
	}
	return nil, ErrNotFound
}

func (s *memStore) Put(coll, id string, doc json.RawMessage) error {
	s.puts++
	if s.failAt > 0 && s.puts == s.failAt {
		return fmt.Errorf("injected write failure")
	}
	b := make([]byte, len(doc))
	copy(b, doc)
	s.docs[s.key(coll, id)] = b
	return nil
}

func (s *memStore) Delete(coll, id string) error {
	delete(s.docs, s.key(coll, id))
	s.deleted = append(s.deleted, s.key(coll, id))
	return nil
}

func (s *memStore) List(coll string) ([]json.RawMessage, error) {
	var out []json.RawMessage
	for k, v := range s.docs {
		if strings.HasPrefix(k, coll+"/") {
			out = append(out, v)
		}
	}
	return out, nil
}

// fakeLive is a scripted LiveIndex.
type fakeLive struct{ tins map[string]string } // tin -> entity id

func (f fakeLive) LookupTIN(tin string) (bool, string, error) {
	id, ok := f.tins[tin]
	return ok, id, nil
}

type failLive struct{}

func (failLive) LookupTIN(string) (bool, string, error) {
	return false, "", errors.New("tin-graph unreachable")
}

func goodTIN(t *testing.T, base string) string {
	t.Helper()
	fixed, err := TINWithCheckDigit(base)
	if err != nil {
		t.Fatal(err)
	}
	return fixed
}

// fixtures builds a small valid 3-file batch (checksum-strict clean).
func fixtures(t *testing.T) []SourceFile {
	t.Helper()
	t1, t2 := goodTIN(t, "12345678-0000"), goodTIN(t, "87654321-0000")
	csvTP := "tin,name,type,registration_date,status\n" +
		t1 + ",Adaeze Okafor,individual,2010-05-01,active\n" +
		t2 + ",Meridian Trading Ltd,company,2015-01-20,active\n"
	jsonlFL := fmt.Sprintf(
		`{"tin":%q,"period":"2024-01","tax_type":"PAYE","assessed_kobo":150000}`+"\n"+
			`{"tin":%q,"period":"2024-01","tax_type":"VAT","assessed_kobo":250000}`+"\n", t1, t2)
	csvPM := "tin,reference,amount_kobo,date,channel\n" +
		t1 + ",PMT-001,150000,2024-02-05,bank\n" +
		t2 + ",PMT-002,100000,2024-02-06,ussd\n"
	return []SourceFile{
		{Entity: Taxpayers, Format: "csv", Name: "taxpayers.csv", R: strings.NewReader(csvTP)},
		{Entity: Filings, Format: "jsonl", Name: "filings.jsonl", R: strings.NewReader(jsonlFL)},
		{Entity: Payments, Format: "csv", Name: "payments.csv", R: strings.NewReader(csvPM)},
	}
}

func deps(st Store, live LiveIndex) Deps {
	return Deps{Store: st, Live: live, SigningKey: testKey, KeyID: "test-key [simulated]"}
}

func TestTINFormatAndChecksum(t *testing.T) {
	fixed := goodTIN(t, "12345678-0000")
	if !ValidTINFormat(fixed) || !TINChecksumOK(fixed) {
		t.Fatalf("fixed TIN %s must pass format+checksum", fixed)
	}
	if ValidTINFormat("123-456") || ValidTINFormat("12345678-00000") {
		t.Fatal("bad formats must fail")
	}
	if TINChecksumOK("12345678-0000") { // almost certainly wrong check digit
		t.Fatal("uncorrected TIN must fail checksum")
	}
}

func TestCSVParsingHappyPath(t *testing.T) {
	st := newMemStore()
	res, err := Import(deps(st, fakeLive{}), fixtures(t), Options{Mode: DryRun, StrictChecksum: true}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	m := res.Manifest
	if m.Counts["taxpayers"] != 2 || m.Counts["filings"] != 2 || m.Counts["payments"] != 2 {
		t.Fatalf("counts: %+v", m.Counts)
	}
	if m.SumsKobo["assessed_kobo"] != 400000 || m.SumsKobo["payments_kobo"] != 250000 {
		t.Fatalf("sums: %+v", m.SumsKobo)
	}
	if len(res.Errors) != 0 {
		t.Fatalf("unexpected errors: %+v", res.Errors)
	}
}

func TestMalformedRowsGoToErrorLedger(t *testing.T) {
	tin := goodTIN(t, "12345678-0000")
	csvTP := "tin,name,type,registration_date,status\n" +
		tin + ",Adaeze Okafor,individual,2010-05-01,active\n" +
		"not-a-tin,Bad Row,individual,2010-05-01,active\n" +
		tin + ",No Type,,2010-05-01,active\n" +
		goodTIN(t, "22222222-0000") + ",Bad Date,individual,31/05/2010,active\n"
	res, err := Import(deps(newMemStore(), fakeLive{}),
		[]SourceFile{{Entity: Taxpayers, Format: "csv", Name: "t.csv", R: strings.NewReader(csvTP)}},
		Options{Mode: DryRun}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if res.Manifest.Counts["taxpayers"] != 1 || res.Manifest.Counts["rejected"] != 3 {
		t.Fatalf("counts: %+v", res.Manifest.Counts)
	}
	if len(res.Errors) != 3 || res.Errors[0].Line != 3 {
		t.Fatalf("error ledger: %+v", res.Errors)
	}
}

func TestMalformedJSONLRow(t *testing.T) {
	body := `{"tin":"12345678-0001","period":"2024-01","tax_type":"PAYE","assessed_kobo":100}` + "\n{not json}\n"
	res, err := Import(deps(newMemStore(), fakeLive{tins: map[string]string{"12345678-0001": "ent-1"}}),
		[]SourceFile{{Entity: Filings, Format: "jsonl", Name: "f.jsonl", R: strings.NewReader(body)}},
		Options{Mode: DryRun}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if res.Manifest.Counts["filings"] != 1 || len(res.Errors) != 1 || res.Errors[0].Line != 2 {
		t.Fatalf("got %+v / %+v", res.Manifest.Counts, res.Errors)
	}
}

func TestFractionalAndNegativeKoboRejected(t *testing.T) {
	tin := goodTIN(t, "12345678-0000")
	body := fmt.Sprintf(`{"tin":%q,"period":"2024-01","tax_type":"PAYE","assessed_kobo":100.50}`+"\n", tin) +
		fmt.Sprintf(`{"tin":%q,"reference":"R1","amount_kobo":-5,"date":"2024-01-01","channel":"bank"}`+"\n", tin)
	res, err := Import(deps(newMemStore(), fakeLive{}), []SourceFile{
		{Entity: Filings, Format: "jsonl", Name: "f.jsonl", R: strings.NewReader(body[:strings.Index(body, "\n")+1])},
		{Entity: Payments, Format: "jsonl", Name: "p.jsonl", R: strings.NewReader(body[strings.Index(body, "\n")+1:])},
	}, Options{Mode: DryRun}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if res.Manifest.Counts["filings"] != 0 || res.Manifest.Counts["payments"] != 0 || len(res.Errors) != 2 {
		t.Fatalf("fractional/negative kobo must be rejected: %+v", res.Errors)
	}
}

func TestStrictChecksumRejectsBadTIN(t *testing.T) {
	csvTP := "tin,name,type,registration_date,status\n12345678-0000,No Check,individual,2010-05-01,active\n"
	res, err := Import(deps(newMemStore(), fakeLive{}),
		[]SourceFile{{Entity: Taxpayers, Format: "csv", Name: "t.csv", R: strings.NewReader(csvTP)}},
		Options{Mode: DryRun, StrictChecksum: true}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if res.Manifest.Counts["taxpayers"] != 0 || len(res.Errors) != 1 || !strings.Contains(res.Errors[0].Err, "check-digit") {
		t.Fatalf("strict checksum: %+v", res.Errors)
	}
	// non-strict mode accepts format-valid TIN
	res, _ = Import(deps(newMemStore(), fakeLive{}),
		[]SourceFile{{Entity: Taxpayers, Format: "csv", Name: "t.csv", R: strings.NewReader(csvTP)}},
		Options{Mode: DryRun}, testNow)
	if res.Manifest.Counts["taxpayers"] != 1 {
		t.Fatalf("non-strict must accept: %+v", res.Manifest.Counts)
	}
}

func TestDuplicateVsLiveTinGraph(t *testing.T) {
	tin := goodTIN(t, "12345678-0000")
	live := fakeLive{tins: map[string]string{tin: "ent-live-1"}}
	files := fixtures(t) // contains tin 12345678-... fixed
	res, err := Import(deps(newMemStore(), live), files, Options{Mode: DryRun, StrictChecksum: true}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if res.Manifest.Counts["taxpayers"] != 1 {
		t.Fatalf("live duplicate must be excluded: %+v", res.Manifest.Counts)
	}
	found := false
	for _, e := range res.Errors {
		if strings.Contains(e.Err, "already live") {
			found = true
		}
	}
	if !found {
		t.Fatalf("duplicate error missing: %+v", res.Errors)
	}
}

func TestIntraBatchDuplicateRejected(t *testing.T) {
	tin := goodTIN(t, "12345678-0000")
	csvTP := "tin,name,type,registration_date,status\n" +
		tin + ",A,individual,2010-05-01,active\n" + tin + ",A Again,individual,2011-01-01,active\n"
	res, err := Import(deps(newMemStore(), fakeLive{}),
		[]SourceFile{{Entity: Taxpayers, Format: "csv", Name: "t.csv", R: strings.NewReader(csvTP)}},
		Options{Mode: DryRun}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if res.Manifest.Counts["taxpayers"] != 1 || len(res.Errors) != 1 || !strings.Contains(res.Errors[0].Err, "duplicate row within batch") {
		t.Fatalf("intra-batch dedup: %+v", res.Errors)
	}
}

func TestReferentialCheckFails(t *testing.T) {
	tin := goodTIN(t, "99999999-0000")
	body := fmt.Sprintf(`{"tin":%q,"reference":"R9","amount_kobo":100,"date":"2024-01-01","channel":"bank"}`+"\n", tin)
	res, err := Import(deps(newMemStore(), fakeLive{}),
		[]SourceFile{{Entity: Payments, Format: "jsonl", Name: "p.jsonl", R: strings.NewReader(body)}},
		Options{Mode: DryRun}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if res.Manifest.Counts["payments"] != 0 || !strings.Contains(res.Errors[0].Err, "referential check failed") {
		t.Fatalf("orphan payment must be rejected: %+v", res.Errors)
	}
}

func TestReferentialCheckPassesViaLive(t *testing.T) {
	tin := goodTIN(t, "99999999-0000")
	live := fakeLive{tins: map[string]string{tin: "ent-live-9"}}
	body := fmt.Sprintf(`{"tin":%q,"reference":"R9","amount_kobo":100,"date":"2024-01-01","channel":"bank"}`+"\n", tin)
	res, err := Import(deps(newMemStore(), live),
		[]SourceFile{{Entity: Payments, Format: "jsonl", Name: "p.jsonl", R: strings.NewReader(body)}},
		Options{Mode: DryRun}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if res.Manifest.Counts["payments"] != 1 {
		t.Fatalf("payment against live TIN must pass: %+v", res.Manifest.Counts)
	}
}

func TestLiveLookupFailureIsFailClosed(t *testing.T) {
	_, err := Import(deps(newMemStore(), failLive{}), fixtures(t), Options{Mode: DryRun, StrictChecksum: true}, testNow)
	if err == nil || !strings.Contains(err.Error(), "fail-closed") {
		t.Fatalf("live lookup failure must abort (fail-closed), got %v", err)
	}
}

func TestCommitWithoutKeyFailsClosed(t *testing.T) {
	d := deps(newMemStore(), fakeLive{})
	d.SigningKey = nil
	if _, err := Import(d, fixtures(t), Options{Mode: Commit, StrictChecksum: true}, testNow); err == nil {
		t.Fatal("commit without signing key must fail closed")
	}
}

func TestDryRunWritesNothing(t *testing.T) {
	st := newMemStore()
	res, err := Import(deps(st, fakeLive{}), fixtures(t), Options{Mode: DryRun, StrictChecksum: true}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.docs) != 0 || st.puts != 0 {
		t.Fatalf("dry-run must not write, got %d docs", len(st.docs))
	}
	if res.Manifest.Committed {
		t.Fatal("dry-run manifest must not be committed")
	}
}

func TestDryRunVsCommitManifestEquivalence(t *testing.T) {
	dry, err := Import(deps(newMemStore(), fakeLive{}), fixtures(t), Options{Mode: DryRun, StrictChecksum: true}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	st := newMemStore()
	com, err := Import(deps(st, fakeLive{}), fixtures(t), Options{Mode: Commit, StrictChecksum: true}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	a, b := dry.Manifest, com.Manifest
	if a.BatchID != b.BatchID || a.SHA256 != b.SHA256 || a.HashChainRoot != b.HashChainRoot {
		t.Fatal("dry-run and commit must produce the same content-addressed manifest")
	}
	if a.Mode == b.Mode {
		t.Fatal("mode must differ")
	}
	if !b.Committed || len(st.docs) == 0 {
		t.Fatal("commit must write docs")
	}
}

func TestIdempotentRerunSameManifestNoDuplicates(t *testing.T) {
	st := newMemStore()
	r1, err := Import(deps(st, fakeLive{}), fixtures(t), Options{Mode: Commit, StrictChecksum: true}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	docsAfterFirst := len(st.docs)
	r2, err := Import(deps(st, fakeLive{}), fixtures(t), Options{Mode: Commit, StrictChecksum: true}, testNow.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if r1.Manifest.BatchID != r2.Manifest.BatchID || r1.Manifest.Signature != r2.Manifest.Signature {
		t.Fatal("re-run must reproduce the same manifest")
	}
	if len(st.docs) != docsAfterFirst {
		t.Fatalf("re-run wrote duplicates: %d -> %d", docsAfterFirst, len(st.docs))
	}
	noted := false
	for _, n := range r2.Manifest.Notes {
		if strings.Contains(n, "idempotent re-run") {
			noted = true
		}
	}
	if !noted {
		t.Fatalf("re-run note missing: %+v", r2.Manifest.Notes)
	}
}

func TestPartialFailureRollback(t *testing.T) {
	st := newMemStore()
	st.failAt = 3 // third write fails
	_, err := Import(deps(st, fakeLive{}), fixtures(t), Options{Mode: Commit, StrictChecksum: true}, testNow)
	if err == nil || !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("want rollback error, got %v", err)
	}
	for coll := range map[string]bool{"mig_taxpayers": true, "mig_filings": true, "mig_payments": true} {
		raws, _ := st.List(coll)
		if len(raws) != 0 {
			t.Fatalf("rollback left %d docs in %s", len(raws), coll)
		}
	}
	if len(st.deleted) != 2 {
		t.Fatalf("expected 2 compensating deletes, got %v", st.deleted)
	}
}

func TestVerifyPassAfterCommit(t *testing.T) {
	st := newMemStore()
	res, err := Import(deps(st, fakeLive{}), fixtures(t), Options{Mode: Commit, StrictChecksum: true}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	proof := Verify(st, res.Manifest, testKey, testNow)
	if proof.Verdict != "PASS" {
		t.Fatalf("want PASS, got %s\n%s", proof.Verdict, proof.Summary)
	}
	if !strings.Contains(proof.Summary, "assessed=400000 kobo") {
		t.Fatalf("human summary wrong:\n%s", proof.Summary)
	}
}

func TestVerifyDetectsTamperedDoc(t *testing.T) {
	st := newMemStore()
	res, err := Import(deps(st, fakeLive{}), fixtures(t), Options{Mode: Commit, StrictChecksum: true}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	// tamper: bump a payment amount in the live store
	for k, v := range st.docs {
		if strings.HasPrefix(k, "mig_payments/") {
			var d map[string]any
			_ = json.Unmarshal(v, &d)
			row := d["row"].(map[string]any)
			row["amount_kobo"] = 999999
			b, _ := json.Marshal(d)
			st.docs[k] = b
		}
	}
	proof := Verify(st, res.Manifest, testKey, testNow)
	if proof.Verdict != "FAIL" {
		t.Fatalf("tampered doc must FAIL proof\n%s", proof.Summary)
	}
}

func TestVerifyDetectsTamperedManifestSignature(t *testing.T) {
	st := newMemStore()
	res, err := Import(deps(st, fakeLive{}), fixtures(t), Options{Mode: Commit, StrictChecksum: true}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	res.Manifest.SumsKobo["payments_kobo"] = 1 // forged manifest
	if VerifySignature(res.Manifest, testKey) {
		t.Fatal("forged manifest must fail signature check")
	}
	proof := Verify(st, res.Manifest, testKey, testNow)
	if proof.Verdict != "FAIL" || !strings.Contains(proof.Summary, "manifest_signature") {
		t.Fatalf("tampered manifest must FAIL closed\n%s", proof.Summary)
	}
	// wrong key fails too
	if VerifySignature(res.Manifest, []byte("other-key")) {
		t.Fatal("wrong key must not verify")
	}
}

func TestHashChainTamperEvidence(t *testing.T) {
	r1, _ := Import(deps(newMemStore(), fakeLive{}), fixtures(t), Options{Mode: DryRun, StrictChecksum: true}, testNow)
	files := fixtures(t)
	// flip one amount in the filings file
	fl := files[1]
	body, _ := readAll(fl.R)
	files[1].R = strings.NewReader(strings.Replace(string(body), "150000", "150001", 1))
	r2, _ := Import(deps(newMemStore(), fakeLive{}), files, Options{Mode: DryRun, StrictChecksum: true}, testNow)
	if r1.Manifest.HashChainRoot == r2.Manifest.HashChainRoot || r1.Manifest.BatchID == r2.Manifest.BatchID {
		t.Fatal("changed row must change hash chain root and batch id")
	}
}

func TestUnknownFormatAndEmptyBatchRejected(t *testing.T) {
	if _, err := Import(deps(newMemStore(), fakeLive{}), nil, Options{Mode: DryRun}, testNow); err == nil {
		t.Fatal("no files must fail")
	}
	files := []SourceFile{{Entity: Taxpayers, Format: "xml", Name: "t.xml", R: strings.NewReader("")}}
	if _, err := Import(deps(newMemStore(), fakeLive{}), files, Options{Mode: DryRun}, testNow); err == nil || !strings.Contains(err.Error(), "unknown format") {
		t.Fatalf("unknown format must fail, got %v", err)
	}
}

func TestManifestCoversEmptyValidSet(t *testing.T) {
	// all rows rejected: manifest still well-formed, zero counts
	csvTP := "tin,name,type,registration_date,status\nbad-tin,A,individual,2010-05-01,active\n"
	res, err := Import(deps(newMemStore(), fakeLive{}),
		[]SourceFile{{Entity: Taxpayers, Format: "csv", Name: "t.csv", R: strings.NewReader(csvTP)}},
		Options{Mode: DryRun}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	m := res.Manifest
	if m.RowCount != 0 || m.HashChainRoot == "" || m.BatchID == "" {
		t.Fatalf("empty valid set: %+v", m)
	}
}

func readAll(r interface{ Read([]byte) (int, error) }) ([]byte, error) {
	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			return []byte(b.String()), nil
		}
	}
}
