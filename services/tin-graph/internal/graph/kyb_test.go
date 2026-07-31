package graph

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDeriveUBOsThreshold(t *testing.T) {
	shs := []Shareholder{
		{PersonRef: PersonRef{Name: "A Bello", NINHash: "h1"}, SharePercent: 40},
		{PersonRef: PersonRef{Name: "B Okafor", NINHash: "h2"}, SharePercent: 25},     // exactly 25 -> NOT a UBO (>25 rule)
		{PersonRef: PersonRef{Name: "C Eze", NINHash: "h3"}, SharePercent: 25.5},      // just over -> UBO
		{PersonRef: PersonRef{Name: "Holdings Ltd"}, SharePercent: 60, ViaEntity: ""}, // corporate: no PersonRef name? still counted below
	}
	ubos := DeriveUBOs(shs, nil)
	// corporate shareholder without a natural-person identity is skipped only
	// if it cannot map to a natural person; here Holdings Ltd has a name so it
	// IS flagged (operator must resolve the natural person via ViaEntity).
	names := map[string]float64{}
	for _, u := range ubos {
		names[u.Name] = u.SharePercent
		if u.Source != "derived" {
			t.Fatalf("source: %+v", u)
		}
	}
	if names["A Bello"] != 40 || names["C Eze"] != 25.5 {
		t.Fatalf("ubos: %+v", ubos)
	}
	if _, ok := names["B Okafor"]; ok {
		t.Fatal("exactly 25% must NOT derive a UBO")
	}
	// declared UBO wins over derived on same identity
	declared := []UBO{{PersonRef: PersonRef{Name: "A Bello", NINHash: "h1", IDDocRef: "doc_1"}, SharePercent: 40}}
	merged := DeriveUBOs(shs, declared)
	count := 0
	for _, u := range merged {
		if u.NINHash == "h1" {
			count++
			if u.Source != "declared" || u.IDDocRef != "doc_1" {
				t.Fatalf("declared UBO should win: %+v", u)
			}
		}
	}
	if count != 1 {
		t.Fatalf("duplicate UBO for h1: %+v", merged)
	}
}

func TestCACHTTPAdapter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-CAC-Signature") == "" {
			t.Error("missing HMAC signature header")
		}
		var in struct {
			RC string `json:"rc_number"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in.RC != "RC123456" {
			t.Errorf("rc: %s", in.RC)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"valid": true, "verified": true, "company_name": "Test Ltd", "status": "active"})
	}))
	defer srv.Close()
	a := NewCACHTTPAdapter(srv.URL, "k")
	res, err := a.Verify("rc123456")
	if err != nil || !res.Verified || res.Company != "Test Ltd" || res.Provider != "cac_api" {
		t.Fatalf("verify: %+v err=%v", res, err)
	}
}

func TestAdaptersFromEnvProfiles(t *testing.T) {
	t.Setenv("NIMC_API_URL", "")
	t.Setenv("CAC_API_URL", "")
	if _, _, err := AdaptersFromEnv("prod"); err == nil {
		t.Fatal("prod without registry URLs must fail closed")
	}
	nin, cac, err := AdaptersFromEnv("dev")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := nin.(NINSimulator); !ok {
		t.Fatalf("dev nin adapter: %T", nin)
	}
	if _, ok := cac.(CACSimulator); !ok {
		t.Fatalf("dev cac adapter: %T", cac)
	}
	t.Setenv("NIMC_API_URL", "http://nimc.local")
	t.Setenv("CAC_API_URL", "http://cac.local")
	nin, cac, err = AdaptersFromEnv("prod")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := nin.(*NINHTTPAdapter); !ok {
		t.Fatalf("prod nin adapter: %T", nin)
	}
	if _, ok := cac.(*CACHTTPAdapter); !ok {
		t.Fatalf("prod cac adapter: %T", cac)
	}
	// partial config rejected
	t.Setenv("CAC_API_URL", "")
	if _, _, err := AdaptersFromEnv("prod"); err == nil {
		t.Fatal("partial prod config must be rejected")
	}
}
