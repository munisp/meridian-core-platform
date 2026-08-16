// money_workflows.go — wires packages/temporal-sdkx/money into the ledger
// binary (docs/temporal-migration.md, "money workflows (follow-up)").
//
// The money sagas (CaptureSaga / RefundWorkflow / RemittanceWorkflow) run
// against the service's LedgerClient via tbMoneyAdapter:
//
//	TEMPORAL_URL set   -> real Temporal worker; init/start failure is a
//	                      boot error (FAIL CLOSED — money sagas never
//	                      degrade silently to the crash-unrecoverable
//	                      inproc runner in prod)
//	TEMPORAL_URL unset -> sdkx inproc dev runner (explicit dev default)
//
// All transfer ids are deterministic (money.DeterministicID), so activity
// retries replay idempotently against both the DevClient and a real
// TigerBeetle cluster.
package main

import (
	"fmt"
	"log"

	sdkx "github.com/munisp/meridian-core-platform/packages/temporal-sdkx"
	"github.com/munisp/meridian-core-platform/packages/temporal-sdkx/money"
	"github.com/munisp/meridian-core-platform/services/ledger/internal/tb"
)

// tbMoneyAdapter adapts tb.LedgerClient to money.LedgerPort. Result codes
// that mean "this exact operation already happened" are success (idempotent
// replay); any other non-OK code is an error.
type tbMoneyAdapter struct{ c tb.LedgerClient }

func toTBTransfer(t money.Transfer) (tb.Transfer, error) {
	id, err := tb.ParseID(t.ID)
	if err != nil {
		return tb.Transfer{}, fmt.Errorf("transfer id: %w", err)
	}
	dr, err := tb.ParseID(t.DebitAccountID)
	if err != nil {
		return tb.Transfer{}, fmt.Errorf("debit account id: %w", err)
	}
	cr, err := tb.ParseID(t.CreditAccountID)
	if err != nil {
		return tb.Transfer{}, fmt.Errorf("credit account id: %w", err)
	}
	return tb.Transfer{ID: id, DebitAccountID: dr, CreditAccountID: cr,
		Ledger: t.Ledger, Code: t.Code, Amount: t.AmountKobo, UserData: t.UserData}, nil
}

func resultErr(op string, res tb.Result) error {
	return fmt.Errorf("money %s: %s %s", op, res.Code, res.Message)
}

func (a *tbMoneyAdapter) CreatePending(t money.Transfer) error {
	tr, err := toTBTransfer(t)
	if err != nil {
		return err
	}
	res, err := a.c.PendingTransfer(tr)
	if err != nil {
		return err
	}
	switch res.Code {
	case tb.OK, tb.Exists: // replay of the same pending transfer
		return nil
	case tb.ExistsWithDifferentAttributes:
		// The stored transfer differs only because an earlier attempt
		// already resolved this deterministic pending id — a replay.
		ex, lres, lerr := a.c.GetTransfer(tr.ID)
		if lerr == nil && lres.Code == tb.OK && (ex.Resolved || !ex.Pending) {
			return nil
		}
		return resultErr("create-pending", res)
	default:
		return resultErr("create-pending", res)
	}
}

func (a *tbMoneyAdapter) PostPendingAs(pendingID, postID string, amountKobo uint64) error {
	id, err := tb.ParseID(pendingID)
	if err != nil {
		return err
	}
	pt, res, err := a.c.GetTransfer(id)
	if err != nil {
		return err
	}
	if res.Code != tb.OK {
		return resultErr("post-pending lookup", res)
	}
	if pt.Resolved || !pt.Pending {
		// Idempotent replay: ids are deterministic, so only this workflow
		// could have resolved the pending transfer.
		return nil
	}
	res, err = a.c.PostPending(id, amountKobo, pt.Code)
	if err != nil {
		return err
	}
	switch res.Code {
	case tb.OK, tb.PendingTransferNotPending: // consumed by an earlier attempt
		return nil
	default:
		return resultErr("post-pending", res)
	}
}

func (a *tbMoneyAdapter) VoidPending(pendingID string) error {
	id, err := tb.ParseID(pendingID)
	if err != nil {
		return err
	}
	res, err := a.c.VoidPending(id, 0)
	if err != nil {
		return err
	}
	switch res.Code {
	case tb.OK, tb.PendingTransferNotFound, tb.PendingTransferNotPending:
		// Nothing to void, or already resolved by an earlier attempt.
		return nil
	default:
		return resultErr("void-pending", res)
	}
}

func (a *tbMoneyAdapter) Transfer(t money.Transfer) error {
	tr, err := toTBTransfer(t)
	if err != nil {
		return err
	}
	res, err := a.c.Transfer(tr)
	if err != nil {
		return err
	}
	switch res.Code {
	case tb.OK, tb.Exists: // replay of the same transfer
		return nil
	default:
		return resultErr("transfer", res)
	}
}

func (a *tbMoneyAdapter) GetTransfer(id string) (money.Transfer, bool, error) {
	tid, err := tb.ParseID(id)
	if err != nil {
		return money.Transfer{}, false, err
	}
	t, res, err := a.c.GetTransfer(tid)
	if err != nil {
		return money.Transfer{}, false, err
	}
	if res.Code != tb.OK {
		return money.Transfer{}, false, nil
	}
	return money.Transfer{ID: t.ID.String(), DebitAccountID: t.DebitAccountID.String(),
		CreditAccountID: t.CreditAccountID.String(), Ledger: t.Ledger, Code: t.Code,
		AmountKobo: t.Amount, Pending: t.Pending && !t.Resolved, UserData: t.UserData}, true, nil
}

// initMoneyWorkflows selects the money runner and registers the money sagas.
// Boot fails closed when TEMPORAL_URL is set but the worker cannot start.
func (s *server) initMoneyWorkflows() {
	runner, err := money.NewRunnerFromEnv()
	if err != nil {
		log.Fatalf("component=ledger FATAL: money workflows: %v", err)
	}
	money.Register(runner, money.Deps{Ledger: &tbMoneyAdapter{c: s.client}})
	mode := "dev-inproc"
	if _, ok := runner.(*sdkx.TemporalRunner); ok {
		mode = "temporal"
	}
	s.wfRunner = runner
	log.Printf("component=ledger money-workflows registered=%v mode=%s", runner.WorkflowNames(), mode)
}
