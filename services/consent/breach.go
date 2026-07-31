// breach.go — C2: NDPA s.40 personal-data-breach registry.
//
// Records breaches with affected subjects, severity and detection time, and
// derives the 72-hour NDPC notification deadline (NDPA s.40: notify the
// Commission within 72 hours of becoming aware). Registering a breach emits
// the alert event nrs.privacy.breach.v1 on the event bus (and persists a
// copy in the breach_alerts collection so the alert is auditable without a
// bus). Status workflow is strictly detected -> assessed -> notified ->
// closed. All endpoints are role-gated to privacy:officer or admin.
package main

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/munisp/meridian-core-platform/packages/events/auth"
	"github.com/munisp/meridian-core-platform/packages/events/envelope"
	"github.com/munisp/meridian-core-platform/packages/events/httpx"
)

// BreachEventType is the alert topic emitted when a breach is registered.
const BreachEventType = "nrs.privacy.breach.v1"

// ndpcNotifyWindow is the NDPA s.40 notification deadline.
const ndpcNotifyWindow = 72 * time.Hour

var breachSeverities = map[string]bool{
	"low": true, "medium": true, "high": true, "critical": true,
}

// Breach is one NDPA s.40 breach-registry record.
type Breach struct {
	ID               string             `json:"id"`
	Title            string             `json:"title"`
	Description      string             `json:"description,omitempty"`
	AffectedSubjects []string           `json:"affected_subjects"` // pseudonymised ids
	Severity         string             `json:"severity"`          // low|medium|high|critical
	DetectedAt       string             `json:"detected_at"`
	NotifyDeadline   string             `json:"notify_deadline"` // DetectedAt + 72h
	Status           string             `json:"status"`          // detected|assessed|notified|closed
	NotifiedAt       string             `json:"notified_at,omitempty"`
	LateNotification bool               `json:"late_notification,omitempty"`
	History          []BreachTransition `json:"history"`
	CreatedBy        string             `json:"created_by"`
	CreatedAt        string             `json:"created_at"`
	UpdatedAt        string             `json:"updated_at"`
}

// BreachTransition is one step in the status workflow audit trail.
type BreachTransition struct {
	From string `json:"from"`
	To   string `json:"to"`
	At   string `json:"at"`
	By   string `json:"by"`
}

// breachWorkflow encodes the only legal transitions.
var breachWorkflow = map[string]string{
	"detected": "assessed",
	"assessed": "notified",
	"notified": "closed",
}

// requireAnyRole gates a handler to any of the given roles (RFC7807 403).
func requireAnyRole(next http.HandlerFunc, roles ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, ok := auth.FromContext(r.Context())
		if !ok {
			httpx.Errorf(w, http.StatusUnauthorized, "unauthorized", "authentication required")
			return
		}
		for _, role := range roles {
			if c.HasRole(role) {
				next(w, r)
				return
			}
		}
		httpx.Errorf(w, http.StatusForbidden, "forbidden", "one of roles %v required", roles)
	}
}

type breachCreateReq struct {
	Title            string   `json:"title"`
	Description      string   `json:"description,omitempty"`
	AffectedSubjects []string `json:"affected_subjects"`
	Severity         string   `json:"severity"`
	DetectedAt       string   `json:"detected_at"` // RFC3339; defaults to now
}

func (s *server) breachCreate(w http.ResponseWriter, r *http.Request) {
	var req breachCreateReq
	if err := httpx.Decode(r, &req); err != nil {
		httpx.BadRequest(w, "invalid JSON: %v", err)
		return
	}
	if req.Title == "" || len(req.AffectedSubjects) == 0 {
		httpx.BadRequest(w, "title and affected_subjects are required")
		return
	}
	if !breachSeverities[req.Severity] {
		httpx.BadRequest(w, "severity must be one of low|medium|high|critical")
		return
	}
	detected := time.Now().UTC()
	if req.DetectedAt != "" {
		t, err := time.Parse(time.RFC3339, req.DetectedAt)
		if err != nil {
			httpx.BadRequest(w, "detected_at must be RFC3339: %v", err)
			return
		}
		detected = t.UTC()
	}
	claims, _ := auth.FromContext(r.Context())
	now := time.Now().UTC().Format(time.RFC3339)
	b := Breach{
		ID:               "brc-" + envelope.NewULID(),
		Title:            req.Title,
		Description:      req.Description,
		AffectedSubjects: req.AffectedSubjects,
		Severity:         req.Severity,
		DetectedAt:       detected.Format(time.RFC3339),
		NotifyDeadline:   detected.Add(ndpcNotifyWindow).Format(time.RFC3339),
		Status:           "detected",
		History: []BreachTransition{
			{From: "", To: "detected", At: now, By: claims.Sub},
		},
		CreatedBy: claims.Sub,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.st.Put("breaches", b.ID, b); err != nil {
		httpx.Internal(w, "%v", err)
		return
	}
	s.emitBreachAlert(r.Context(), b, claims.TenantID)
	httpx.JSON(w, http.StatusCreated, map[string]any{"breach": b})
}

// emitBreachAlert publishes nrs.privacy.breach.v1 and persists an auditable
// copy. Bus failure never blocks the registry write (the alert copy is the
// durable record); it is logged loudly instead.
func (s *server) emitBreachAlert(ctx context.Context, b Breach, tenantID string) {
	payload := map[string]any{
		"breach_id":         b.ID,
		"severity":          b.Severity,
		"affected_subjects": b.AffectedSubjects,
		"detected_at":       b.DetectedAt,
		"notify_deadline":   b.NotifyDeadline,
	}
	alertID := "alert-" + envelope.NewULID()
	if err := s.st.Put("breach_alerts", alertID, map[string]any{
		"id": alertID, "event": BreachEventType, "data": payload, "at": time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		log.Printf("breach alert persist: %v", err)
	}
	if s.eventBus == nil {
		return
	}
	env, err := envelope.New(BreachEventType, service, tenantID, "", payload)
	if err != nil {
		log.Printf("breach alert envelope: %v", err)
		return
	}
	if err := s.eventBus.Publish(ctx, BreachEventType, env); err != nil {
		log.Printf("breach alert publish (durable copy retained): %v", err)
	}
}

func (s *server) breachList(w http.ResponseWriter, r *http.Request) {
	var all []Breach
	if err := s.st.ListInto("breaches", &all); err != nil {
		httpx.Internal(w, "%v", err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"breaches": all, "count": len(all)})
}

func (s *server) breachGet(w http.ResponseWriter, r *http.Request) {
	var b Breach
	if err := s.st.Get("breaches", r.PathValue("id"), &b); err != nil {
		httpx.NotFound(w, "breach %s", r.PathValue("id"))
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"breach": b})
}

// breachTransition advances the status workflow. The transition to
// "notified" records the NDPC notification time and flags late notification
// against the 72h deadline.
func (s *server) breachTransition(w http.ResponseWriter, r *http.Request) {
	var req struct {
		To string `json:"to"`
	}
	if err := httpx.Decode(r, &req); err != nil {
		httpx.BadRequest(w, "invalid JSON: %v", err)
		return
	}
	id := r.PathValue("id")
	var b Breach
	if err := s.st.Get("breaches", id, &b); err != nil {
		httpx.NotFound(w, "breach %s", id)
		return
	}
	next, ok := breachWorkflow[b.Status]
	if !ok {
		httpx.Conflict(w, "breach %s is closed; no further transitions", id)
		return
	}
	if req.To != next {
		httpx.Conflict(w, "invalid transition %s -> %s; expected %s", b.Status, req.To, next)
		return
	}
	claims, _ := auth.FromContext(r.Context())
	now := time.Now().UTC()
	if req.To == "notified" {
		b.NotifiedAt = now.Format(time.RFC3339)
		deadline, _ := time.Parse(time.RFC3339, b.NotifyDeadline)
		b.LateNotification = now.After(deadline)
	}
	b.History = append(b.History, BreachTransition{From: b.Status, To: req.To, At: now.Format(time.RFC3339), By: claims.Sub})
	b.Status = req.To
	b.UpdatedAt = now.Format(time.RFC3339)
	if err := s.st.Put("breaches", id, b); err != nil {
		httpx.Internal(w, "%v", err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"breach": b})
}
