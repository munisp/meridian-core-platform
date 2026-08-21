package main

import (
	"log"
	"net/http"
	"sort"
	"time"
)

// cloneUser deep-copies a User so callers can safely use the value after
// releasing store.mu (FF-1: shared *User pointers were JSON-encoded after
// unlock while handleUserUpdate mutated them in place).
func cloneUser(u *User) *User {
	if u == nil {
		return nil
	}
	c := *u
	if u.Roles != nil {
		c.Roles = append([]string(nil), u.Roles...)
	}
	return &c
}

// ---------- login / me ----------

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (a *app) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	a.store.mu.Lock()
	u, ok := a.store.Users[req.Email]
	if ok {
		u = cloneUser(u) // copy under lock; safe to read after unlock
	}
	hash := ""
	if ok {
		hash = u.PasswordHash
	}
	a.store.mu.Unlock()
	// A6: PBKDF2-SHA256 verification (constant-time); plaintext is never
	// stored or compared.
	if !ok || u.Status != "active" || hash == "" || !VerifyPassword(hash, req.Password) {
		writeProblem(w, http.StatusUnauthorized, "invalid credentials", "email or password incorrect")
		return
	}
	c := claims{
		Sub:      u.ID,
		Email:    u.Email,
		Roles:    u.Roles,
		TenantID: u.TenantID,
		Issuer:   "admin-api-dev",
		IssuedAt: time.Now().Unix(),
		Expires:  time.Now().Add(12 * time.Hour).Unix(),
	}
	tok, err := a.issueJWT(c)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "token issue failed", err.Error())
		return
	}
	a.appendAudit("auth.login", "user:"+u.Email, u.Email, "login", "dev JWT issued")
	writeJSON(w, http.StatusOK, map[string]any{
		"token":    tok,
		"token_type": "Bearer",
		"expires_in": 12 * 3600,
		"dev_mode":   a.authMode == "dev",
		"user":       u,
	})
}

func (a *app) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, getClaims(r))
}

// ---------- overview (SPEC §2) ----------

func (a *app) handleOverview(w http.ResponseWriter, r *http.Request) {
	packs, packsSource := a.packsView()
	a.store.mu.Lock()
	tenants := len(a.store.Tenants)
	workflows := len(a.store.WorkflowDefs)
	evidence := len(a.store.Evidence)
	transfers := len(a.store.LedgerTransfers)
	gates := map[string]bool{}
	for id, g := range a.store.Gates {
		gates[id] = g.State
	}
	runs := len(a.store.WorkflowRuns)
	a.store.mu.Unlock()

	// try live counts from ledger/audit-evidence
	transferSource := "dev-seed"
	if base, ok := a.serviceURL("ledger"); ok {
		var live []map[string]any
		if err := fetchJSON(a.client, base+"/v1/accounts", &live); err == nil {
			transferSource = "live"
		}
	}

	healthy, total := 0, 0
	for _, s := range a.rollup(false) {
		if !s.Enabled {
			continue
		}
		total++
		if s.HealthStatus == "ok" {
			healthy++
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"packs":            map[string]any{"count": len(packs), "source": packsSource},
		"tenants":          map[string]any{"count": tenants, "source": "local"},
		"workflows":        map[string]any{"count": workflows, "recent_runs": runs, "source": "catalog"},
		"transfers":        map[string]any{"count": transfers, "source": transferSource},
		"evidence_objects": map[string]any{"count": evidence, "source": "dev-seed"},
		"gates":            gates,
		"services":         map[string]any{"healthy": healthy, "total": total},
		"generated_at":     nowRFC3339(),
	})
}

// ---------- tenants CRUD ----------

func validIsolation(s string) bool {
	return s == "enclave" || s == "schema" || s == "row"
}

func (a *app) handleTenants(w http.ResponseWriter, r *http.Request) {
	a.store.mu.Lock()
	out := make([]*Tenant, 0, len(a.store.Tenants))
	for _, t := range a.store.Tenants {
		out = append(out, t)
	}
	a.store.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	writeJSON(w, http.StatusOK, out)
}

func (a *app) handleTenantCreate(w http.ResponseWriter, r *http.Request) {
	var t Tenant
	if !decodeJSON(w, r, &t) {
		return
	}
	if t.Name == "" || !validIsolation(t.Isolation) {
		writeProblem(w, http.StatusBadRequest, "invalid tenant", "name required; isolation must be enclave|schema|row")
		return
	}
	t.ID = newID("t")
	if t.Slug == "" {
		t.Slug = t.ID
	}
	if t.Status == "" {
		t.Status = "active"
	}
	t.CreatedAt = nowRFC3339()
	a.store.mu.Lock()
	a.store.Tenants[t.ID] = &t
	a.store.mu.Unlock()
	a.appendAudit("tenant.created", "tenant:"+t.ID, actorOf(r), "create", t.Name)
	writeJSON(w, http.StatusCreated, &t)
}

func (a *app) handleTenantGet(w http.ResponseWriter, r *http.Request) {
	a.store.mu.Lock()
	t, ok := a.store.Tenants[r.PathValue("id")]
	a.store.mu.Unlock()
	if !ok {
		writeProblem(w, http.StatusNotFound, "tenant not found", "")
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (a *app) handleTenantUpdate(w http.ResponseWriter, r *http.Request) {
	var in Tenant
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.Isolation != "" && !validIsolation(in.Isolation) {
		writeProblem(w, http.StatusBadRequest, "invalid isolation", "must be enclave|schema|row")
		return
	}
	a.store.mu.Lock()
	t, ok := a.store.Tenants[r.PathValue("id")]
	if ok {
		if in.Name != "" {
			t.Name = in.Name
		}
		if in.Isolation != "" {
			t.Isolation = in.Isolation
		}
		if in.Status != "" {
			t.Status = in.Status
		}
		if in.ContactMail != "" {
			t.ContactMail = in.ContactMail
		}
		t.Notes = in.Notes
	}
	a.store.mu.Unlock()
	if !ok {
		writeProblem(w, http.StatusNotFound, "tenant not found", "")
		return
	}
	a.appendAudit("tenant.updated", "tenant:"+t.ID, actorOf(r), "update", t.Name)
	writeJSON(w, http.StatusOK, t)
}

func (a *app) handleTenantDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	a.store.mu.Lock()
	_, ok := a.store.Tenants[id]
	delete(a.store.Tenants, id)
	a.store.mu.Unlock()
	if !ok {
		writeProblem(w, http.StatusNotFound, "tenant not found", "")
		return
	}
	a.appendAudit("tenant.deleted", "tenant:"+id, actorOf(r), "delete", "")
	w.WriteHeader(http.StatusNoContent)
}

// ---------- users CRUD ----------

func (a *app) handleUsers(w http.ResponseWriter, r *http.Request) {
	a.store.mu.Lock()
	out := make([]*User, 0, len(a.store.Users))
	for _, u := range a.store.Users {
		out = append(out, cloneUser(u)) // copy values under lock (FF-1)
	}
	a.store.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Email < out[j].Email })
	writeJSON(w, http.StatusOK, out)
}

func (a *app) handleUserCreate(w http.ResponseWriter, r *http.Request) {
	var u User
	if !decodeJSON(w, r, &u) {
		return
	}
	if u.Email == "" || u.Name == "" {
		writeProblem(w, http.StatusBadRequest, "invalid user", "email and name required")
		return
	}
	a.store.mu.Lock()
	if _, dup := a.store.Users[u.Email]; dup {
		a.store.mu.Unlock()
		writeProblem(w, http.StatusConflict, "user exists", u.Email)
		return
	}
	u.ID = newID("u")
	if u.Status == "" {
		u.Status = "active"
	}
	if len(u.Roles) == 0 {
		u.Roles = []string{"operator"}
	}
	if u.Password == "" {
		if a.authMode != "dev" {
			a.store.mu.Unlock()
			writeProblem(w, http.StatusBadRequest, "password required",
				"an explicit password is required when AUTH_MODE != dev (no default credentials)")
			return
		}
		// Dev only: generate a one-off password, log it once, force reset.
		u.Password = generatePassword()
		u.ForcePasswordReset = true
		log.Printf("component=admin-api dev-only: generated password for new user %s: %s (force-reset on first login)", u.Email, u.Password)
	}
	u.PasswordHash = MustHashPassword(u.Password)
	u.Password = "" // never retain plaintext
	u.CreatedAt = nowRFC3339()
	a.store.Users[u.Email] = &u
	a.upsertUserPg(&u)
	a.store.mu.Unlock()
	a.appendAudit("user.created", "user:"+u.Email, actorOf(r), "create", u.Name)
	writeJSON(w, http.StatusCreated, &u)
}

func (a *app) handleUserUpdate(w http.ResponseWriter, r *http.Request) {
	var in User
	if !decodeJSON(w, r, &in) {
		return
	}
	a.store.mu.Lock()
	var target *User
	var email string
	for e, u := range a.store.Users {
		if u.ID == r.PathValue("id") {
			email = e
			// FF-1: copy-on-write — never mutate a shared *User in place;
			// build the updated copy and swap the map pointer atomically
			// under the lock so readers never observe a half-updated user.
			target = cloneUser(u)
			break
		}
	}
	if target != nil {
		if in.Name != "" {
			target.Name = in.Name
		}
		if len(in.Roles) > 0 {
			target.Roles = append([]string(nil), in.Roles...)
		}
		if in.Status != "" {
			target.Status = in.Status
		}
		if in.TenantID != "" {
			target.TenantID = in.TenantID
		}
		if in.Password != "" {
			target.PasswordHash = MustHashPassword(in.Password)
		}
		a.store.Users[email] = target
	}
	if target != nil {
		a.upsertUserPg(target)
	}
	a.store.mu.Unlock()
	if target == nil {
		writeProblem(w, http.StatusNotFound, "user not found", "")
		return
	}
	a.appendAudit("user.updated", "user:"+target.Email, actorOf(r), "update", "")
	writeJSON(w, http.StatusOK, target)
}

func (a *app) handleUserDelete(w http.ResponseWriter, r *http.Request) {
	a.store.mu.Lock()
	var email string
	for e, u := range a.store.Users {
		if u.ID == r.PathValue("id") {
			email = e
			break
		}
	}
	if email != "" {
		delete(a.store.Users, email)
	}
	a.store.mu.Unlock()
	if email == "" {
		writeProblem(w, http.StatusNotFound, "user not found", "")
		return
	}
	a.appendAudit("user.deleted", "user:"+email, actorOf(r), "delete", "")
	w.WriteHeader(http.StatusNoContent)
}

func (a *app) handleRelations(w http.ResponseWriter, r *http.Request) {
	a.store.mu.Lock()
	out := a.store.Relations
	a.store.mu.Unlock()
	writeJSON(w, http.StatusOK, out)
}

func actorOf(r *http.Request) string {
	if c := getClaims(r); c != nil && c.Email != "" {
		return c.Email
	}
	return "system"
}
