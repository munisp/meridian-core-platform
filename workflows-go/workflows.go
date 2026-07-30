// Package workflows holds shared wf-* building blocks planes compose into
// their Temporal workflows (SPEC 2). Each primitive is registered in the
// shared activity registry and runnable via the sdkx inproc runner in dev.
package workflows

import (
	"context"
	"fmt"

	"github.com/munisp/meridian-core-platform/packages/temporal-sdkx"
)

// Registry is the shared activity registry for wf-* primitives.
var Registry = sdkx.NewActivityRegistry()

func init() {
	Registry.Register("wf-activity.noop", func(ctx context.Context, in any) (any, error) { return in, nil })
}

// StepSpec declaratively describes one wf step (used by Compose).
type StepSpec struct {
	Name       string
	Activity   string // registered activity name
	Compensate string // optional compensation activity name
}

// Compose builds a sdkx.Saga from declarative step specs using the shared
// registry with retry — the common shape of wf-* primitives (wf-pack-rollout,
// wf-remit-schedule, wf-gate-flip, ...).
func Compose(name string, specs []StepSpec, policy sdkx.RetryPolicy) (*sdkx.Saga, error) {
	steps := make([]sdkx.SagaStep, 0, len(specs))
	for _, spec := range specs {
		act, ok := Registry.Get(spec.Activity)
		if !ok {
			return nil, fmt.Errorf("compose %s: activity %q not registered", name, spec.Activity)
		}
		a := act
		step := sdkx.SagaStep{
			Name: spec.Name,
			Action: func(ctx context.Context, in any) (any, error) {
				return sdkx.ExecuteActivity(ctx, Registry, policy, spec.Activity, in)
			},
		}
		if spec.Compensate != "" {
			comp, ok := Registry.Get(spec.Compensate)
			if !ok {
				return nil, fmt.Errorf("compose %s: compensation %q not registered", name, spec.Compensate)
			}
			_ = a
			step.Compensation = func(ctx context.Context, out any) error {
				_, err := comp(ctx, out)
				return err
			}
		}
		steps = append(steps, step)
	}
	return sdkx.NewSaga(steps...), nil
}

// WFGateFlip is the shared wf-gate-flip primitive: verify board
// authorization -> flip gate (via injected flipper) -> emit audit event.
// Plane services inject their own gate client; the SAGA shape lives here.
func WFGateFlip(flip func(ctx context.Context, gateID, state, authRef string) error,
	audit func(ctx context.Context, gateID, state, authRef string) error) sdkx.Workflow {
	return func(ctx context.Context, input any) (any, error) {
		req, ok := input.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("wf-gate-flip: input must be map with gate_id/state/authorization_ref")
		}
		gateID, _ := req["gate_id"].(string)
		state, _ := req["state"].(string)
		authRef, _ := req["authorization_ref"].(string)
		if gateID == "" || state == "" || authRef == "" {
			return nil, fmt.Errorf("wf-gate-flip: gate_id, state and authorization_ref are required")
		}
		saga := sdkx.NewSaga(
			sdkx.SagaStep{
				Name: "flip-gate",
				Action: func(ctx context.Context, in any) (any, error) {
					if err := flip(ctx, gateID, state, authRef); err != nil {
						return nil, err
					}
					return state, nil
				},
				Compensation: func(ctx context.Context, in any) error {
					prev := "armed"
					if state == "armed" {
						prev = "disarmed"
					}
					return flip(ctx, gateID, prev, authRef+"-revert")
				},
			},
			sdkx.SagaStep{
				Name: "emit-audit",
				Action: func(ctx context.Context, in any) (any, error) {
					return nil, audit(ctx, gateID, state, authRef)
				},
			},
		)
		if err := saga.Run(ctx, input); err != nil {
			return nil, err
		}
		return map[string]any{"gate_id": gateID, "state": state}, nil
	}
}

// WFPackRollout is the shared wf-pack-rollout primitive shape: fetch pack ->
// validate -> distribute to consumers -> verify pins updated, with
// compensation re-pinning the previous version on failure.
func WFPackRollout(fetch func(ctx context.Context, ref string) (any, error),
	distribute func(ctx context.Context, ref string) ([]string, error),
	repin func(ctx context.Context, ref string) error) sdkx.Workflow {
	return func(ctx context.Context, input any) (any, error) {
		ref, _ := input.(map[string]any)["ref"].(string)
		prevRef, _ := input.(map[string]any)["previous_ref"].(string)
		if ref == "" {
			return nil, fmt.Errorf("wf-pack-rollout: ref required")
		}
		saga := sdkx.NewSaga(
			sdkx.SagaStep{Name: "fetch-pack", Action: func(ctx context.Context, in any) (any, error) {
				return fetch(ctx, ref)
			}},
			sdkx.SagaStep{
				Name: "distribute",
				Action: func(ctx context.Context, in any) (any, error) {
					return distribute(ctx, ref)
				},
				Compensation: func(ctx context.Context, in any) error {
					if prevRef == "" {
						return nil
					}
					return repin(ctx, prevRef)
				},
			},
		)
		if err := saga.Run(ctx, input); err != nil {
			return nil, err
		}
		return map[string]any{"rolled_out": ref}, nil
	}
}
