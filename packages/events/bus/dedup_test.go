package bus

// §6.3 "consumer crash" + at-least-once redelivery coverage (R7 item 3).

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/munisp/meridian-core-platform/packages/events/envelope"
)

func dedupTestEnv(t *testing.T, id string) envelope.Envelope {
	t.Helper()
	env, err := envelope.New("nrs.test.v1", "test", "t1", "", map[string]any{"n": 1})
	if err != nil {
		t.Fatal(err)
	}
	env.ID = id // pin the id so redeliveries share the dedup key
	return env
}

// Duplicate delivery (ack lost / replay): the handler runs exactly once.
func TestDedupDuplicateDeliverySingleEffect(t *testing.T) {
	d := NewDedupConsumer(NewInprocProcessedStore())
	env := dedupTestEnv(t, "evt-1")
	calls := 0
	fn := func(context.Context, envelope.Envelope) error { calls++; return nil }
	ctx := context.Background()
	if err := d.Handle(ctx, "topic-a", env, fn); err != nil {
		t.Fatal(err)
	}
	if err := d.Handle(ctx, "topic-a", env, fn); err != nil { // redelivery
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("duplicate delivery must invoke the handler once, got %d", calls)
	}
	if err := d.Handle(ctx, "topic-a", dedupTestEnv(t, "evt-2"), fn); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("distinct event must be processed, got %d calls", calls)
	}
}

// Crash after the effect but before the ack: the claim persists, so the
// replay is deduped and the handler is NOT re-invoked.
func TestDedupCrashAfterEffectBeforeAck(t *testing.T) {
	st := &completeFailStore{NewInprocProcessedStore()}
	d := NewDedupConsumer(st)
	env := dedupTestEnv(t, "evt-crash")
	ctx := context.Background()
	effects := 0
	fn := func(context.Context, envelope.Envelope) error { effects++; return nil }
	// first delivery: effect applied, then the process "dies" before the ack
	if err := d.Handle(ctx, "topic-a", env, fn); err == nil {
		t.Fatal("expected the lost-ack (Complete failure) to surface")
	}
	if effects != 1 {
		t.Fatalf("effect must be applied once, got %d", effects)
	}
	// restart + replay over the SAME processed store: deduped
	d2 := NewDedupConsumer(st)
	if err := d2.Handle(ctx, "topic-a", env, fn); err != nil {
		t.Fatalf("replayed delivery must be acked, got %v", err)
	}
	if effects != 1 {
		t.Fatalf("crash-before-ack replay must not re-run the handler, effects=%d", effects)
	}
}

// completeFailStore simulates a crash between handler effect and ack.
type completeFailStore struct{ *InprocProcessedStore }

func (s *completeFailStore) Complete(string) error { return errors.New("simulated crash: ack lost") }

// Handler failure releases the claim: the redelivery retries the handler.
func TestDedupHandlerErrorAllowsRedelivery(t *testing.T) {
	d := NewDedupConsumer(NewInprocProcessedStore())
	env := dedupTestEnv(t, "evt-flaky")
	ctx := context.Background()
	calls := 0
	boom := errors.New("downstream down")
	fn := func(context.Context, envelope.Envelope) error {
		calls++
		if calls == 1 {
			return boom
		}
		return nil
	}
	if err := d.Handle(ctx, "topic-a", env, fn); !errors.Is(err, boom) {
		t.Fatalf("handler error must surface (no ack), got %v", err)
	}
	if err := d.Handle(ctx, "topic-a", env, fn); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("redelivery must retry the handler, got %d calls", calls)
	}
}

// Offset-keyed dedup: same coordinates under a fresh event id still dedup.
func TestDedupOffsetKey(t *testing.T) {
	d := NewDedupConsumer(NewInprocProcessedStore())
	ctx := context.Background()
	calls := 0
	fn := func(context.Context, envelope.Envelope) error { calls++; return nil }
	key := OffsetKey("topic-b", 3, 42)
	if err := d.HandleKey(ctx, key, dedupTestEnv(t, "evt-x"), fn); err != nil {
		t.Fatal(err)
	}
	if err := d.HandleKey(ctx, key, dedupTestEnv(t, "evt-y"), fn); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("offset-keyed dedup failed, calls=%d", calls)
	}
}

// End-to-end through the inproc bus + DLQ: a flaky handler is DLQ'd on its
// first (failing) delivery; manual replay then succeeds exactly once.
func TestDedupThroughInprocBusAndDLQ(t *testing.T) {
	b := NewInproc()
	d := NewDedupConsumer(NewInprocProcessedStore())
	ctx := context.Background()
	calls := 0
	fn := func(context.Context, envelope.Envelope) error {
		calls++
		if calls == 1 {
			return errors.New("transient")
		}
		return nil
	}
	if _, err := b.Subscribe("orders", d.Wrap("orders", fn)); err != nil {
		t.Fatal(err)
	}
	env := dedupTestEnv(t, "evt-bus")
	if err := b.Publish(ctx, "orders", env); err != nil {
		t.Fatal(err)
	}
	if dlq := b.DLQ("orders"); len(dlq) != 1 {
		t.Fatalf("failed delivery must land in the DLQ, got %d", len(dlq))
	}
	// replay from the DLQ: retried, handler succeeds; further replay deduped
	if err := b.Publish(ctx, "orders", env); err != nil {
		t.Fatal(err)
	}
	if err := b.Publish(ctx, "orders", env); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("handler must run exactly twice (fail + retry), got %d", calls)
	}
	if dlq := b.DLQ("orders"); len(dlq) != 1 {
		t.Fatalf("deduped replays must ack, not DLQ: %d", len(dlq))
	}
}

// Stale claims from the claim→effect crash window can be reclaimed.
func TestDedupReclaimStale(t *testing.T) {
	st := NewInprocProcessedStore()
	d := NewDedupConsumer(st)
	ctx := context.Background()
	cf := &completeFailStore{st}
	if err := NewDedupConsumer(cf).HandleKey(ctx, "k2", dedupTestEnv(t, "e2"),
		func(context.Context, envelope.Envelope) error { return nil }); err == nil {
		t.Fatal("lost-ack must surface")
	}
	if n := st.ReclaimStale(time.Now().Add(time.Minute)); n != 1 {
		t.Fatalf("expected 1 stale claim reclaimed, got %d", n)
	}
	calls := 0
	if err := d.HandleKey(ctx, "k2", dedupTestEnv(t, "e2"),
		func(context.Context, envelope.Envelope) error { calls++; return nil }); err != nil || calls != 1 {
		t.Fatalf("reclaimed key must be reprocessable: err=%v calls=%d", err, calls)
	}
}
