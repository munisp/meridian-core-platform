package events_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/munisp/meridian-core-platform/packages/events/auth"
	"github.com/munisp/meridian-core-platform/packages/events/bus"
	"github.com/munisp/meridian-core-platform/packages/events/envelope"
	"github.com/munisp/meridian-core-platform/packages/events/outbox"
	"github.com/munisp/meridian-core-platform/packages/events/store"
)

func TestULIDFormatAndMonotonicTime(t *testing.T) {
	a := envelope.NewULID()
	if len(a) != 26 {
		t.Fatalf("ulid len = %d, want 26", len(a))
	}
	for _, c := range a {
		if !strings.ContainsRune("0123456789ABCDEFGHJKMNPQRSTVWXYZ", c) {
			t.Fatalf("bad ulid char %q in %s", c, a)
		}
	}
	if a[0] > 'F' { // top 2 bits masked -> first char in 0-F
		t.Fatalf("first ulid char %c out of canonical range", a[0])
	}
	b := envelope.NewULID()
	if a == b {
		t.Fatal("ulids collide")
	}
}

func TestEnvelopeNew(t *testing.T) {
	env, err := envelope.New("nrs.psm.payments.v1", "ledger", "t1", "rp-x@1.0.0", map[string]any{"amount_kobo": 500})
	if err != nil {
		t.Fatal(err)
	}
	if env.Type != "nrs.psm.payments.v1" || env.Source != "ledger" || env.RulePackVersion != "rp-x@1.0.0" {
		t.Fatalf("bad envelope: %+v", env)
	}
	var data map[string]any
	if err := env.Decode(&data); err != nil {
		t.Fatal(err)
	}
	if data["amount_kobo"].(float64) != 500 {
		t.Fatalf("bad data: %v", data)
	}
}

func TestInprocBusDLQ(t *testing.T) {
	b := bus.NewInproc()
	defer b.Close()
	env, _ := envelope.New("nrs.x.v1", "test", "", "", nil)
	got := make(chan envelope.Envelope, 1)
	if _, err := b.Subscribe("t", func(ctx context.Context, e envelope.Envelope) error { got <- e; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Subscribe("t", func(ctx context.Context, e envelope.Envelope) error { return context.DeadlineExceeded }); err != nil {
		t.Fatal(err)
	}
	if err := b.Publish(context.Background(), "t", env); err != nil {
		t.Fatal(err)
	}
	select {
	case <-got:
	case <-time.After(time.Second):
		t.Fatal("handler not called")
	}
	if n := len(b.DLQ("t")); n != 1 {
		t.Fatalf("dlq len = %d, want 1", n)
	}
}

func TestJWT(t *testing.T) {
	tok, err := auth.SignHS256(auth.Claims{Sub: "u1", Roles: []string{"admin"}, TenantID: "t"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	c, err := auth.VerifyHS256(tok)
	if err != nil {
		t.Fatal(err)
	}
	if c.Sub != "u1" || !c.HasRole("admin") {
		t.Fatalf("bad claims: %+v", c)
	}
	if _, err := auth.VerifyHS256(tok + "x"); err == nil {
		t.Fatal("tampered token accepted")
	}
	expired, _ := auth.SignHS256(auth.Claims{Sub: "u2", Exp: time.Now().Add(-time.Hour).Unix()}, time.Hour)
	if _, err := auth.VerifyHS256(expired); err == nil {
		t.Fatal("expired token accepted")
	}
}

func TestStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	type doc struct {
		Name string `json:"name"`
		N    int    `json:"n"`
	}
	if err := s.Put("c", "1", doc{"a", 1}); err != nil {
		t.Fatal(err)
	}
	var d doc
	if err := s.Get("c", "1", &d); err != nil || d.Name != "a" {
		t.Fatalf("get: %v %+v", err, d)
	}
	// reload from disk
	s2, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s2.Get("c", "1", &d); err != nil || d.N != 1 {
		t.Fatalf("reload get: %v %+v", err, d)
	}
	if err := s2.Delete("c", "1"); err != nil {
		t.Fatal(err)
	}
	if err := s2.Get("c", "1", &d); err != store.ErrNotFound {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestOutboxRelay(t *testing.T) {
	dir := t.TempDir()
	fs, err := outbox.NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close()
	b := bus.NewInproc()
	defer b.Close()
	got := make(chan envelope.Envelope, 1)
	if _, err := b.Subscribe("nrs.x.v1", func(ctx context.Context, e envelope.Envelope) error { got <- e; return nil }); err != nil {
		t.Fatal(err)
	}
	env, _ := envelope.New("nrs.x.v1", "test", "", "", map[string]string{"k": "v"})
	if err := fs.Append("nrs.x.v1", env); err != nil {
		t.Fatal(err)
	}
	r := outbox.Relay{Store: fs, Bus: b, Dir: dir, Interval: 20 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Run(ctx)
	select {
	case e := <-got:
		if e.Type != "nrs.x.v1" {
			t.Fatalf("bad type %s", e.Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("relay did not publish")
	}
}
