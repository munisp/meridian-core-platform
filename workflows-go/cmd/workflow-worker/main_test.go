package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	sdkx "github.com/munisp/meridian-core-platform/packages/temporal-sdkx"
)

// Registration must cover all shared wf-* primitives under the catalog ids
// (same names for inproc and Temporal runners).
func TestRegisterWorkflowsNames(t *testing.T) {
	r := sdkx.NewInprocRunner()
	if err := registerWorkflows(r); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"wf-gate-flip": true, "wf-pack-rollout": true, "wf-noop": true}
	for _, n := range r.WorkflowNames() {
		delete(want, n)
	}
	if len(want) != 0 {
		t.Fatalf("missing workflows: %v", want)
	}
}

// wf-noop executes successfully through the inproc dev runner.
func TestNoopCompletesInproc(t *testing.T) {
	r := sdkx.NewInprocRunner()
	if err := registerWorkflows(r); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Execute(context.Background(), "wf-noop", nil); err != nil {
		t.Fatal(err)
	}
	h := r.History()
	if len(h) != 1 || h[0].Status != "completed" {
		t.Fatalf("history %+v", h)
	}
}

// With no plane endpoint configured the run must record FAILED, never a
// fake success.
func TestGateFlipFailsHonestlyWhenPlaneUnset(t *testing.T) {
	t.Setenv("GATE_PLANE_URL", "")
	r := sdkx.NewInprocRunner()
	if err := registerWorkflows(r); err != nil {
		t.Fatal(err)
	}
	_, err := r.Execute(context.Background(), "wf-gate-flip", map[string]any{
		"gate_id": "g1", "state": "disarmed", "authorization_ref": "board-1"})
	if err == nil {
		t.Fatal("expected failure with GATE_PLANE_URL unset")
	}
	h := r.History()
	if len(h) != 1 || h[0].Status != "failed" {
		t.Fatalf("history %+v", h)
	}
}

// With a plane endpoint configured, wf-gate-flip calls flip + audit over
// HTTP and completes.
func TestGateFlipAgainstStubPlane(t *testing.T) {
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"ok": "true"})
	}))
	defer srv.Close()
	t.Setenv("GATE_PLANE_URL", srv.URL)
	r := sdkx.NewInprocRunner()
	if err := registerWorkflows(r); err != nil {
		t.Fatal(err)
	}
	out, err := r.Execute(context.Background(), "wf-gate-flip", map[string]any{
		"gate_id": "g1", "state": "disarmed", "authorization_ref": "board-1"})
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["state"] != "disarmed" {
		t.Fatalf("out %v", out)
	}
	if len(calls) != 2 || calls[0] != "/gates/flip" || calls[1] != "/audit/events" {
		t.Fatalf("plane calls %v", calls)
	}
}
