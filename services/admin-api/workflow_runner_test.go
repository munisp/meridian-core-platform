package main

// workflow_runner_test.go — reference-implementation tests for the Temporal
// migration (docs/temporal-migration.md): triggers execute through the
// env-selected runner and record the real outcome.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/munisp/meridian-core-platform/packages/temporal-sdkx"
)

var errTestBoom = errors.New("boom")

func withOperatorClaims(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), ctxClaims,
		&claims{Sub: "u1", Email: "op@meridian.local", Roles: []string{"operator"}}))
}

func TestWorkflowTriggerExecutesInprocRunner(t *testing.T) {
	t.Setenv("TEMPORAL_URL", "") // dev default: inproc runner
	a := &app{store: NewStore(), authMode: "dev"}
	a.initWorkflows()

	req := httptest.NewRequest(http.MethodPost, "/v1/admin/workflows/wf-etr-compute/trigger",
		strings.NewReader(`{"input":"{}"}`))
	req.SetPathValue("id", "wf-etr-compute")
	req = withOperatorClaims(req)
	rec := httptest.NewRecorder()
	a.handleWorkflowTrigger(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (%s)", rec.Code, rec.Body.String())
	}
	a.store.mu.Lock()
	defer a.store.mu.Unlock()
	run := a.store.WorkflowRuns[0] // newest first; store seeds demo runs
	if run.WorkflowID != "wf-etr-compute" {
		t.Fatalf("newest run = %q, want wf-etr-compute", run.WorkflowID)
	}
	if run.Status != "completed" {
		t.Fatalf("run status = %q, want completed", run.Status)
	}
	if run.FinishedAt == "" {
		t.Fatal("finished_at must be set after real execution")
	}
	// the inproc runner must have a run history entry — proof of execution
	if len(a.wfRunner.History()) != 1 {
		t.Fatalf("runner history = %d, want 1", len(a.wfRunner.History()))
	}
}

func TestWorkflowTriggerUnknownDefRejected(t *testing.T) {
	t.Setenv("TEMPORAL_URL", "")
	a := &app{store: NewStore(), authMode: "dev"}
	a.initWorkflows()

	req := httptest.NewRequest(http.MethodPost, "/v1/admin/workflows/wf-nope/trigger",
		strings.NewReader(`{}`))
	req.SetPathValue("id", "wf-nope")
	rec := httptest.NewRecorder()
	a.handleWorkflowTrigger(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestWorkflowTriggerFailureSurfacesFailed(t *testing.T) {
	t.Setenv("TEMPORAL_URL", "")
	a := &app{store: NewStore(), authMode: "dev"}
	a.initWorkflows()
	// Replace the registered workflow with a failing one; the run row must
	// record "failed" and the API must answer 422 (no fake completed runs).
	a.wfExec["wf-etr-compute"] = sdkx.Workflow(func(ctx context.Context, in any) (any, error) {
		return nil, errTestBoom
	})
	a.wfRunner.RegisterWorkflow("wf-etr-compute", a.wfExec["wf-etr-compute"])

	req := httptest.NewRequest(http.MethodPost, "/v1/admin/workflows/wf-etr-compute/trigger",
		strings.NewReader(`{}`))
	req.SetPathValue("id", "wf-etr-compute")
	req = withOperatorClaims(req)
	rec := httptest.NewRecorder()
	a.handleWorkflowTrigger(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (%s)", rec.Code, rec.Body.String())
	}
	a.store.mu.Lock()
	defer a.store.mu.Unlock()
	if a.store.WorkflowRuns[0].Status != "failed" {
		t.Fatalf("run status = %q, want failed", a.store.WorkflowRuns[0].Status)
	}
}

func TestWorkflowModeHonestDefault(t *testing.T) {
	t.Setenv("TEMPORAL_URL", "")
	a := &app{store: NewStore(), authMode: "dev"}
	a.initWorkflows()
	if a.workflowMode() != "dev-inproc" {
		t.Fatalf("mode = %q, want dev-inproc", a.workflowMode())
	}
}
