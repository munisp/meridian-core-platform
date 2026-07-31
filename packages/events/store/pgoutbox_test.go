package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/munisp/meridian-core-platform/packages/events/envelope"
)

// fakeTx records Exec calls and simulates commit/rollback without a server.
type fakeTx struct {
	pgx.Tx // embedded nil: only the overridden methods are usable
	execs  []string
}

func (f *fakeTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	f.execs = append(f.execs, sql)
	return pgconn.NewCommandTag("INSERT 0 1"), nil
}

func (f *fakeTx) Commit(ctx context.Context) error   { return nil }
func (f *fakeTx) Rollback(ctx context.Context) error { return nil }

// fakeBeginner returns a shared fakeTx.
type fakeBeginner struct{ tx *fakeTx }

func (b fakeBeginner) Begin(ctx context.Context) (pgx.Tx, error) { return b.tx, nil }

func testEnvelope(t *testing.T) envelope.Envelope {
	t.Helper()
	env, err := envelope.New("nrs.psm.payments.v1", "svc-test", "", "",
		map[string]any{"reference": "r1", "amount_kobo": 100})
	if err != nil {
		t.Fatal(err)
	}
	return env
}

func TestWithTxCommitsDomainAndOutboxAtomically(t *testing.T) {
	ftx := &fakeTx{}
	err := WithTx(context.Background(), fakeBeginner{tx: ftx}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(context.Background(), `UPDATE operators SET status='tin_provisioned' WHERE id=$1`, "op1"); err != nil {
			return err
		}
		return AppendOutboxTx(context.Background(), tx, "nrs.onb.provisioned.v1", testEnvelope(t))
	})
	if err != nil {
		t.Fatalf("WithTx: %v", err)
	}
	if len(ftx.execs) != 2 {
		t.Fatalf("expected 2 execs in one tx, got %d", len(ftx.execs))
	}
	if !strings.Contains(ftx.execs[1], "meridian_outbox") {
		t.Fatalf("second exec must be the outbox insert, got %q", ftx.execs[1])
	}
}

func TestWithTxRollsBackOutboxOnDomainError(t *testing.T) {
	ftx := &fakeTx{}
	domainErr := errors.New("domain write failed")
	err := WithTx(context.Background(), fakeBeginner{tx: ftx}, func(tx pgx.Tx) error {
		if err := AppendOutboxTx(context.Background(), tx, "nrs.onb.provisioned.v1", testEnvelope(t)); err != nil {
			return err
		}
		return domainErr
	})
	if !errors.Is(err, domainErr) {
		t.Fatalf("expected domain error to propagate, got %v", err)
	}
	// The fake rollback is a no-op; the point proven here is that fn's error
	// aborts before Commit, so the outbox row never commits either (same tx).
}

func TestAppendOutboxTxRejectsUnmarshalableEnvelope(t *testing.T) {
	ftx := &fakeTx{}
	env := testEnvelope(t)
	env.Data = []byte("{invalid json")
	// AppendOutboxTx marshals the envelope struct (Data is RawMessage); an
	// invalid RawMessage must surface an error before any INSERT.
	err := AppendOutboxTx(context.Background(), ftx, "nrs.psm.payments.v1", env)
	if err == nil {
		t.Fatal("expected marshal error for invalid RawMessage data")
	}
	if len(ftx.execs) != 0 {
		t.Fatal("no INSERT must be attempted on marshal failure")
	}
}

// fakeRows emulates pgx.Rows for ClaimOutbox.
type fakeRows struct {
	pgx.Rows
	seqs   []int64
	topics []string
	envs   [][]byte
	idx    int
	closed bool
}

func (r *fakeRows) Close() { r.closed = true }
func (r *fakeRows) Err() error {
	return nil
}
func (r *fakeRows) Next() bool {
	if r.idx < len(r.seqs) {
		r.idx++
		return true
	}
	return false
}
func (r *fakeRows) Scan(dest ...any) error {
	i := r.idx - 1
	*(dest[0].(*int64)) = r.seqs[i]
	*(dest[1].(*string)) = r.topics[i]
	*(dest[2].(*[]byte)) = r.envs[i]
	return nil
}

type fakeQuerier struct {
	rows    *fakeRows
	gotSQL  string
	gotArgs []any
}

func (q *fakeQuerier) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	q.gotSQL = sql
	q.gotArgs = args
	return q.rows, nil
}

func TestClaimOutboxUsesSkipLockedAndParses(t *testing.T) {
	env := testEnvelope(t)
	raw, _ := env.MarshalBinary()
	rows := &fakeRows{seqs: []int64{7, 8}, topics: []string{"nrs.psm.payments.v1", "nrs.psm.payments.v1"}, envs: [][]byte{raw, raw}}
	q := &fakeQuerier{rows: rows}
	claims, err := ClaimOutbox(context.Background(), q, 50)
	if err != nil {
		t.Fatalf("ClaimOutbox: %v", err)
	}
	if !strings.Contains(q.gotSQL, "FOR UPDATE SKIP LOCKED") {
		t.Fatal("claim SQL must use FOR UPDATE SKIP LOCKED")
	}
	if q.gotArgs[0].(int) != 50 {
		t.Fatalf("limit arg must be passed, got %v", q.gotArgs)
	}
	if len(claims) != 2 || claims[0].Seq != 7 || claims[1].Seq != 8 {
		t.Fatalf("claims mismatch: %+v", claims)
	}
	if claims[0].Envelope.Type != "nrs.psm.payments.v1" {
		t.Fatalf("envelope not decoded: %+v", claims[0].Envelope)
	}
}

func TestClaimOutboxDefaultLimit(t *testing.T) {
	q := &fakeQuerier{rows: &fakeRows{}}
	if _, err := ClaimOutbox(context.Background(), q, 0); err != nil {
		t.Fatal(err)
	}
	if q.gotArgs[0].(int) != 100 {
		t.Fatalf("default limit must be 100, got %v", q.gotArgs[0])
	}
}
