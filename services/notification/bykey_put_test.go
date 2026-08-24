package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/munisp/meridian-core-platform/packages/events/store"
)

// Regression (V2 round, B4-14 residual): notify's post-send by_key Put
// error was swallowed (`_ =`), so a retry of the same idempotency key
// would double-send. The handler must now log and surface the failure.

// failByKeyStore fails Put on the by_key collection only.
type failByKeyStore struct {
	store.DocStore
}

func (f failByKeyStore) Put(coll, id string, v any) error {
	if coll == "by_key" {
		return errors.New("disk full")
	}
	return f.DocStore.Put(coll, id, v)
}

func TestNotifyByKeyPutFailureSurfaced(t *testing.T) {
	base, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := &server{st: failByKeyStore{base}, orch: newOrchestrator(nil)}
	body := `{"to":"+2348000000000","template_id":"t1","idempotency_key":"k-fail","chain":["sms"]}`
	req := httptest.NewRequest("POST", "/v1/notify", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.notify(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("by_key Put failure = %d, want 500 (surfaced, not swallowed); body %s", rec.Code, rec.Body.String())
	}
}

func TestNotifyByKeyPutSuccessStillAccepted(t *testing.T) {
	base, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := &server{st: base, orch: newOrchestrator(nil)}
	body := `{"to":"+2348000000000","template_id":"t1","idempotency_key":"k-ok","chain":["sms"]}`
	req := httptest.NewRequest("POST", "/v1/notify", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.notify(rec, req)
	if rec.Code != http.StatusAccepted && rec.Code != http.StatusBadGateway {
		t.Fatalf("normal notify = %d, want 202/502; body %s", rec.Code, rec.Body.String())
	}
	// by_key record persisted: replay returns the original message.
	var out struct {
		Message Message `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	var keyed Message
	if err := base.Get("by_key", "notify:k-ok", &keyed); err != nil {
		t.Fatalf("by_key record missing: %v", err)
	}
}
