// workflow-worker — the deployable worker process for the shared wf-*
// primitives (docs/temporal-migration.md, "workflows-go (follow-up)").
//
// Runner selection mirrors the admin-api reference implementation:
//
//	TEMPORAL_URL set   -> real Temporal worker (sdkx.TemporalRunner, started
//	                      by sdkx.NewRunnerFromEnv; task queue
//	                      TEMPORAL_TASK_QUEUE default meridian-core)
//	TEMPORAL_URL unset -> sdkx inproc dev runner (explicit dev default)
//
// Plane clients are plain HTTP calls to env-configured plane endpoints
// (GATE_PLANE_URL, PACK_PLANE_URL). An unconfigured endpoint makes the
// activity return an error, so the run records as FAILED — never a fake
// success. Deploy via the existing helm temporalWorker workload.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/munisp/meridian-core-platform/packages/events/otelx"
	sdkx "github.com/munisp/meridian-core-platform/packages/temporal-sdkx"
	workflows "github.com/munisp/meridian-core-platform/workflows-go"
)

var httpClient = &http.Client{Timeout: 15 * time.Second, Transport: otelx.Client(nil)}

// planePost POSTs payload as JSON to base+path and requires a 2xx. base is
// read from envVar; unset envVar is a hard activity error (honest failure).
func planePost(ctx context.Context, envVar, path string, payload any, out any) error {
	base := os.Getenv(envVar)
	if base == "" {
		return fmt.Errorf("%s unset: plane endpoint for %s not configured", envVar, path)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", envVar, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s %s: status %d: %s", envVar, path, resp.StatusCode, string(b))
	}
	if out != nil && len(b) > 0 {
		if err := json.Unmarshal(b, out); err != nil {
			return fmt.Errorf("%s %s: decode: %w", envVar, path, err)
		}
	}
	return nil
}

// registerWorkflows registers every shared wf-* primitive on r. Names match
// the catalog ids used by admin-api so a run triggered in dev (inproc) and
// in prod (Temporal) addresses the same workflow definition.
func registerWorkflows(r sdkx.Runner) error {
	r.RegisterWorkflow("wf-gate-flip", workflows.WFGateFlip(
		func(ctx context.Context, gateID, state, authRef string) error {
			return planePost(ctx, "GATE_PLANE_URL", "/gates/flip",
				map[string]string{"gate_id": gateID, "state": state, "authorization_ref": authRef}, nil)
		},
		func(ctx context.Context, gateID, state, authRef string) error {
			return planePost(ctx, "GATE_PLANE_URL", "/audit/events",
				map[string]string{"gate_id": gateID, "state": state, "authorization_ref": authRef}, nil)
		},
	))
	r.RegisterWorkflow("wf-pack-rollout", workflows.WFPackRollout(
		func(ctx context.Context, ref string) (any, error) {
			var out any
			err := planePost(ctx, "PACK_PLANE_URL", "/packs/fetch", map[string]string{"ref": ref}, &out)
			return out, err
		},
		func(ctx context.Context, ref string) ([]string, error) {
			var out struct {
				Consumers []string `json:"consumers"`
			}
			err := planePost(ctx, "PACK_PLANE_URL", "/packs/distribute", map[string]string{"ref": ref}, &out)
			return out.Consumers, err
		},
		func(ctx context.Context, ref string) error {
			return planePost(ctx, "PACK_PLANE_URL", "/packs/repin", map[string]string{"ref": ref}, nil)
		},
	))
	// wf-noop is the honest stand-in for catalog defs without a plane
	// implementation yet (same convention as admin-api).
	noop, err := workflows.Compose("wf-noop",
		[]workflows.StepSpec{{Name: "execute", Activity: "wf-activity.noop"}},
		sdkx.DefaultRetryPolicy)
	if err != nil {
		return fmt.Errorf("compose wf-noop: %w", err)
	}
	r.RegisterWorkflow("wf-noop", func(ctx context.Context, input any) (any, error) {
		if err := noop.Run(ctx, input); err != nil {
			return nil, err
		}
		return map[string]any{"workflow": "wf-noop", "activities": []string{"wf-activity.noop"}}, nil
	})
	return nil
}

func main() {
	// OTel bootstrap (DESIGN-CONTRACT): fail-soft — no OTLP endpoint means
	// no-op providers; PROFILE=prod without one logs a loud warning.
	otelProv := otelx.InitProvidersFor(context.Background(), "workflow-worker", "")
	defer otelProv.Shutdown(context.Background())

	runner := sdkx.NewRunnerFromEnv()
	if err := registerWorkflows(runner); err != nil {
		log.Fatalf("component=workflow-worker FATAL: %v", err)
	}
	mode := "dev-inproc"
	if tr, ok := runner.(*sdkx.TemporalRunner); ok {
		mode = "temporal"
		defer tr.Stop()
	}
	log.Printf("component=workflow-worker workflows=%v mode=%s", runner.WorkflowNames(), mode)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	s := <-sig
	log.Printf("component=workflow-worker shutdown signal=%v", s)
}
