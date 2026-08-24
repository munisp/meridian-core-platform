package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"
)

// ---------- rule packs (rp-registry with dev-seed fallback) ----------

// packsView returns pack summaries + source ("live" | "dev-seed").
func (a *app) packsView() ([]PackSummary, string) {
	if base, ok := a.serviceURL("rp-registry"); ok {
		var raw json.RawMessage
		if err := fetchJSON(a.client, base+"/v1/packs", &raw); err == nil {
			var packs []PackSummary
			if json.Unmarshal(raw, &packs) == nil && len(packs) > 0 {
				return packs, "live"
			}
			// tolerate object-wrapped responses
			var wrapped struct {
				Packs []PackSummary `json:"packs"`
			}
			if json.Unmarshal(raw, &wrapped) == nil && len(wrapped.Packs) > 0 {
				return wrapped.Packs, "live"
			}
		}
	}
	return packSeeds(), "dev-seed"
}

func (a *app) handlePacks(w http.ResponseWriter, r *http.Request) {
	packs, source := a.packsView()
	stale := []string{}
	for _, p := range packs {
		if p.StaleConsumers > 0 {
			stale = append(stale, p.ID)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"packs":           packs,
		"source":          source,
		"stale_consumers": stale,
	})
}

func (a *app) handlePackGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if base, ok := a.serviceURL("rp-registry"); ok {
		var raw json.RawMessage
		if err := fetchJSON(a.client, base+"/v1/packs/"+id+"/latest", &raw); err == nil {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Source", "live")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(raw)
			return
		}
	}
	// dev-seed fallback: synthesise a detail view from the summary
	for _, p := range packSeeds() {
		if p.ID == id {
			yaml := "id: " + p.ID + "\nversion: " + p.LatestVersion + "\neffective_from: " + p.EffectiveFrom +
				"\nstatus: " + p.Status + "\nsubject_to_regazette: true\nprovenance:\n  source_citation: \"" +
				p.SourceCitation + "\"\nrules:\n  - id: example.rule\n    when: { }\n    then: { narrate: \"dev seed placeholder — start rp-registry for the full pack\" }\n"
			writeJSON(w, http.StatusOK, map[string]any{
				"summary": p,
				"yaml":    yaml,
				"signature": map[string]any{
					"algorithm": "ed25519", "key_id": "governance-board-2026",
					"verified": p.Signed,
				},
				"source": "dev-seed",
			})
			return
		}
	}
	writeProblem(w, http.StatusNotFound, "pack not found", id)
}

func (a *app) handlePackPublish(w http.ResponseWriter, r *http.Request) {
	id, ver := r.PathValue("id"), r.PathValue("ver")
	if base, ok := a.serviceURL("rp-registry"); ok {
		var raw json.RawMessage
		if err := postJSON(a.client, base+"/v1/packs/"+id+"/"+ver+"/publish", map[string]any{}, &raw); err == nil {
			a.appendAudit("rulepack.published", id+"@"+ver, actorOf(r), "publish", "via rp-registry (live)")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(raw)
			return
		}
	}
	// dev-seed: emulate publish transition draft|review|simulation -> published
	a.appendAudit("rulepack.published", id+"@"+ver, actorOf(r), "publish", "dev-seed emulation (rp-registry unreachable)")
	writeJSON(w, http.StatusOK, map[string]any{
		"id": id, "version": ver, "status": "published",
		"event":  "nrs.rulepacks.published.v1",
		"source": "dev-seed",
		"note":   "rp-registry unreachable; publish emulated locally and audited",
	})
}

// ---------- gates & reg-watch ----------

func (a *app) handleGates(w http.ResponseWriter, r *http.Request) {
	if base, ok := a.serviceURL("reg-watch"); ok {
		var gates []Gate
		if err := fetchJSON(a.client, base+"/v1/gates", &gates); err == nil && len(gates) > 0 {
			for i := range gates {
				gates[i].Source = "live"
			}
			writeJSON(w, http.StatusOK, map[string]any{"gates": gates, "source": "live"})
			return
		}
	}
	a.store.mu.Lock()
	out := make([]*Gate, 0, len(a.store.Gates))
	for _, g := range a.store.Gates {
		out = append(out, g)
	}
	a.store.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	writeJSON(w, http.StatusOK, map[string]any{"gates": out, "source": "dev-seed"})
}

func (a *app) handleGateFlip(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Confirm bool   `json:"confirm"`
		Reason  string `json:"reason"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if !body.Confirm {
		writeProblem(w, http.StatusBadRequest, "confirmation required", "gate flips are board-authorised; send confirm=true")
		return
	}
	if base, ok := a.serviceURL("reg-watch"); ok {
		var raw json.RawMessage
		if err := postJSON(a.client, base+"/v1/gates/"+id+"/flip", body, &raw); err == nil {
			a.appendAudit("gate.flipped", id, actorOf(r), "flip", body.Reason+" (live reg-watch)")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(raw)
			return
		}
	}
	a.store.mu.Lock()
	g, ok := a.store.Gates[id]
	if !ok {
		g = &Gate{ID: id, Name: id, Source: "dev-seed"}
		a.store.Gates[id] = g
	}
	g.State = !g.State
	g.ArmedBy = actorOf(r)
	g.UpdatedAt = nowRFC3339()
	a.store.mu.Unlock()
	a.appendAudit("gate.flipped", id, actorOf(r), "flip", body.Reason+" (dev-seed local)")
	writeJSON(w, http.StatusOK, g)
}

func (a *app) handleGazetteWatch(w http.ResponseWriter, r *http.Request) {
	if base, ok := a.serviceURL("reg-watch"); ok {
		var raw json.RawMessage
		if err := fetchJSON(a.client, base+"/v1/gazette-watch", &raw); err == nil {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Source", "live")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(raw)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"source": "dev-seed",
		"watch": []map[string]string{
			{"instrument": "Deduction of Tax at Source (WHT) Regulations 2024", "status": "gazetted — CTC confirmation pending", "gate": "G1", "checked_at": "2025-06-24T06:00:00Z"},
			{"instrument": "Nigeria Tax Act (ETR/GloBE commencement)", "status": "passed — awaiting commencement circular", "gate": "qdmtt_upgrade", "checked_at": "2025-06-24T06:00:00Z"},
			{"instrument": "Presumptive tax regulation", "status": "draft in gazette queue", "gate": "G8", "checked_at": "2025-06-24T06:00:00Z"},
			{"instrument": "Rivers VAT attribution case", "status": "awaiting judgement — dual_shadow remains on", "gate": "G2", "checked_at": "2025-06-24T06:00:00Z"},
		},
	})
}

// ---------- audit & WORM evidence ----------

// appendAudit records a privileged mutation. The event is kept in the local
// store (dev read-path fallback) AND forwarded to the WORM audit-evidence
// service (B4-6): previously the in-mem slice was the only sink, so every
// admin-plane mutation vanished on restart and never reached the immutable
// trail that the live read path queries. Forward failures are queued and
// retried (auditFlushLoop) — never silently dropped.
func (a *app) appendAudit(typ, subject, actor, action, detail string) {
	ev := AuditEvent{
		ID: newID("ae"), Type: typ, Subject: subject, Actor: actor,
		Action: action, Detail: detail, Timestamp: nowRFC3339(),
	}
	a.store.mu.Lock()
	a.store.AuditEvents = append([]AuditEvent{ev}, a.store.AuditEvents...)
	a.store.mu.Unlock()
	a.forwardAudit(ev)
}

// forwardAudit posts one event to the audit-evidence WORM store; on failure
// it queues the event for retry. audit-evidence attributes the event to the
// authenticated principal itself, so the human actor travels in details.
func (a *app) forwardAudit(ev AuditEvent) {
	if err := a.postAuditEvent(ev); err != nil {
		log.Printf("component=admin-api audit WORM forward FAILED (%v); queued for retry (queue depth grows until audit-evidence recovers)", err)
		a.auditMu.Lock()
		a.auditQueue = append(a.auditQueue, ev)
		a.auditMu.Unlock()
	}
}

// postAuditEvent performs a single delivery attempt to audit-evidence.
func (a *app) postAuditEvent(ev AuditEvent) error {
	base, ok := a.serviceURL("audit-evidence")
	if !ok || base == "" {
		return errString("audit-evidence service URL not configured")
	}
	payload := map[string]any{
		"subject": ev.Subject,
		"action":  ev.Action,
		"type":    ev.Type,
		"details": map[string]any{
			"actor":  ev.Actor,
			"detail": ev.Detail,
			"id":     ev.ID,
			"ts":     ev.Timestamp,
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, base+"/v1/audit/events", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if a.authMode == "dev" {
		// dev shared-secret auth; audit-evidence attributes the event to the
		// service principal and keeps the human actor in details.
		req.Header.Set("X-Dev-Role", "admin")
	}
	resp, err := a.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 300 {
		return errString("audit-evidence returned " + resp.Status)
	}
	return nil
}

// httpClient returns the downstream client (nil-safe for bare test apps).
func (a *app) httpClient() *http.Client {
	if a.client != nil {
		return a.client
	}
	return &http.Client{Timeout: 5 * time.Second}
}

// auditFlushLoop retries queued WORM forwards until they land.
func (a *app) auditFlushLoop() {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for range t.C {
		a.flushAuditQueue()
	}
}

// flushAuditQueue attempts one delivery per queued event; events that still
// fail remain queued (order preserved).
func (a *app) flushAuditQueue() {
	a.auditMu.Lock()
	pending := append([]AuditEvent(nil), a.auditQueue...)
	a.auditQueue = nil
	a.auditMu.Unlock()
	if len(pending) == 0 {
		return
	}
	var still []AuditEvent
	for _, ev := range pending {
		if err := a.postAuditEvent(ev); err != nil {
			still = append(still, ev)
		}
	}
	if len(still) > 0 {
		a.auditMu.Lock()
		a.auditQueue = append(still, a.auditQueue...)
		a.auditMu.Unlock()
	}
}

// auditQueueDepth exposes the pending-forward depth (tests + metrics).
func (a *app) auditQueueDepth() int {
	a.auditMu.Lock()
	defer a.auditMu.Unlock()
	return len(a.auditQueue)
}

func (a *app) handleAuditEvents(w http.ResponseWriter, r *http.Request) {
	subject := r.URL.Query().Get("subject")
	typ := r.URL.Query().Get("type")
	if base, ok := a.serviceURL("audit-evidence"); ok {
		var events []AuditEvent
		url := base + "/v1/audit/events"
		if subject != "" {
			url += "?subject=" + subject
		}
		if err := fetchJSON(a.client, url, &events); err == nil {
			writeJSON(w, http.StatusOK, map[string]any{"events": events, "source": "live"})
			return
		}
	}
	a.store.mu.Lock()
	out := make([]AuditEvent, 0, len(a.store.AuditEvents))
	for _, e := range a.store.AuditEvents {
		if subject != "" && !strings.Contains(e.Subject, subject) {
			continue
		}
		if typ != "" && !strings.HasPrefix(e.Type, typ) {
			continue
		}
		out = append(out, e)
	}
	a.store.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"events": out, "source": "dev-seed"})
}

func (a *app) handleAuditAppend(w http.ResponseWriter, r *http.Request) {
	var e AuditEvent
	if !decodeJSON(w, r, &e) {
		return
	}
	if e.Type == "" || e.Subject == "" {
		writeProblem(w, http.StatusBadRequest, "invalid event", "type and subject required")
		return
	}
	e.ID = newID("ae")
	e.Timestamp = nowRFC3339()
	a.store.mu.Lock()
	a.store.AuditEvents = append([]AuditEvent{e}, a.store.AuditEvents...)
	a.store.mu.Unlock()
	writeJSON(w, http.StatusCreated, &e)
}

func sealEvidence(e *EvidenceObject) {
	sum := sha256.Sum256([]byte(e.Content))
	e.SHA256 = hex.EncodeToString(sum[:])
	e.SizeBytes = len(e.Content)
	e.Immutable = true
}

func (a *app) handleEvidenceList(w http.ResponseWriter, r *http.Request) {
	a.store.mu.Lock()
	out := make([]*EvidenceObject, 0, len(a.store.Evidence))
	for _, e := range a.store.Evidence {
		cp := *e
		sealEvidence(&cp)
		out = append(out, &cp)
	}
	a.store.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	writeJSON(w, http.StatusOK, map[string]any{"evidence": out, "source": "dev-seed"})
}

func (a *app) handleEvidenceGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if base, ok := a.serviceURL("audit-evidence"); ok {
		var raw json.RawMessage
		if err := fetchJSON(a.client, base+"/v1/evidence/"+id, &raw); err == nil {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Source", "live")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(raw)
			return
		}
	}
	a.store.mu.Lock()
	e, ok := a.store.Evidence[id]
	a.store.mu.Unlock()
	if !ok {
		writeProblem(w, http.StatusNotFound, "evidence not found", id)
		return
	}
	cp := *e
	sealEvidence(&cp)
	writeJSON(w, http.StatusOK, map[string]any{"evidence": &cp, "source": "dev-seed"})
}

func (a *app) handleEvidenceCreate(w http.ResponseWriter, r *http.Request) {
	var in EvidenceObject
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.Content == "" {
		writeProblem(w, http.StatusBadRequest, "invalid evidence", "content required (WORM write-once)")
		return
	}
	in.ID = newID("ev")
	in.CreatedAt = nowRFC3339()
	if in.CreatedBy == "" {
		in.CreatedBy = actorOf(r)
	}
	if in.WORMURI == "" {
		in.WORMURI = "worm://local/evidence/" + in.ID + ".json"
	}
	sealEvidence(&in)
	a.store.mu.Lock()
	a.store.Evidence[in.ID] = &in
	a.store.mu.Unlock()
	a.appendAudit("evidence.worm_write", in.ID, in.CreatedBy, "seal", in.Kind)
	writeJSON(w, http.StatusCreated, &in)
}

// TAT: technical-audit-trail — who saw what when under which rule_pack_version.
func (a *app) handleTATAssemble(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Subject string `json:"subject"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if base, ok := a.serviceURL("audit-evidence"); ok {
		var raw json.RawMessage
		if err := postJSON(a.client, base+"/v1/tat/assemble", req, &raw); err == nil {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Source", "live")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(raw)
			return
		}
	}
	a.store.mu.Lock()
	var trail []AuditEvent
	for _, e := range a.store.AuditEvents {
		if req.Subject == "" || strings.Contains(e.Subject, req.Subject) {
			trail = append(trail, e)
		}
	}
	a.store.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"subject": req.Subject,
		"entries": trail,
		"assembled_at": nowRFC3339(),
		"source":  "dev-seed",
		"note":    "audit-evidence svc unreachable; TAT assembled from admin-api local audit store",
	})
}

// ---------- cross-zone flows ----------

func (a *app) handleFlowMatrix(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"flows": flowMatrix()})
}

func (a *app) handleFlowReceipts(w http.ResponseWriter, r *http.Request) {
	a.store.mu.Lock()
	out := make([]FlowReceipt, len(a.store.Receipts))
	copy(out, a.store.Receipts)
	a.store.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Timestamp > out[j].Timestamp })
	writeJSON(w, http.StatusOK, map[string]any{
		"receipts": out,
		"source":   "dev-seed",
		"note":     "in production these are WORM receipts served by audit-evidence / enclave-gateway",
	})
}

func (a *app) handleFlowReceiptAppend(w http.ResponseWriter, r *http.Request) {
	var rc FlowReceipt
	if !decodeJSON(w, r, &rc) {
		return
	}
	if rc.Flow == "" {
		writeProblem(w, http.StatusBadRequest, "invalid receipt", "flow required")
		return
	}
	rc.ID = newID("rcpt")
	rc.Timestamp = nowRFC3339()
	a.store.mu.Lock()
	if rc.Flow == "F9" || rc.Flow == "F10" {
		a.store.Forbidden = append(a.store.Forbidden, rc)
		a.store.mu.Unlock()
		a.appendAudit("crosszone.forbidden_attempt", "flow:"+rc.Flow, rc.Sender, "deny", rc.Detail)
		writeProblem(w, http.StatusUnprocessableEntity, "forbidden flow",
			rc.Flow+" is forbidden by construction; attempt recorded and alerted")
		return
	}
	a.store.Receipts = append(a.store.Receipts, rc)
	a.store.mu.Unlock()
	writeJSON(w, http.StatusCreated, &rc)
}

// Forbidden-flow monitor — must always be empty.
func (a *app) handleForbiddenFlows(w http.ResponseWriter, r *http.Request) {
	a.store.mu.Lock()
	sightings := make([]FlowReceipt, len(a.store.Forbidden))
	copy(sightings, a.store.Forbidden)
	a.store.mu.Unlock()
	status := "clean"
	if len(sightings) > 0 {
		status = "ALERT"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":    status,
		"sightings": sightings,
		"invariant": "F9/F10 have no code path; any sighting is a security incident",
	})
}
