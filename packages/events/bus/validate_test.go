package bus

import (
	"context"
	"errors"
	"testing"

	"github.com/munisp/meridian-core-platform/packages/events/envelope"
)

func testEnv(t *testing.T) envelope.Envelope {
	t.Helper()
	env, err := envelope.New("nrs.test.hook.v1", "svc-test", "", "", map[string]any{"a": 1})
	if err != nil {
		t.Fatal(err)
	}
	return env
}

func TestValidateBeforePublishDevWarns(t *testing.T) {
	t.Setenv("PROFILE", "")
	SetPublishValidator(func(topic string, env envelope.Envelope) error {
		return errors.New("boom")
	})
	defer SetPublishValidator(nil)
	b := NewInproc()
	got := 0
	_, _ = b.Subscribe("nrs.test.hook.v1", func(ctx context.Context, e envelope.Envelope) error {
		got++
		return nil
	})
	vb := NewValidating(b)
	if err := vb.Publish(context.Background(), "nrs.test.hook.v1", testEnv(t)); err != nil {
		t.Fatalf("dev mode must warn-and-allow, got %v", err)
	}
	if got != 1 {
		t.Fatalf("event must still be delivered in dev, got %d", got)
	}
}

func TestValidateBeforePublishProdRejects(t *testing.T) {
	t.Setenv("PROFILE", "prod")
	SetPublishValidator(func(topic string, env envelope.Envelope) error {
		return errors.New("boom")
	})
	defer SetPublishValidator(nil)
	b := NewValidating(NewInproc())
	if err := b.Publish(context.Background(), "nrs.test.hook.v1", testEnv(t)); err == nil {
		t.Fatal("prod mode must reject invalid publish")
	}
}

func TestValidateBeforePublishPassThrough(t *testing.T) {
	t.Setenv("PROFILE", "prod")
	SetPublishValidator(func(topic string, env envelope.Envelope) error { return nil })
	defer SetPublishValidator(nil)
	b := NewValidating(NewInproc())
	if err := b.Publish(context.Background(), "nrs.test.hook.v1", testEnv(t)); err != nil {
		t.Fatalf("valid publish must pass: %v", err)
	}
}

func TestNewFromEnvWrapsWhenValidatorSet(t *testing.T) {
	t.Setenv("PROFILE", "prod")
	t.Setenv("EVENT_BUS", "inproc")
	t.Setenv("KAFKA_BROKERS", "")
	SetPublishValidator(func(topic string, env envelope.Envelope) error {
		return errors.New("nope")
	})
	defer SetPublishValidator(nil)
	b := NewFromEnv()
	if _, ok := b.(*validatingBus); !ok {
		t.Fatalf("NewFromEnv must wrap with validator, got %T", b)
	}
	if err := b.Publish(context.Background(), "nrs.test.hook.v1", testEnv(t)); err == nil {
		t.Fatal("wrapped bus must reject in prod")
	}
}

func TestNewFromEnvNoValidator(t *testing.T) {
	t.Setenv("EVENT_BUS", "inproc")
	t.Setenv("KAFKA_BROKERS", "")
	SetPublishValidator(nil)
	b := NewFromEnv()
	if _, ok := b.(*InprocBus); !ok {
		t.Fatalf("without validator NewFromEnv must return raw bus, got %T", b)
	}
}
