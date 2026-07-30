package main

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"sort"
)

// ---------- ledger views (ledger svc with dev-seed fallback) ----------

func (a *app) handleLedgerAccounts(w http.ResponseWriter, r *http.Request) {
	a.store.mu.Lock()
	out := make([]*LedgerAccount, 0, len(a.store.LedgerAccounts))
	for _, ac := range a.store.LedgerAccounts {
		out = append(out, ac)
	}
	a.store.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	writeJSON(w, http.StatusOK, map[string]any{"accounts": out, "source": "dev-seed"})
}

func (a *app) handleLedgerBalance(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if base, ok := a.serviceURL("ledger"); ok {
		var raw map[string]any
		if err := fetchJSON(a.client, base+"/v1/accounts/"+id+"/balance", &raw); err == nil {
			raw["source"] = "live"
			writeJSON(w, http.StatusOK, raw)
			return
		}
	}
	a.store.mu.Lock()
	ac, ok := a.store.LedgerAccounts[id]
	a.store.mu.Unlock()
	if !ok {
		writeProblem(w, http.StatusNotFound, "account not found", id)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"account_id": ac.ID, "balance_kobo": ac.Balance, "currency": ac.Currency, "source": "dev-seed",
	})
}

func (a *app) handleLedgerTransfer(w http.ResponseWriter, r *http.Request) {
	var in LedgerTransfer
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.DebitAccountID == "" || in.CreditAccountID == "" || in.AmountKobo <= 0 {
		writeProblem(w, http.StatusBadRequest, "invalid transfer", "debit/credit account and positive integer kobo amount required")
		return
	}
	// try live ledger pending transfer
	if base, ok := a.serviceURL("ledger"); ok {
		var raw map[string]any
		if err := postJSON(a.client, base+"/v1/transfers/pending", in, &raw); err == nil {
			raw["source"] = "live"
			a.appendAudit("ledger.transfer", "transfer:"+in.ID, actorOf(r), "pending", "via ledger svc (live)")
			writeJSON(w, http.StatusCreated, raw)
			return
		}
	}
	in.ID = newID("tr")
	in.State = "pending"
	in.Code = 1
	in.CreatedAt = nowRFC3339()
	a.store.mu.Lock()
	a.store.LedgerTransfers[in.ID] = &in
	a.store.mu.Unlock()
	a.appendAudit("ledger.transfer", "transfer:"+in.ID, actorOf(r), "pending", "dev-seed in-memory ledger")
	writeJSON(w, http.StatusCreated, &in)
}

func (a *app) settleTransfer(w http.ResponseWriter, r *http.Request, action string) {
	id := r.PathValue("id")
	if base, ok := a.serviceURL("ledger"); ok {
		var raw map[string]any
		if err := postJSON(a.client, base+"/v1/transfers/"+id+"/"+action, map[string]any{}, &raw); err == nil {
			raw["source"] = "live"
			writeJSON(w, http.StatusOK, raw)
			return
		}
	}
	a.store.mu.Lock()
	tr, ok := a.store.LedgerTransfers[id]
	if !ok {
		a.store.mu.Unlock()
		writeProblem(w, http.StatusNotFound, "transfer not found", id)
		return
	}
	if tr.State != "pending" {
		a.store.mu.Unlock()
		writeProblem(w, http.StatusConflict, "transfer not pending", "current state: "+tr.State)
		return
	}
	if action == "post" {
		tr.State = "posted"
		tr.Code = 2
		if d, ok := a.store.LedgerAccounts[tr.DebitAccountID]; ok {
			d.Balance -= tr.AmountKobo
		}
		if c, ok := a.store.LedgerAccounts[tr.CreditAccountID]; ok {
			c.Balance += tr.AmountKobo
		}
	} else {
		tr.State = "voided"
		tr.Code = 3
	}
	a.store.mu.Unlock()
	a.appendAudit("ledger.transfer", "transfer:"+id, actorOf(r), action, "dev-seed in-memory ledger")
	writeJSON(w, http.StatusOK, tr)
}

func (a *app) handleLedgerPost(w http.ResponseWriter, r *http.Request) { a.settleTransfer(w, r, "post") }
func (a *app) handleLedgerVoid(w http.ResponseWriter, r *http.Request) { a.settleTransfer(w, r, "void") }

func (a *app) handleReconBreaks(w http.ResponseWriter, r *http.Request) {
	if base, ok := a.serviceURL("settlement"); ok {
		var breaks []ReconBreak
		if err := fetchJSON(a.client, base+"/v1/recon/breaks", &breaks); err == nil {
			writeJSON(w, http.StatusOK, map[string]any{"breaks": breaks, "source": "live"})
			return
		}
	}
	a.store.mu.Lock()
	out := a.store.ReconBreaks
	a.store.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"breaks": out, "source": "dev-seed"})
}

// ---------- workflows ----------

func (a *app) handleWorkflows(w http.ResponseWriter, r *http.Request) {
	a.store.mu.Lock()
	defs := a.store.WorkflowDefs
	a.store.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"workflows": defs,
		"source":    "static-catalog",
		"note":      "set TEMPORAL_UI_URL on the console for live run inspection",
	})
}

func (a *app) handleWorkflowTrigger(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Input string `json:"input"`
	}
	_ = decodeJSON(w, r, &body)
	a.store.mu.Lock()
	var def *WorkflowDef
	for i := range a.store.WorkflowDefs {
		if a.store.WorkflowDefs[i].ID == id {
			def = &a.store.WorkflowDefs[i]
			break
		}
	}
	if def == nil || !def.Triggerable {
		a.store.mu.Unlock()
		writeProblem(w, http.StatusNotFound, "workflow not triggerable", id)
		return
	}
	run := WorkflowRun{
		ID: newID("run"), WorkflowID: id, Status: "completed",
		Triggered: actorOf(r), Input: body.Input,
		StartedAt: nowRFC3339(), FinishedAt: nowRFC3339(),
	}
	a.store.WorkflowRuns = append([]WorkflowRun{run}, a.store.WorkflowRuns...)
	a.store.mu.Unlock()
	a.appendAudit("workflow.triggered", id, actorOf(r), "trigger", body.Input)
	writeJSON(w, http.StatusCreated, map[string]any{
		"run": run, "mode": "dev-inproc",
		"note": "TEMPORAL_URL unset — executed via dev in-process runner semantics",
	})
}

func (a *app) handleWorkflowRuns(w http.ResponseWriter, r *http.Request) {
	a.store.mu.Lock()
	runs := a.store.WorkflowRuns
	a.store.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs, "source": "dev-seed"})
}

// ---------- settings ----------

func (a *app) handleFlags(w http.ResponseWriter, r *http.Request) {
	a.store.mu.Lock()
	flags := map[string]bool{}
	for k, v := range a.store.Flags {
		flags[k] = v
	}
	a.store.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"flags": flags})
}

func (a *app) handleFlagsUpdate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Flags map[string]bool `json:"flags"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	a.store.mu.Lock()
	for k, v := range body.Flags {
		a.store.Flags[k] = v
	}
	a.store.mu.Unlock()
	a.appendAudit("settings.flags_updated", "feature-flags", actorOf(r), "update", "")
	writeJSON(w, http.StatusOK, map[string]any{"flags": body.Flags})
}

func (a *app) handleAPIKeys(w http.ResponseWriter, r *http.Request) {
	a.store.mu.Lock()
	out := a.store.APIKeys
	a.store.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"api_keys": out})
}

func (a *app) handleAPIKeyCreate(w http.ResponseWriter, r *http.Request) {
	var in APIKey
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.Name == "" {
		writeProblem(w, http.StatusBadRequest, "invalid key", "name required")
		return
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	secret := hex.EncodeToString(b)
	in.ID = newID("key")
	in.Prefix = "mk_live_" + secret[:4]
	in.SecretTail = "…" + secret[len(secret)-4:]
	in.CreatedAt = nowRFC3339()
	a.store.mu.Lock()
	a.store.APIKeys = append(a.store.APIKeys, in)
	a.store.mu.Unlock()
	a.appendAudit("settings.apikey_created", in.ID, actorOf(r), "create", in.Name)
	writeJSON(w, http.StatusCreated, map[string]any{
		"key": in,
		"secret_once": "mk_live_" + secret,
		"note": "full secret shown once; store it now",
	})
}

func (a *app) handleAPIKeyRevoke(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	a.store.mu.Lock()
	found := false
	for i := range a.store.APIKeys {
		if a.store.APIKeys[i].ID == id {
			a.store.APIKeys[i].Revoked = true
			found = true
		}
	}
	a.store.mu.Unlock()
	if !found {
		writeProblem(w, http.StatusNotFound, "key not found", id)
		return
	}
	a.appendAudit("settings.apikey_revoked", id, actorOf(r), "revoke", "")
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "status": "revoked"})
}

func (a *app) handleNotifProviders(w http.ResponseWriter, r *http.Request) {
	a.store.mu.Lock()
	out := a.store.NotifProviders
	a.store.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"providers": out})
}

func (a *app) handleRoutes(w http.ResponseWriter, r *http.Request) {
	if base, ok := a.serviceURL("edge-policy"); ok {
		var raw []RouteRow
		if err := fetchJSON(a.client, base+"/v1/routes", &raw); err == nil {
			writeJSON(w, http.StatusOK, map[string]any{"routes": raw, "source": "live"})
			return
		}
	}
	a.store.mu.Lock()
	out := a.store.Routes
	waf := a.store.WAFMode
	a.store.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"routes": out, "waf_mode": waf, "source": "dev-seed"})
}

func (a *app) handleWAFMode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Mode string `json:"mode"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Mode != "detect" && body.Mode != "enforce" {
		writeProblem(w, http.StatusBadRequest, "invalid mode", "must be detect|enforce")
		return
	}
	if base, ok := a.serviceURL("edge-policy"); ok {
		var raw map[string]any
		if err := postJSON(a.client, base+"/v1/waf/mode", body, &raw); err == nil {
			raw["source"] = "live"
			writeJSON(w, http.StatusOK, raw)
			return
		}
	}
	a.store.mu.Lock()
	a.store.WAFMode = body.Mode
	a.store.mu.Unlock()
	a.appendAudit("settings.waf_mode", "edge-waf", actorOf(r), body.Mode, "")
	writeJSON(w, http.StatusOK, map[string]string{"waf_mode": body.Mode, "source": "dev-seed"})
}
