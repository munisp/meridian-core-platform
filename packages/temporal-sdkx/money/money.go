// Package money defines the Temporal workflow definitions for Meridian's
// money-moving sagas (F10): CaptureSaga, RefundWorkflow and
// RemittanceWorkflow. The workflows run on the sdkx in-process runner in
// dev and register against a real Temporal server when TEMPORAL_URL is set
// (env-selected, FAIL-CLOSED in prod: a configured-but-unreachable Temporal
// is a startup error, never a silent in-proc fallback for money flows).
//
// All ledger legs use deterministic transfer ids (seed = workflow entity id)
// so activity retries replay instead of double-posting; every workflow is
// therefore idempotent per business key (payment id / refund id /
// remittance run id).
package money

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"

	sdkx "github.com/munisp/meridian-core-platform/packages/temporal-sdkx"
)

// ---------------------------------------------------------------------------
// Ledger port (TigerBeetle-semantics subset the money workflows need)
// ---------------------------------------------------------------------------

// Transfer is one double-entry ledger transfer (amount in kobo).
type Transfer struct {
	ID              string
	DebitAccountID  string
	CreditAccountID string
	Ledger          uint64
	Code            uint16
	AmountKobo      uint64
	Pending         bool
	UserData        string
}

// LedgerPort abstracts the ledger client so workflows run against the dev
// shim, the core ledger REST service, or a TigerBeetle cluster. Create
// operations MUST be idempotent on caller-supplied ids: replaying an
// identical transfer returns success without moving money again.
type LedgerPort interface {
	// CreatePending creates a pending transfer (idempotent on ID).
	CreatePending(t Transfer) error
	// PostPendingAs posts a pending transfer under postID (idempotent:
	// replaying after the pending was consumed by the same post returns
	// success).
	PostPendingAs(pendingID, postID string, amountKobo uint64) error
	// VoidPending releases a pending transfer (idempotent: voiding an
	// already-resolved transfer returns success).
	VoidPending(pendingID string) error
	// Transfer posts an immediate transfer (idempotent on ID).
	Transfer(t Transfer) error
	// GetTransfer returns (transfer, found, error).
	GetTransfer(id string) (Transfer, bool, error)
}

// DeterministicID derives a stable 128-bit hex transfer id from a seed.
func DeterministicID(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:16])
}

// ---------------------------------------------------------------------------
// CaptureSaga — PSSP capture -> ledger post -> fee leg, with compensation
// ---------------------------------------------------------------------------

// CaptureInput drives CaptureSaga. PaymentID is the idempotency key.
type CaptureInput struct {
	PaymentID          string `json:"payment_id"`
	PayerAccountID     string `json:"payer_account_id"`
	CollectionsAccount string `json:"collections_account"`
	FeeAccountID       string `json:"fee_account_id,omitempty"`
	AmountKobo         uint64 `json:"amount_kobo"`
	FeeKobo            uint64 `json:"fee_kobo,omitempty"`
	Ledger             uint64 `json:"ledger"`
}

// CaptureSaga posts a captured payment: pending -> post -> fee leg.
// Compensation: fee reversal + post reversal (only for legs that landed) +
// void of an un-posted pending.
func CaptureSaga(lc LedgerPort) sdkx.Workflow {
	return func(ctx context.Context, input any) (any, error) {
		in, ok := input.(CaptureInput)
		if !ok || in.PaymentID == "" || in.AmountKobo == 0 {
			return nil, fmt.Errorf("capture saga: invalid input %+v", input)
		}
		pendID := DeterministicID("cap-pend:" + in.PaymentID)
		postID := DeterministicID("cap-post:" + in.PaymentID)
		feeID := DeterministicID("cap-fee:" + in.PaymentID)
		pend := Transfer{ID: pendID, DebitAccountID: in.PayerAccountID, CreditAccountID: in.CollectionsAccount,
			Ledger: in.Ledger, Code: 1, AmountKobo: in.AmountKobo, Pending: true, UserData: "capture:" + in.PaymentID}

		reverse := func(idSeed string, amount uint64, from, to string) error {
			if amount == 0 {
				return nil
			}
			// only reverse legs that actually landed
			if _, found, err := lc.GetTransfer(DeterministicID(idSeed)); err != nil || !found {
				return err
			}
			return lc.Transfer(Transfer{ID: DeterministicID("cap-rev:" + idSeed), DebitAccountID: from,
				CreditAccountID: to, Ledger: in.Ledger, Code: 3, AmountKobo: amount, UserData: "capture-reversal:" + in.PaymentID})
		}

		saga := sdkx.NewSaga(
			sdkx.SagaStep{
				Name: "create-pending",
				Action: func(ctx context.Context, _ any) (any, error) {
					return pendID, lc.CreatePending(pend)
				},
				Compensation: func(ctx context.Context, _ any) error {
					return lc.VoidPending(pendID) // no-op if already posted/voided
				},
			},
			sdkx.SagaStep{
				Name: "post-pending",
				Action: func(ctx context.Context, _ any) (any, error) {
					return postID, lc.PostPendingAs(pendID, postID, in.AmountKobo)
				},
				Compensation: func(ctx context.Context, _ any) error {
					return reverse("cap-post:"+in.PaymentID, in.AmountKobo, in.CollectionsAccount, in.PayerAccountID)
				},
			},
			sdkx.SagaStep{
				Name: "fee-leg",
				Action: func(ctx context.Context, _ any) (any, error) {
					if in.FeeKobo == 0 || in.FeeAccountID == "" {
						return "", nil
					}
					return feeID, lc.Transfer(Transfer{ID: feeID, DebitAccountID: in.CollectionsAccount,
						CreditAccountID: in.FeeAccountID, Ledger: in.Ledger, Code: 5, AmountKobo: in.FeeKobo,
						UserData: "capture-fee:" + in.PaymentID})
				},
				Compensation: func(ctx context.Context, _ any) error {
					return reverse("cap-fee:"+in.PaymentID, in.FeeKobo, in.FeeAccountID, in.CollectionsAccount)
				},
			},
		)
		if err := saga.Run(ctx, nil); err != nil {
			return nil, err
		}
		return map[string]any{"payment_id": in.PaymentID, "post_transfer_id": postID, "fee_transfer_id": feeID}, nil
	}
}

// ---------------------------------------------------------------------------
// RefundWorkflow — decision -> pending (refund treasury -> taxpayer) -> post
// ---------------------------------------------------------------------------

// RefundInput drives RefundWorkflow. RefundID is the idempotency key
// (derived per (tin, period, tax_type) by the caller).
type RefundInput struct {
	RefundID        string `json:"refund_id"`
	TreasuryAccount string `json:"treasury_account"`
	TaxpayerAccount string `json:"taxpayer_account"`
	AmountKobo      uint64 `json:"amount_kobo"`
	Ledger          uint64 `json:"ledger"`
}

// RefundWorkflow executes an approved refund as pending -> post with void
// compensation. Idempotent per RefundID.
func RefundWorkflow(lc LedgerPort) sdkx.Workflow {
	return func(ctx context.Context, input any) (any, error) {
		in, ok := input.(RefundInput)
		if !ok || in.RefundID == "" || in.AmountKobo == 0 {
			return nil, fmt.Errorf("refund workflow: invalid input %+v", input)
		}
		pendID := DeterministicID("ref-pend:" + in.RefundID)
		postID := DeterministicID("ref-post:" + in.RefundID)
		saga := sdkx.NewSaga(
			sdkx.SagaStep{
				Name: "refund-pending",
				Action: func(ctx context.Context, _ any) (any, error) {
					return pendID, lc.CreatePending(Transfer{ID: pendID, DebitAccountID: in.TreasuryAccount,
						CreditAccountID: in.TaxpayerAccount, Ledger: in.Ledger, Code: 1, AmountKobo: in.AmountKobo,
						Pending: true, UserData: "refund:" + in.RefundID})
				},
				Compensation: func(ctx context.Context, _ any) error { return lc.VoidPending(pendID) },
			},
			sdkx.SagaStep{
				Name: "refund-post",
				Action: func(ctx context.Context, _ any) (any, error) {
					return postID, lc.PostPendingAs(pendID, postID, in.AmountKobo)
				},
				Compensation: func(ctx context.Context, _ any) error {
					if _, found, err := lc.GetTransfer(postID); err != nil || !found {
						return err
					}
					return lc.Transfer(Transfer{ID: DeterministicID("ref-rev:" + in.RefundID), DebitAccountID: in.TaxpayerAccount,
						CreditAccountID: in.TreasuryAccount, Ledger: in.Ledger, Code: 3, AmountKobo: in.AmountKobo,
						UserData: "refund-reversal:" + in.RefundID})
				},
			},
		)
		if err := saga.Run(ctx, nil); err != nil {
			return nil, err
		}
		return map[string]any{"refund_id": in.RefundID, "post_transfer_id": postID, "status": "posted"}, nil
	}
}

// ---------------------------------------------------------------------------
// RemittanceWorkflow — WHT credits + mark-remitted as one compensated unit
// ---------------------------------------------------------------------------

// RemittanceCredit is one vendor WHT credit leg.
type RemittanceCredit struct {
	DeductionID   string `json:"deduction_id"`
	CreditAccount string `json:"credit_account"`
	AmountKobo    uint64 `json:"amount_kobo"`
}

// RemittanceInput drives RemittanceWorkflow. RunID is the idempotency key.
type RemittanceInput struct {
	RunID        string             `json:"run_id"`
	DebitAccount string             `json:"debit_account"` // WHT clearing account
	Credits      []RemittanceCredit `json:"credits"`
	Ledger       uint64             `json:"ledger"`
}

// MarkRemitted is the durable "deductions marked remitted" activity
// (service-specific store write), injected by the registering service. It
// must be idempotent per RunID.
type MarkRemitted func(ctx context.Context, runID string, deductionIDs []string) error

// RemittanceWorkflow posts deterministic credit transfers per deduction and
// then marks the run remitted — as one compensated unit: failure of
// mark-remitted reverses every posted credit, so a retried run can never
// double-post (audit Flow 3).
func RemittanceWorkflow(lc LedgerPort, mark MarkRemitted) sdkx.Workflow {
	return func(ctx context.Context, input any) (any, error) {
		in, ok := input.(RemittanceInput)
		if !ok || in.RunID == "" {
			return nil, fmt.Errorf("remittance workflow: invalid input %+v", input)
		}
		var steps []sdkx.SagaStep
		var ids []string
		for _, c := range in.Credits {
			c := c
			creditID := DeterministicID("wht-cr:" + in.RunID + ":" + c.DeductionID)
			ids = append(ids, c.DeductionID)
			steps = append(steps, sdkx.SagaStep{
				Name: "credit:" + c.DeductionID,
				Action: func(ctx context.Context, _ any) (any, error) {
					return creditID, lc.Transfer(Transfer{ID: creditID, DebitAccountID: in.DebitAccount,
						CreditAccountID: c.CreditAccount, Ledger: in.Ledger, Code: 5, AmountKobo: c.AmountKobo,
						UserData: "wht-credit:" + in.RunID + ":" + c.DeductionID})
				},
				Compensation: func(ctx context.Context, _ any) error {
					if _, found, err := lc.GetTransfer(creditID); err != nil || !found {
						return err
					}
					return lc.Transfer(Transfer{ID: DeterministicID("wht-cr-rev:" + in.RunID + ":" + c.DeductionID),
						DebitAccountID: c.CreditAccount, CreditAccountID: in.DebitAccount, Ledger: in.Ledger,
						Code: 3, AmountKobo: c.AmountKobo, UserData: "wht-credit-reversal:" + in.RunID + ":" + c.DeductionID})
				},
			})
		}
		steps = append(steps, sdkx.SagaStep{
			Name: "mark-remitted",
			Action: func(ctx context.Context, _ any) (any, error) {
				if mark == nil {
					return nil, fmt.Errorf("mark-remitted activity not bound")
				}
				return in.RunID, mark(ctx, in.RunID, ids)
			},
			// no compensation: credits are reversed by the saga if a LATER
			// step failed; mark-remitted is the final step.
		})
		if err := sdkx.NewSaga(steps...).Run(ctx, nil); err != nil {
			return nil, err
		}
		return map[string]any{"run_id": in.RunID, "credits_posted": len(in.Credits), "remitted": true}, nil
	}
}

// ---------------------------------------------------------------------------
// Registration + env-selected runner (fail-closed in prod)
// ---------------------------------------------------------------------------

// Deps binds the workflow dependencies at registration time.
type Deps struct {
	Ledger       LedgerPort
	MarkRemitted MarkRemitted // nil disables RemittanceWorkflow
}

// Register registers all money workflow definitions on the runner (in-proc
// or real Temporal — the same definitions serve both).
func Register(r sdkx.Runner, deps Deps) {
	r.RegisterWorkflow("CaptureSaga", CaptureSaga(deps.Ledger))
	r.RegisterWorkflow("RefundWorkflow", RefundWorkflow(deps.Ledger))
	if deps.MarkRemitted != nil {
		r.RegisterWorkflow("RemittanceWorkflow", RemittanceWorkflow(deps.Ledger, deps.MarkRemitted))
	}
}

// NewRunnerFromEnv selects the runner for money workflows:
//   - TEMPORAL_URL set (profile=prod): a real Temporal worker — and a
//     connection/start failure is a hard error (FAIL CLOSED; money sagas
//     must never silently degrade to the crash-unrecoverable in-proc
//     runner in prod).
//   - unset (profile=dev): the in-process runner.
func NewRunnerFromEnv() (sdkx.Runner, error) {
	if url := os.Getenv("TEMPORAL_URL"); url != "" {
		tr, err := sdkx.NewTemporalRunner(url, os.Getenv("TEMPORAL_NAMESPACE"), os.Getenv("TEMPORAL_TASK_QUEUE"))
		if err != nil {
			return nil, fmt.Errorf("profile=prod money workflows: temporal init: %w", err)
		}
		if err := tr.Start(); err != nil {
			tr.Stop()
			return nil, fmt.Errorf("profile=prod money workflows: temporal start: %w", err)
		}
		return tr, nil
	}
	return sdkx.NewInprocRunner(), nil
}
