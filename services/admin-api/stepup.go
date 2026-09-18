package main

// R4-9c: TOTP (RFC 6238) step-up authentication for admin money paths.
// Bearer token + role alone no longer moves money: designated routes also
// require X-Stepup-Code (live TOTP or single-use recovery code) or a
// short-lived single-use X-Stepup-Ticket obtained from the challenge
// endpoint. Fail-closed in prod: an admin without an enrolled TOTP is
// denied on money routes (403 step_up_required). In dev the bypass is
// allowed but logged loudly.

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/munisp/meridian-core-platform/packages/stepup"
)

// stepupState bundles the enrollment service and ticket key.
type stepupState struct {
	svc       *stepup.Service
	store     *stepup.MemStore
	ticketKey []byte
	profile   string
}

func (a *app) initStepup() error {
	profile := os.Getenv("PROFILE")
	if profile == "" {
		profile = "dev"
	}
	store := stepup.NewMemStore()
	svc, err := stepup.NewService(store, profile, "MeridianAdmin")
	if err != nil {
		return err
	}
	if svc.Sealer.UsingDevKey() {
		log.Printf("component=admin-api WARN: STEPUP_SEAL_KEY unset; using well-known dev seal key (dev profile only)")
	}
	key, err := stepup.TicketKey(profile, a.jwtSecret)
	if err != nil {
		return err
	}
	a.stepup = &stepupState{svc: svc, store: store, ticketKey: key, profile: profile}
	return nil
}

// --- enrollment endpoints ---

// handleStepupEnroll starts TOTP enrollment for the calling admin. Returns
// the otpauth:// URI and one-time recovery codes (shown once).
func (a *app) handleStepupEnroll(w http.ResponseWriter, r *http.Request) {
	c := getClaims(r)
	if c == nil || !hasRole(c, "admin") {
		writeProblem(w, http.StatusForbidden, "forbidden", "requires role admin")
		return
	}
	_, uri, recovery, err := a.stepup.svc.Enroll(c.Sub)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "enroll failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"actor":          c.Sub,
		"otpauth_uri":    uri,
		"recovery_codes": recovery,
		"status":         "pending_confirm",
	})
}

// handleStepupConfirm activates enrollment after the first valid TOTP.
func (a *app) handleStepupConfirm(w http.ResponseWriter, r *http.Request) {
	c := getClaims(r)
	var req struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, http.StatusBadRequest, "bad request", "code required")
		return
	}
	if err := a.stepup.svc.Confirm(c.Sub, req.Code); err != nil {
		status := http.StatusUnauthorized
		if err == stepup.ErrNotEnrolled {
			status = http.StatusConflict
		}
		writeProblem(w, status, "confirm failed", err.Error())
		return
	}
	log.Printf("component=admin-api stepup enrollment CONFIRMED actor=%s", c.Sub)
	writeJSON(w, http.StatusOK, map[string]string{"status": "enabled"})
}

// handleStepupChallenge trades a valid TOTP (or recovery code) for a
// short-lived single-use ticket bound to actor + action + payload hash.
func (a *app) handleStepupChallenge(w http.ResponseWriter, r *http.Request) {
	c := getClaims(r)
	var req struct {
		Code    string          `json:"code"`
		Action  string          `json:"action"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Action == "" {
		writeProblem(w, http.StatusBadRequest, "bad request", "code and action required")
		return
	}
	if err := a.stepup.svc.VerifyCode(c.Sub, req.Code); err != nil {
		writeProblem(w, http.StatusUnauthorized, "step-up failed", "invalid TOTP or recovery code")
		return
	}
	tok, err := stepup.MintTicket(a.stepup.ticketKey, c.Sub, req.Action,
		stepup.HashPayload(req.Payload), stepupNow())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "ticket failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ticket":     tok,
		"expires_in": int(stepup.TicketTTL.Seconds()),
	})
}

// handleStepupDisable removes enrollment; requires step-up itself
// (X-Stepup-Code with a valid TOTP or recovery code).
func (a *app) handleStepupDisable(w http.ResponseWriter, r *http.Request) {
	c := getClaims(r)
	code := r.Header.Get("X-Stepup-Code")
	if err := a.stepup.svc.VerifyCode(c.Sub, code); err != nil {
		writeProblem(w, http.StatusUnauthorized, "step-up failed", "disable requires a valid TOTP or recovery code")
		return
	}
	if err := a.stepup.svc.Disable(c.Sub); err != nil {
		writeProblem(w, http.StatusInternalServerError, "disable failed", err.Error())
		return
	}
	log.Printf("component=admin-api stepup enrollment DISABLED actor=%s", c.Sub)
	writeJSON(w, http.StatusOK, map[string]string{"status": "disabled"})
}

// --- verification middleware ---

// requireStepUp gates a money route. action is the ticket binding string.
// Policy:
//   - enrolled actor: X-Stepup-Code (TOTP/recovery) or X-Stepup-Ticket
//     (actor+action+payload-hash bound, single-use) required, all profiles
//   - unenrolled actor: PROFILE=prod -> 403 step_up_required (fail-closed);
//     dev -> loud-log bypass
func (a *app) requireStepUp(action string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c := getClaims(r)
		if c == nil {
			writeProblem(w, http.StatusUnauthorized, "unauthorized", "missing claims")
			return
		}
		// read & restore the body for payload-hash binding
		var body []byte
		if r.Body != nil {
			body, _ = io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		ph := stepup.HashPayload(body)

		if code := r.Header.Get("X-Stepup-Code"); code != "" {
			if err := a.stepup.svc.VerifyCode(c.Sub, code); err != nil {
				writeProblem(w, http.StatusUnauthorized, "step_up_invalid",
					"invalid or expired TOTP/recovery code")
				return
			}
			h(w, r)
			return
		}
		if tok := r.Header.Get("X-Stepup-Ticket"); tok != "" {
			_, err := stepup.VerifyTicket(a.stepup.ticketKey, a.stepup.store, tok, c.Sub, action, ph, stepupNow())
			if err != nil {
				kind := "step_up_invalid"
				if err == stepup.ErrReplay {
					kind = "step_up_ticket_replay"
				}
				writeProblem(w, http.StatusUnauthorized, kind, err.Error())
				return
			}
			h(w, r)
			return
		}
		// no step-up credential presented
		if a.stepup.svc.Enrolled(c.Sub) {
			writeProblem(w, http.StatusForbidden, "step_up_required",
				"this money route requires X-Stepup-Code (TOTP) or X-Stepup-Ticket")
			return
		}
		if a.stepup.profile == "prod" {
			writeProblem(w, http.StatusForbidden, "step_up_required",
				"admin has no enrolled TOTP; enroll at /v1/stepup/enroll (fail-closed in prod)")
			return
		}
		log.Printf("component=admin-api WARN step-up BYPASSED (dev profile, unenrolled actor) actor=%s action=%s path=%s",
			c.Sub, action, r.URL.Path)
		h(w, r)
	}
}

// stepupNow is a seam for tests.
var stepupNow = time.Now
