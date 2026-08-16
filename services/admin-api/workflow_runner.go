// workflow_runner.go — Temporal real-worker wiring for admin workflow
// triggers (reference implementation for docs/temporal-migration.md).
//
// Before this change handleWorkflowTrigger only recorded a WorkflowRun row
// marked "completed" — no workflow executed. Now every triggerable catalog
// def is registered as an executable sdkx workflow and run through the
// env-selected runner:
//
//	TEMPORAL_URL set   -> real Temporal worker (sdkx.TemporalRunner; worker
//	                      started by sdkx.NewRunnerFromEnv, task queue
//	                      TEMPORAL_TASK_QUEUE default meridian-core)
//	TEMPORAL_URL unset -> sdkx inproc dev runner (dev default, unchanged)
//
// Plane-specific activities register in workflows.Registry as planes adopt
// the worker; until then each triggerable def composes a single-step saga
// over wf-activity.noop so the run record reflects what actually executed.
package main

import (
	"context"
	"log"

	"github.com/munisp/meridian-core-platform/packages/temporal-sdkx"
	workflows "github.com/munisp/meridian-core-platform/workflows-go"
)

// initWorkflows selects the runner and registers one executable workflow per
// triggerable catalog def. Called from main after the store is seeded.
func (a *app) initWorkflows() {
	a.wfRunner = sdkx.NewRunnerFromEnv()
	a.wfExec = map[string]sdkx.Workflow{}
	a.store.mu.Lock()
	defs := make([]WorkflowDef, len(a.store.WorkflowDefs))
	copy(defs, a.store.WorkflowDefs)
	a.store.mu.Unlock()
	for _, def := range defs {
		if !def.Triggerable {
			continue
		}
		id := def.ID
		saga, err := workflows.Compose(id,
			[]workflows.StepSpec{{Name: "execute", Activity: "wf-activity.noop"}},
			sdkx.DefaultRetryPolicy)
		if err != nil {
			log.Printf("component=admin-api workflow %s not registered: %v", id, err)
			continue
		}
		wf := sdkx.Workflow(func(ctx context.Context, input any) (any, error) {
			if err := saga.Run(ctx, input); err != nil {
				return nil, err
			}
			return map[string]any{"workflow": id, "activities": []string{"wf-activity.noop"}}, nil
		})
		a.wfExec[id] = wf
		a.wfRunner.RegisterWorkflow(id, wf)
	}
	mode := "dev-inproc"
	if _, ok := a.wfRunner.(*sdkx.TemporalRunner); ok {
		mode = "temporal"
	}
	log.Printf("component=admin-api workflows registered=%d mode=%s", len(a.wfExec), mode)
}

// workflowMode reports which runner executes triggers (honest reporting in
// API responses instead of the previous hardcoded "dev-inproc" note).
func (a *app) workflowMode() string {
	if _, ok := a.wfRunner.(*sdkx.TemporalRunner); ok {
		return "temporal"
	}
	return "dev-inproc"
}
