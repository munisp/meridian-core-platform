package workflows

import (
	"context"
	"errors"
	"testing"

	"github.com/munisp/meridian-core-platform/packages/temporal-sdkx"
)

func TestWFGateFlipSuccess(t *testing.T) {
	flips := []string{}
	flip := func(ctx context.Context, gateID, state, ref string) error {
		flips = append(flips, gateID+":"+state)
		return nil
	}
	audit := func(ctx context.Context, gateID, state, ref string) error { return nil }
	r := sdkx.NewInprocRunner()
	r.RegisterWorkflow("wf-gate-flip", WFGateFlip(flip, audit))
	out, err := r.Execute(context.Background(), "wf-gate-flip",
		map[string]any{"gate_id": "g8_presumptive_reg", "state": "disarmed", "authorization_ref": "BM-1"})
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["state"] != "disarmed" || len(flips) != 1 {
		t.Fatalf("%v %v", out, flips)
	}
}

func TestWFGateFlipCompensatesOnAuditFailure(t *testing.T) {
	flips := []string{}
	flip := func(ctx context.Context, gateID, state, ref string) error {
		flips = append(flips, state)
		return nil
	}
	audit := func(ctx context.Context, gateID, state, ref string) error { return errors.New("audit down") }
	wf := WFGateFlip(flip, audit)
	_, err := wf(context.Background(), map[string]any{
		"gate_id": "g1", "state": "disarmed", "authorization_ref": "BM-2"})
	if err == nil {
		t.Fatal("expected saga failure")
	}
	if len(flips) != 2 || flips[1] != "armed" { // compensation re-arms
		t.Fatalf("compensation flips: %v", flips)
	}
}

func TestComposeAndPackRollout(t *testing.T) {
	Registry.Register("wf-test.act", func(ctx context.Context, in any) (any, error) { return "x", nil })
	saga, err := Compose("wf-test", []StepSpec{{Name: "s1", Activity: "wf-test.act"}}, sdkx.DefaultRetryPolicy)
	if err != nil {
		t.Fatal(err)
	}
	if err := saga.Run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := Compose("wf-bad", []StepSpec{{Name: "s", Activity: "missing"}}, sdkx.DefaultRetryPolicy); err == nil {
		t.Fatal("compose with missing activity should fail")
	}
	distributed := []string{}
	wf := WFPackRollout(
		func(ctx context.Context, ref string) (any, error) { return map[string]any{"ref": ref}, nil },
		func(ctx context.Context, ref string) ([]string, error) {
			distributed = append(distributed, ref)
			return []string{"wht"}, nil
		},
		func(ctx context.Context, ref string) error { return nil },
	)
	out, err := wf(context.Background(), map[string]any{"ref": "rp-wht-2024@1.0.0"})
	if err != nil || out.(map[string]any)["rolled_out"] != "rp-wht-2024@1.0.0" || len(distributed) != 1 {
		t.Fatalf("%v %v %v", out, err, distributed)
	}
}
