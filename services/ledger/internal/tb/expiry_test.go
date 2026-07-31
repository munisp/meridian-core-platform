package tb

import (
	"testing"
	"time"
)

// TestPendingExpirySweeper proves F7: a pending transfer with a timeout is
// voided by ExpirePendings after its ExpiresAt, the reservation is released,
// and an unexpired pending is untouched.
func TestPendingExpirySweeper(t *testing.T) {
	c := NewDevClient()
	a := MakeID(200, 1)
	b := MakeID(200, 2)
	if _, err := c.CreateAccounts([]Account{{ID: a, Ledger: 200, Code: 1}, {ID: b, Ledger: 200, Code: 1}}); err != nil {
		t.Fatal(err)
	}
	// expired pending (timeout 1s, backdated via short sleep window below)
	p1 := MakeID(200, 1001)
	if res, err := c.PendingTransfer(Transfer{ID: p1, DebitAccountID: a, CreditAccountID: b, Ledger: 200, Code: CodeAuthorise, Amount: 5000, TimeoutSeconds: 1}); err != nil || res.Code != OK {
		t.Fatalf("pending: %+v %v", res, err)
	}
	// pending without timeout: never expires
	p2 := MakeID(200, 1002)
	if res, err := c.PendingTransfer(Transfer{ID: p2, DebitAccountID: a, CreditAccountID: b, Ledger: 200, Code: CodeAuthorise, Amount: 7000}); err != nil || res.Code != OK {
		t.Fatalf("pending: %+v %v", res, err)
	}
	// not yet expired
	if got := c.ExpirePendings(time.Now().UTC()); len(got) != 0 {
		t.Fatalf("nothing should expire yet, got %d", len(got))
	}
	time.Sleep(1100 * time.Millisecond)
	expired := c.ExpirePendings(time.Now().UTC())
	if len(expired) != 1 || expired[0].ID != p1 {
		t.Fatalf("expected exactly p1 to expire, got %+v", expired)
	}
	if expired[0].Code != CodeVoid || expired[0].Pending {
		t.Fatalf("expired transfer must be a void record, got %+v", expired[0])
	}
	// reservation released, nothing posted
	bal, _, _ := c.Balance(b)
	if bal.CreditsPending != 7000 || bal.CreditsPosted != 0 {
		t.Fatalf("reservation must be released; p2 untouched: %+v", bal)
	}
	// expired pending can no longer be posted
	if res, _ := c.PostPending(p1, 0, CodeCapture); res.Code != PendingTransferNotPending {
		t.Fatalf("posting an expired pending must fail, got %+v", res)
	}
	// the no-timeout pending still posts fine
	if res, _ := c.PostPending(p2, 0, CodeCapture); res.Code != OK {
		t.Fatalf("unexpired pending must post, got %+v", res)
	}
	// sweeping again is a no-op
	if got := c.ExpirePendings(time.Now().UTC().Add(time.Hour)); len(got) != 0 {
		t.Fatalf("re-sweep must be a no-op, got %d", len(got))
	}
}

// TestPendingExpiryEventHook: the sweeper emits the transfer event hook so
// the service can publish nrs.ledger.pending_expired.v1.
func TestPendingExpiryEventHook(t *testing.T) {
	c := NewDevClient()
	var events []Transfer
	c.SetHooks(func() {}, func(tr Transfer) { events = append(events, tr) })
	a := MakeID(100, 1)
	b := MakeID(100, 2)
	_, _ = c.CreateAccounts([]Account{{ID: a, Ledger: 100, Code: 1}, {ID: b, Ledger: 100, Code: 1}})
	p1 := MakeID(100, 42)
	if _, err := c.PendingTransfer(Transfer{ID: p1, DebitAccountID: a, CreditAccountID: b, Ledger: 100, Code: CodeHold, Amount: 100, TimeoutSeconds: 1}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)
	if got := c.ExpirePendings(time.Now().UTC()); len(got) != 1 {
		t.Fatalf("expected 1 expiry, got %d", len(got))
	}
	last := events[len(events)-1]
	if last.ID != p1 || last.Code != CodeVoid {
		t.Fatalf("expiry event hook must fire with the void record, got %+v", last)
	}
}
