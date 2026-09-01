package tb

// trace.go — OTel spans around every TigerBeetle client call (DESIGN-
// CONTRACT: outbound spans on the money-path client). Spans are named
// "tigerbeetle.<op>" with only the operation + business result code as
// attributes — NEVER account ids, amounts or user data (cardinality
// ban-list). Tracing is fail-soft by construction: the decorator adds no new
// error paths, so funds flows never block on telemetry (no-op provider =
// near-zero cost).

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const tbTracerName = "github.com/munisp/meridian-core-platform/services/ledger/internal/tb"

// tracedClient wraps a LedgerClient with one span per call. ctx is the span
// parent (context.Background() when constructed via Traced; the request
// context when constructed via WithContext).
type tracedClient struct {
	inner LedgerClient
	ctx   context.Context
}

// Traced returns a LedgerClient that emits a span per TigerBeetle operation
// and delegates everything else unchanged. A nil client returns nil.
func Traced(inner LedgerClient) LedgerClient {
	if inner == nil {
		return nil
	}
	return &tracedClient{inner: inner, ctx: context.Background()}
}

// WithContext returns a LedgerClient whose spans parent to ctx (typically the
// request span, so TB calls join the request trace). Use per-request:
// client := tb.WithContext(r.Context(), s.client).
func WithContext(ctx context.Context, inner LedgerClient) LedgerClient {
	if inner == nil {
		return nil
	}
	return &tracedClient{inner: inner, ctx: ctx}
}

func (c *tracedClient) start(op string) trace.Span {
	_, span := otel.Tracer(tbTracerName).Start(c.ctx, "tigerbeetle."+op,
		trace.WithSpanKind(trace.SpanKindClient))
	return span
}

// end closes a span. Transport/kit errors are recorded as span errors; a
// non-OK business result (Exists, AccountNotFound, ...) is an attribute, not
// a telemetry error.
func end(span trace.Span, code ResultCode, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	} else if code != OK {
		span.SetAttributes(attribute.String("tigerbeetle.result", string(code)))
	}
	span.End()
}

func endNoCode(span trace.Span, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	span.End()
}

func (c *tracedClient) CreateAccounts(a []Account) ([]Result, error) {
	span := c.start("create_accounts")
	res, err := c.inner.CreateAccounts(a)
	endNoCode(span, err)
	return res, err
}

func (c *tracedClient) Transfer(t Transfer) (Result, error) {
	span := c.start("transfer")
	res, err := c.inner.Transfer(t)
	end(span, res.Code, err)
	return res, err
}

func (c *tracedClient) PendingTransfer(t Transfer) (Result, error) {
	span := c.start("pending_transfer")
	res, err := c.inner.PendingTransfer(t)
	end(span, res.Code, err)
	return res, err
}

func (c *tracedClient) PostPending(id ID, amount uint64, code uint16) (Result, error) {
	span := c.start("post_pending")
	res, err := c.inner.PostPending(id, amount, code)
	end(span, res.Code, err)
	return res, err
}

func (c *tracedClient) PostPendingAs(pendingID, postID ID, amount uint64, code uint16) (Result, error) {
	span := c.start("post_pending_as")
	res, err := c.inner.PostPendingAs(pendingID, postID, amount, code)
	end(span, res.Code, err)
	return res, err
}

func (c *tracedClient) VoidPending(id ID, code uint16) (Result, error) {
	span := c.start("void_pending")
	res, err := c.inner.VoidPending(id, code)
	end(span, res.Code, err)
	return res, err
}

func (c *tracedClient) Balance(id ID) (Balance, Result, error) {
	span := c.start("balance")
	b, res, err := c.inner.Balance(id)
	end(span, res.Code, err)
	return b, res, err
}

func (c *tracedClient) GetAccount(id ID) (Account, Result, error) {
	span := c.start("get_account")
	a, res, err := c.inner.GetAccount(id)
	end(span, res.Code, err)
	return a, res, err
}

func (c *tracedClient) GetTransfer(id ID) (Transfer, Result, error) {
	span := c.start("get_transfer")
	t, res, err := c.inner.GetTransfer(id)
	end(span, res.Code, err)
	return t, res, err
}

func (c *tracedClient) ListAccounts() ([]Account, error) {
	span := c.start("list_accounts")
	a, err := c.inner.ListAccounts()
	endNoCode(span, err)
	return a, err
}

func (c *tracedClient) ListTransfers(id ID) ([]Transfer, error) {
	span := c.start("list_transfers")
	t, err := c.inner.ListTransfers(id)
	endNoCode(span, err)
	return t, err
}
