package tb

import (
	"testing"
	"time"
)

// Regression test for the DevClient self-deadlock: mutation hooks that call
// back into the client (srv.persist -> Snapshot) previously ran while the
// mutation still held the global c.mu, deadlocking the very first dev-ledger
// write. Hooks now run after the mutex is released, so a re-entrant hook
// must complete.
func TestWriteWithReentrantPersistHookDoesNotDeadlock(t *testing.T) {
	c := NewDevClient()
	var snapshotCalls int
	c.SetHooks(func() {
		// mimics srv.persist: re-enters the client to export state
		_, _, _ = c.Snapshot()
		snapshotCalls++
	}, func(Transfer) {})

	done := make(chan struct{})
	go func() {
		defer close(done)
		dr := MakeID(200, 1)
		cr := MakeID(200, 2)
		if _, err := c.CreateAccounts([]Account{
			{ID: dr, Ledger: 200, Code: 1, Flags: FlagDebitsMustNotExceedCredits},
			{ID: cr, Ledger: 200, Code: 1},
		}); err != nil {
			t.Errorf("create accounts: %v", err)
			return
		}
		if _, err := c.Transfer(Transfer{
			ID: MakeID(200, 100), DebitAccountID: cr, CreditAccountID: dr,
			Amount: 1, Ledger: 200, Code: 4,
		}); err != nil {
			t.Errorf("transfer: %v", err)
		}
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("dev-ledger write with re-entrant persist hook hung (self-deadlock)")
	}
	if snapshotCalls == 0 {
		t.Fatal("onChange hook never ran")
	}
}

// Hooks must observe every mutation exactly once, in order: onChange once
// per mutation, then the mutation's transfer events.
func TestHooksFireAfterUnlockInOrder(t *testing.T) {
	c := NewDevClient()
	var changes int
	var events []Transfer
	c.SetHooks(func() { changes++ }, func(tr Transfer) { events = append(events, tr) })

	dr := MakeID(200, 1)
	cr := MakeID(200, 2)
	if _, err := c.CreateAccounts([]Account{
		{ID: dr, Ledger: 200, Code: 1},
		{ID: cr, Ledger: 200, Code: 1},
	}); err != nil {
		t.Fatal(err)
	}
	if changes != 1 || len(events) != 0 {
		t.Fatalf("after CreateAccounts: changes=%d events=%d", changes, len(events))
	}
	res, err := c.Transfer(Transfer{
		ID: MakeID(200, 100), DebitAccountID: dr, CreditAccountID: cr,
		Amount: 5, Ledger: 200, Code: 4,
	})
	if err != nil || res.Code != OK {
		t.Fatalf("transfer: %v %s", err, res.Code)
	}
	if changes != 2 || len(events) != 1 || events[0].Amount != 5 {
		t.Fatalf("after Transfer: changes=%d events=%v", changes, events)
	}
	// A failed mutation (constraint violation) must not fire hooks.
	if res, _ := c.Transfer(Transfer{
		ID: MakeID(200, 101), DebitAccountID: MakeID(200, 999), CreditAccountID: cr,
		Amount: 5, Ledger: 200, Code: 4,
	}); res.Code == OK {
		t.Fatal("expected failure for missing account")
	}
	if changes != 2 || len(events) != 1 {
		t.Fatalf("failed mutation fired hooks: changes=%d events=%d", changes, len(events))
	}
}
