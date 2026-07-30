package sdkx

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRetryEventuallySucceeds(t *testing.T) {
	reg := NewActivityRegistry()
	calls := 0
	reg.Register("flaky", func(ctx context.Context, in any) (any, error) {
		calls++
		if calls < 3 {
			return nil, errors.New("transient")
		}
		return "ok", nil
	})
	out, err := ExecuteActivity(context.Background(), reg, DefaultRetryPolicy, "flaky", nil)
	if err != nil || out != "ok" || calls != 3 {
		t.Fatalf("out=%v err=%v calls=%d", out, err, calls)
	}
}

func TestRetryExhaustion(t *testing.T) {
	reg := NewActivityRegistry()
	calls := 0
	reg.Register("always-fails", func(ctx context.Context, in any) (any, error) {
		calls++
		return nil, errors.New("boom")
	})
	p := RetryPolicy{InitialInterval: time.Millisecond, BackoffCoefficient: 1, MaximumAttempts: 3}
	if _, err := ExecuteActivity(context.Background(), reg, p, "always-fails", nil); err == nil || calls != 3 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
	if _, err := ExecuteActivity(context.Background(), reg, p, "missing", nil); err == nil {
		t.Fatal("unregistered activity ran")
	}
}

func TestSagaCompensationOrder(t *testing.T) {
	var compensated []string
	mk := func(name string, fail bool) SagaStep {
		return SagaStep{
			Name: name,
			Action: func(ctx context.Context, in any) (any, error) {
				if fail {
					return nil, errors.New("fail " + name)
				}
				return name + "-out", nil
			},
			Compensation: func(ctx context.Context, in any) error {
				compensated = append(compensated, name)
				return nil
			},
		}
	}
	s := NewSaga(mk("a", false), mk("b", false), mk("c", true))
	err := s.Run(context.Background(), nil)
	if err == nil {
		t.Fatal("saga should fail")
	}
	var se *SagaError
	if !errors.As(err, &se) || se.Step != "c" {
		t.Fatalf("saga error: %v", err)
	}
	if len(compensated) != 2 || compensated[0] != "b" || compensated[1] != "a" {
		t.Fatalf("compensation order: %v", compensated)
	}
}

func TestInprocRunner(t *testing.T) {
	r := NewInprocRunner()
	r.RegisterWorkflow("wf-test", func(ctx context.Context, in any) (any, error) { return "done", nil })
	r.RegisterWorkflow("wf-bad", func(ctx context.Context, in any) (any, error) { return nil, errors.New("x") })
	out, err := r.Execute(context.Background(), "wf-test", nil)
	if err != nil || out != "done" {
		t.Fatal(out, err)
	}
	if _, err := r.Execute(context.Background(), "wf-bad", nil); err == nil {
		t.Fatal("expected failure")
	}
	if _, err := r.Execute(context.Background(), "wf-missing", nil); err == nil {
		t.Fatal("expected unknown workflow error")
	}
	h := r.History()
	if len(h) != 2 || h[0].Status != "completed" || h[1].Status != "failed" {
		t.Fatalf("history: %+v", h)
	}
}
