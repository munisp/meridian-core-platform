package main

// R4-9c: step-up tickets on ledger money routes (defense in depth behind
// admin-api). The ledger does not hold TOTP secrets; it verifies the
// short-lived single-use step-up tickets minted by admin-api's
// /v1/stepup/challenge with the shared STEPUP_TICKET_KEY. Tickets bind
// actor + action + payload SHA-256; replay is rejected (401).
// Fail-closed: PROFILE=prod without a valid ticket -> 403 step_up_required.
// Dev profile allows the bypass with a loud log so local flows keep working.

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/munisp/meridian-core-platform/packages/events/auth"
	"github.com/munisp/meridian-core-platform/packages/events/httpx"
	"github.com/munisp/meridian-core-platform/packages/stepup"
)

type stepupGate struct {
	store     *stepup.MemStore
	ticketKey []byte
	profile   string
}

func newStepupGate() (*stepupGate, error) {
	profile := httpx.Env("PROFILE", "dev")
	key, err := stepup.TicketKey(profile, os.Getenv("MERIDIAN_DEV_JWT_SECRET"))
	if err != nil {
		return nil, err
	}
	return &stepupGate{store: stepup.NewMemStore(), ticketKey: key, profile: profile}, nil
}

// requireStepUp gates a money route behind a step-up ticket bound to
// actor + action + request-body hash.
func (g *stepupGate) requireStepUp(action string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, _ := auth.FromContext(r.Context())
		var body []byte
		if r.Body != nil {
			body, _ = io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		tok := r.Header.Get("X-Stepup-Ticket")
		if tok == "" {
			if g.profile == "prod" {
				httpx.Errorf(w, http.StatusForbidden, "step_up_required",
					"money route requires X-Stepup-Ticket from admin-api /v1/stepup/challenge")
				return
			}
			log.Printf("component=ledger WARN step-up BYPASSED (dev profile) actor=%s action=%s path=%s",
				claims.Sub, action, r.URL.Path)
			h(w, r)
			return
		}
		_, err := stepup.VerifyTicket(g.ticketKey, g.store, tok, claims.Sub, action,
			stepup.HashPayload(body), time.Now())
		if err != nil {
			kind := "step_up_invalid"
			if err == stepup.ErrReplay {
				kind = "step_up_ticket_replay"
			}
			httpx.Errorf(w, http.StatusUnauthorized, kind, "%s", err.Error())
			return
		}
		h(w, r)
	}
}
