package meridian

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// stubMux emulates the onboarding + tin-graph + ledger endpoints the SDK calls.
func stubMux(t *testing.T, seen map[string]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/operators", func(w http.ResponseWriter, r *http.Request) {
		seen["idem"] = r.Header.Get("Idempotency-Key")
		seen["role"] = r.Header.Get("X-Dev-Role")
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(map[string]any{"id": "op_1", "full_name": "Adaeze", "status": "registered", "agent_id": "ag_1"})
	})
	mux.HandleFunc("POST /v1/operators/op_1/status", func(w http.ResponseWriter, r *http.Request) {
		var in map[string]string
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in["to"] == "graduated" {
			w.WriteHeader(409)
			json.NewEncoder(w).Encode(map[string]any{"title": "illegal_transition", "detail": "registered -> graduated is not allowed"})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"id": "op_1", "status": in["to"]})
	})
	mux.HandleFunc("POST /v1/tin/provision", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"id": "run_1", "workflow": "wf-onb-tin-provision", "status": "completed", "attempt": 1})
	})
	mux.HandleFunc("GET /v1/onboarding/op_1", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"operator_id": "op_1", "status": "registered", "current_step": "identity_verification", "missing_items": []string{"nimc_verification"}})
	})
	mux.HandleFunc("POST /v1/agents", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(map[string]any{"id": "ag_1", "full_name": "Agent", "vetting_status": "pending"})
	})
	mux.HandleFunc("POST /v1/entities/e_1/kyb", func(w http.ResponseWriter, r *http.Request) {
		var cp CompanyProfile
		_ = json.NewDecoder(r.Body).Decode(&cp)
		if cp.Shareholders[0].SharePercent != 40 {
			t.Errorf("shareholder pct: %+v", cp.Shareholders)
		}
		json.NewEncoder(w).Encode(map[string]any{"entity": map[string]any{"id": "e_1", "entity_type": "company", "ubos": []map[string]any{{"name": "A Bello", "share_percent": 40, "source": "derived"}}}})
	})
	mux.HandleFunc("GET /v1/entities/e_1/ubos", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"entity_id": "e_1", "ubo_threshold_percent": 25, "ubos": []map[string]any{{"name": "A Bello", "share_percent": 40, "source": "derived"}}})
	})
	mux.HandleFunc("POST /v1/accounts", func(w http.ResponseWriter, r *http.Request) {
		seen["ledger_idem"] = r.Header.Get("Idempotency-Key")
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(map[string]any{"results": []string{"ok"}})
	})
	mux.HandleFunc("GET /v1/accounts/acct_1/balance", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"account_id": "acct_1", "posted_net": 5000, "available": 5000})
	})
	mux.HandleFunc("POST /v1/transfers", func(w http.ResponseWriter, r *http.Request) {
		var tr Transfer
		_ = json.NewDecoder(r.Body).Decode(&tr)
		if tr.Amount == 0 {
			t.Error("amount required")
		}
		tr.ID = "tx_1"
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(tr)
	})
	return httptest.NewServer(mux)
}

func TestOnboardingClient(t *testing.T) {
	seen := map[string]string{}
	srv := stubMux(t, seen)
	defer srv.Close()
	c := NewClient(srv.URL)
	ctx := context.Background()

	op, err := c.Onboarding().CreateOperator(ctx, OperatorCreate{NIN: "12345678901", FullName: "Adaeze", AgentID: "ag_1"}, NewIdempotencyKey())
	if err != nil || op.ID != "op_1" {
		t.Fatalf("create: %+v err=%v", op, err)
	}
	if seen["idem"] == "" || seen["role"] != "operator" {
		t.Fatalf("headers not sent: %v", seen)
	}

	if _, err := c.Onboarding().TransitionStatus(ctx, "op_1", "pending_review", "outage"); err != nil {
		t.Fatal(err)
	}
	// illegal transition surfaces a typed 409 Problem
	_, err = c.Onboarding().TransitionStatus(ctx, "op_1", "graduated", "")
	pb, ok := err.(*Problem)
	if !ok || pb.Status != 409 || pb.Title != "illegal_transition" {
		t.Fatalf("expected 409 Problem, got %v", err)
	}

	run, err := c.Onboarding().ProvisionTIN(ctx, "op_1", "12345678901")
	if err != nil || run.Status != "completed" {
		t.Fatalf("provision: %+v err=%v", run, err)
	}

	st, err := c.Onboarding().Resumption(ctx, "op_1")
	if err != nil || st.CurrentStep != "identity_verification" || len(st.MissingItems) == 0 {
		t.Fatalf("resumption: %+v err=%v", st, err)
	}

	ag, err := c.Onboarding().RegisterAgent(ctx, Agent{FullName: "Agent", Phone: "+234"})
	if err != nil || !strings.HasPrefix(ag.ID, "ag_") {
		t.Fatalf("agent: %+v err=%v", ag, err)
	}
}

func TestTinGraphClient(t *testing.T) {
	srv := stubMux(t, map[string]string{})
	defer srv.Close()
	c := NewClient(srv.URL)
	ctx := context.Background()
	cp := CompanyProfile{
		CompanyName: "Test Ltd", RCNumber: "RC123456",
		Shareholders: []Shareholder{{PersonRef: PersonRef{Name: "A Bello"}, SharePercent: 40}},
	}
	e, err := c.TinGraph().UpdateKYB(ctx, "e_1", cp)
	if err != nil || len(e.UBOs) != 1 || e.UBOs[0].Source != "derived" {
		t.Fatalf("kyb: %+v err=%v", e, err)
	}
	uv, err := c.TinGraph().EntityUBOs(ctx, "e_1")
	if err != nil || uv.UboThresholdPct != 25 || len(uv.UBOs) != 1 {
		t.Fatalf("ubos: %+v err=%v", uv, err)
	}
}

func TestLedgerClient(t *testing.T) {
	seen := map[string]string{}
	srv := stubMux(t, seen)
	defer srv.Close()
	c := NewClient(srv.URL)
	ctx := context.Background()
	if err := c.Ledger().CreateAccounts(ctx, []Account{{ID: "acct_1", Ledger: 700, Code: 5}}, NewIdempotencyKey()); err != nil {
		t.Fatal(err)
	}
	if seen["ledger_idem"] == "" {
		t.Fatal("idempotency key not sent to ledger")
	}
	b, err := c.Ledger().Balance(ctx, "acct_1")
	if err != nil || b.PostedNet != 5000 {
		t.Fatalf("balance: %+v err=%v", b, err)
	}
	tr, err := c.Ledger().Transfer(ctx, Transfer{DebitAccountID: "a", CreditAccountID: "acct_1", Amount: 100, Ledger: 700, Code: 4}, "")
	if err != nil || tr.ID != "tx_1" {
		t.Fatalf("transfer: %+v err=%v", tr, err)
	}
}
