package main

import (
	"context"
	"strings"
	"testing"

	"github.com/munisp/meridian-core-platform/packages/temporal-sdkx/money"
	"github.com/munisp/meridian-core-platform/services/ledger/internal/tb"
)

// newFundedDevLedger creates a dev client with payer/collections/fee
// accounts (ids derived like the money workflows do, from seeds).
func newFundedDevLedger(t *testing.T) (*tb.DevClient, *tbMoneyAdapter, tb.ID, tb.ID, tb.ID) {
	t.Helper()
	c := tb.NewDevClient()
	payer := moneyID(t, "acct:payer")
	collections := moneyID(t, "acct:collections")
	fee := moneyID(t, "acct:fee")
	res, err := c.CreateAccounts([]tb.Account{
		{ID: payer, Ledger: 1, Code: 1},
		{ID: collections, Ledger: 1, Code: 1},
		{ID: fee, Ledger: 1, Code: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		if r.Code != tb.OK {
			t.Fatalf("create accounts: %v", res)
		}
	}
	return c, &tbMoneyAdapter{c: c}, payer, collections, fee
}

func moneyID(t *testing.T, seed string) tb.ID {
	t.Helper()
	id, err := tb.ParseID(money.DeterministicID(seed))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// CaptureSaga executes end-to-end against the real DevClient through the
// adapter: pending -> post -> fee leg land as posted transfers.
func TestCaptureSagaAgainstDevClient(t *testing.T) {
	c, adapter, payer, collections, fee := newFundedDevLedger(t)
	wf := money.CaptureSaga(adapter)
	_, err := wf(context.Background(), money.CaptureInput{
		PaymentID: "pay-1", PayerAccountID: payer.String(),
		CollectionsAccount: collections.String(), FeeAccountID: fee.String(),
		AmountKobo: 10_000, FeeKobo: 250, Ledger: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	bal, res, err := c.Balance(payer)
	if err != nil || res.Code != tb.OK {
		t.Fatalf("balance: %v %v", res, err)
	}
	if bal.DebitsPosted != 10_000 {
		t.Fatalf("payer debits_posted=%d want 10000", bal.DebitsPosted)
	}
	if bal.DebitsPending != 0 {
		t.Fatalf("payer debits_pending=%d want 0", bal.DebitsPending)
	}
	// fee leg: collections -> fee account
	fbal, _, _ := c.Balance(fee)
	if fbal.CreditsPosted != 250 {
		t.Fatalf("fee credits_posted=%d want 250", fbal.CreditsPosted)
	}
}

// Idempotent replay: executing the same capture twice must not move money
// twice (deterministic ids + Exists-as-success in the adapter).
func TestCaptureSagaReplayIsIdempotent(t *testing.T) {
	c, adapter, payer, collections, _ := newFundedDevLedger(t)
	wf := money.CaptureSaga(adapter)
	in := money.CaptureInput{
		PaymentID: "pay-dup", PayerAccountID: payer.String(),
		CollectionsAccount: collections.String(), AmountKobo: 5_000, Ledger: 1,
	}
	if _, err := wf(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if _, err := wf(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	bal, _, _ := c.Balance(payer)
	if bal.DebitsPosted != 5_000 {
		t.Fatalf("payer debits_posted=%d want 5000 after replay", bal.DebitsPosted)
	}
}

// Adapter errors surface (not swallowed) when the ledger rejects a leg,
// e.g. an unknown account.
func TestCaptureSagaUnknownAccountFails(t *testing.T) {
	_, adapter, payer, collections, _ := newFundedDevLedger(t)
	wf := money.CaptureSaga(adapter)
	ghost := moneyID(t, "acct:ghost")
	_, err := wf(context.Background(), money.CaptureInput{
		PaymentID: "pay-bad", PayerAccountID: payer.String(),
		CollectionsAccount: ghost.String(), AmountKobo: 1_000, Ledger: 1,
	})
	if err == nil {
		t.Fatal("expected failure for unknown account")
	}
	_ = collections
}

// Registration on the dev runner wires CaptureSaga + RefundWorkflow (and
// RemittanceWorkflow only when MarkRemitted is bound — here it is not).
func TestMoneyRegisterDevRunner(t *testing.T) {
	_, adapter, _, _, _ := newFundedDevLedger(t)
	runner, err := money.NewRunnerFromEnv() // TEMPORAL_URL unset -> inproc
	if err != nil {
		t.Fatal(err)
	}
	money.Register(runner, money.Deps{Ledger: adapter})
	names := runner.WorkflowNames()
	joined := strings.Join(names, ",")
	for _, want := range []string{"CaptureSaga", "RefundWorkflow"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("registered=%v missing %s", names, want)
		}
	}
	if strings.Contains(joined, "RemittanceWorkflow") {
		t.Fatalf("RemittanceWorkflow must not register without MarkRemitted: %v", names)
	}
}
