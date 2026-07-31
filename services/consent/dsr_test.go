package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/munisp/meridian-core-platform/packages/events/auth"
)

func dsrReq(t *testing.T, h http.HandlerFunc, method, path, subject, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	if path == "" {
		path = "/v1/dsr/x"
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.SetPathValue("subject", subject)
	if token != "" {
		req.Header.Set("Authorization", token)
	}
	rec := httptest.NewRecorder()
	auth.Middleware(h).ServeHTTP(rec, req)
	return rec
}

func seedDSRSubject(t *testing.T, s *server, subject string) {
	t.Helper()
	seedConsent(t, s, Consent{
		ID: "con-dsr-1", Subject: subject, Purpose: "nin_verification",
		LawfulBasis: "consent", Granted: true, Status: "active",
		Channel: "ussd", Metadata: map[string]any{"note": "hi"},
	})
	seedConsent(t, s, Consent{
		ID: "con-dsr-2", Subject: subject, Purpose: "tin_verification",
		LawfulBasis: "legal_obligation", Granted: true, Status: "active",
	})
}

func TestDSRExportBundle(t *testing.T) {
	s := newTestServer(t)
	seedDSRSubject(t, s, "tin-hash-A")

	// subject self-export
	tok := bearerFor(t, auth.Claims{Sub: "tin-hash-A"})
	rec := dsrReq(t, s.dsrExport, "GET", "/v1/dsr/tin-hash-A/export", "tin-hash-A", tok, nil)
	if rec.Code != 200 {
		t.Fatalf("self export must be 200, got %d (%s)", rec.Code, rec.Body)
	}
	var out struct {
		Consents []Consent `json:"consents"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Consents) != 2 {
		t.Fatalf("expected 2 consents in bundle, got %d", len(out.Consents))
	}

	// other subject forbidden; officer allowed
	bad := bearerFor(t, auth.Claims{Sub: "tin-hash-B"})
	if rec := dsrReq(t, s.dsrExport, "GET", "", "tin-hash-A", bad, nil); rec.Code != 403 {
		t.Fatalf("other subject must be 403, got %d", rec.Code)
	}
	off := bearerFor(t, auth.Claims{Sub: "dpo", Roles: []string{"privacy:officer"}})
	if rec := dsrReq(t, s.dsrExport, "GET", "", "tin-hash-A", off, nil); rec.Code != 200 {
		t.Fatalf("officer export must be 200, got %d", rec.Code)
	}

	// export logged
	var logs []map[string]any
	if err := s.st.ListInto("dsr_access_log", &logs); err != nil {
		t.Fatal(err)
	}
	if len(logs) != 2 { // self export + officer export (403 not logged)
		t.Fatalf("expected 2 access-log entries, got %d", len(logs))
	}
}

func TestDSRErasureAnonymises(t *testing.T) {
	s := newTestServer(t)
	seedDSRSubject(t, s, "nin-hash-Z")
	tok := bearerFor(t, auth.Claims{Sub: "nin-hash-Z"})

	rec := dsrReq(t, s.dsrErasure, "POST", "", "nin-hash-Z", tok, nil)
	if rec.Code != 200 {
		t.Fatalf("erasure must be 200, got %d (%s)", rec.Code, rec.Body)
	}
	var out map[string]any
	_ = json.NewDecoder(rec.Body).Decode(&out)
	if out["records_anonymised"].(float64) != 2 {
		t.Fatalf("expected 2 anonymised, got %v", out["records_anonymised"])
	}
	anon, _ := out["anonymised_subject"].(string)

	var c Consent
	if err := s.st.Get("consents", "con-dsr-1", &c); err != nil {
		t.Fatal(err)
	}
	if c.Subject != anon || c.Metadata != nil || c.Channel != "" {
		t.Fatalf("consent not anonymised: %+v", c)
	}
	if c.Status != "revoked" || c.Granted {
		t.Fatalf("active consent must be revoked on erasure: %+v", c)
	}

	// audit entry exists
	var audits []map[string]any
	if err := s.st.ListInto("dsr_audit", &audits); err != nil {
		t.Fatal(err)
	}
	if len(audits) != 1 || audits[0]["subject"] != "nin-hash-Z" {
		t.Fatalf("expected 1 erasure audit entry, got %v", audits)
	}

	// post-erasure export finds nothing under the old subject
	rec = dsrReq(t, s.dsrExport, "GET", "", "nin-hash-Z", tok, nil)
	var bundle struct {
		Consents []Consent `json:"consents"`
	}
	_ = json.NewDecoder(rec.Body).Decode(&bundle)
	if len(bundle.Consents) != 0 {
		t.Fatalf("erased subject must export 0 consents, got %d", len(bundle.Consents))
	}
}

func TestDSRErasureLegalHold(t *testing.T) {
	s := newTestServer(t)
	seedDSRSubject(t, s, "tin-hash-H")
	if err := s.st.Put("legal_holds", "lh-1", legalHold{Subject: "tin-hash-H", Active: true, Reason: "tax audit"}); err != nil {
		t.Fatal(err)
	}
	tok := bearerFor(t, auth.Claims{Sub: "root", Roles: []string{"admin"}})
	rec := dsrReq(t, s.dsrErasure, "POST", "", "tin-hash-H", tok, nil)
	if rec.Code != 409 {
		t.Fatalf("legal hold must refuse erasure with 409, got %d", rec.Code)
	}
	// nothing anonymised
	var c Consent
	if err := s.st.Get("consents", "con-dsr-1", &c); err != nil {
		t.Fatal(err)
	}
	if c.Subject != "tin-hash-H" {
		t.Fatal("legal-hold refusal must not modify records")
	}
	// refusal is logged
	var logs []map[string]any
	_ = s.st.ListInto("dsr_access_log", &logs)
	if len(logs) != 1 || logs[0]["action"] != "erasure_refused_legal_hold" {
		t.Fatalf("expected refusal access-log entry, got %v", logs)
	}

	// release the hold -> erasure proceeds
	if err := s.st.Put("legal_holds", "lh-1", legalHold{Subject: "tin-hash-H", Active: false}); err != nil {
		t.Fatal(err)
	}
	rec = dsrReq(t, s.dsrErasure, "POST", "", "tin-hash-H", tok, nil)
	if rec.Code != 200 {
		t.Fatalf("erasure after hold release must be 200, got %d", rec.Code)
	}
}

func TestDSRAccessLogRoleGated(t *testing.T) {
	s := newTestServer(t)
	subj := bearerFor(t, auth.Claims{Sub: "s1"})
	if rec := dsrReq(t, s.dsrAccessLog, "GET", "", "s1", subj, nil); rec.Code != 403 {
		t.Fatalf("subject must not read access log, got %d", rec.Code)
	}
	off := bearerFor(t, auth.Claims{Sub: "dpo", Roles: []string{"privacy:officer"}})
	if rec := dsrReq(t, s.dsrAccessLog, "GET", "", "s1", off, nil); rec.Code != 200 {
		t.Fatalf("officer must read access log, got %d", rec.Code)
	}
}
