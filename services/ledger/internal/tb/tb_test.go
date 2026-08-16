package tb

import "testing"

func mkAcct(t *testing.T, c *DevClient, ns, serial uint64, flags uint16) ID {
	t.Helper()
	id := MakeID(ns, serial)
	res, err := c.CreateAccounts([]Account{{ID: id, Ledger: ns, Code: 1, Flags: flags}})
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Code != OK {
		t.Fatalf("create account: %s", res[0].Code)
	}
	return id
}

func TestIDRoundTrip(t *testing.T) {
	id := MakeID(200, 42)
	if id.String() != "00000000000000c8000000000000002a" {
		t.Fatalf("id string = %s", id.String())
	}
	back, err := ParseID(id.String())
	if err != nil || back != id {
		t.Fatalf("round trip: %v %+v", err, back)
	}
	if _, err := ParseID("zz"); err == nil {
		t.Fatal("bad id accepted")
	}
}

func TestDoubleEntryTransfer(t *testing.T) {
	c := NewDevClient()
	a := mkAcct(t, c, LedgerPSMPayments, 1, 0)
	b := mkAcct(t, c, LedgerPSMPayments, 2, 0)
	res, err := c.Transfer(Transfer{ID: MakeID(0, 999), DebitAccountID: b, CreditAccountID: a,
		Amount: 50_000, Ledger: LedgerPSMPayments, Code: CodeSettle})
	if err != nil || res.Code != OK {
		t.Fatalf("transfer: %v %s", err, res.Code)
	}
	bal, _, _ := c.Balance(a)
	if bal.PostedNet != 50_000 {
		t.Fatalf("a net = %d", bal.PostedNet)
	}
	bal, _, _ = c.Balance(b)
	if bal.PostedNet != -50_000 {
		t.Fatalf("b net = %d", bal.PostedNet)
	}
	// conservation: sum of posted nets is zero
}

func TestIdempotentTransfer(t *testing.T) {
	c := NewDevClient()
	a := mkAcct(t, c, 200, 1, 0)
	b := mkAcct(t, c, 200, 2, 0)
	tr := Transfer{ID: MakeID(0, 5), DebitAccountID: a, CreditAccountID: b, Amount: 100, Ledger: 200, Code: CodeTopup}
	if r, _ := c.Transfer(tr); r.Code != OK {
		t.Fatalf("first: %s", r.Code)
	}
	if r, _ := c.Transfer(tr); r.Code != Exists {
		t.Fatalf("replay same attrs: %s", r.Code)
	}
	tr.Amount = 200
	if r, _ := c.Transfer(tr); r.Code != ExistsWithDifferentAttributes {
		t.Fatalf("replay diff attrs: %s", r.Code)
	}
	bal, _, _ := c.Balance(b)
	if bal.CreditsPosted != 100 {
		t.Fatalf("double-applied: %d", bal.CreditsPosted)
	}
}

func TestDebitsMustNotExceedCredits(t *testing.T) {
	c := NewDevClient()
	float := mkAcct(t, c, LedgerAgentFloat, 1, FlagDebitsMustNotExceedCredits)
	agent := mkAcct(t, c, LedgerAgentFloat, 2, 0)
	// topup the float with 1000 kobo
	if r, _ := c.Transfer(Transfer{ID: MakeID(0, 1), DebitAccountID: agent, CreditAccountID: float,
		Amount: 1000, Ledger: LedgerAgentFloat, Code: CodeTopup}); r.Code != OK {
		t.Fatalf("topup: %s", r.Code)
	}
	// payout 600 ok
	if r, _ := c.Transfer(Transfer{ID: MakeID(0, 2), DebitAccountID: float, CreditAccountID: agent,
		Amount: 600, Ledger: LedgerAgentFloat, Code: CodeSettle}); r.Code != OK {
		t.Fatalf("payout within float: %s", r.Code)
	}
	// payout another 600 -> exceeds credits (400 left)
	if r, _ := c.Transfer(Transfer{ID: MakeID(0, 3), DebitAccountID: float, CreditAccountID: agent,
		Amount: 600, Ledger: LedgerAgentFloat, Code: CodeSettle}); r.Code != ExceedsCredits {
		t.Fatalf("want exceeds_credits, got %s", r.Code)
	}
}

func TestPendingLifecycle(t *testing.T) {
	c := NewDevClient()
	src := mkAcct(t, c, 200, 1, FlagDebitsMustNotExceedCredits)
	dst := mkAcct(t, c, 200, 2, 0)
	top := mkAcct(t, c, 200, 3, 0)
	if r, _ := c.Transfer(Transfer{ID: MakeID(0, 10), DebitAccountID: top, CreditAccountID: src,
		Amount: 10_000, Ledger: 200, Code: CodeTopup}); r.Code != OK {
		t.Fatal(r.Code)
	}
	pend := Transfer{ID: MakeID(0, 11), DebitAccountID: src, CreditAccountID: dst,
		Amount: 4_000, Ledger: 200, Code: CodeAuthorise}
	if r, _ := c.PendingTransfer(pend); r.Code != OK {
		t.Fatalf("pending: %s", r.Code)
	}
	bal, _, _ := c.Balance(src)
	if bal.AvailableKobo != 6_000 || bal.DebitsPending != 4_000 {
		t.Fatalf("reservation wrong: %+v", bal)
	}
	// cannot reserve beyond available
	if r, _ := c.PendingTransfer(Transfer{ID: MakeID(0, 12), DebitAccountID: src, CreditAccountID: dst,
		Amount: 7_000, Ledger: 200, Code: CodeAuthorise}); r.Code != ExceedsCredits {
		t.Fatalf("want exceeds_credits on pending, got %s", r.Code)
	}
	// partial post: 3000 of 4000
	if r, _ := c.PostPending(pend.ID, 3_000, 0); r.Code != OK {
		t.Fatalf("post: %s", r.Code)
	}
	bal, _, _ = c.Balance(src)
	if bal.DebitsPosted != 3_000 || bal.DebitsPending != 0 {
		t.Fatalf("post balances: %+v", bal)
	}
	bal, _, _ = c.Balance(dst)
	if bal.CreditsPosted != 3_000 {
		t.Fatalf("dst credits: %+v", bal)
	}
	// double post rejected
	if r, _ := c.PostPending(pend.ID, 0, 0); r.Code == OK {
		t.Fatal("double post accepted")
	}
	// void unknown
	if r, _ := c.VoidPending(MakeID(0, 77), 0); r.Code != PendingTransferNotFound {
		t.Fatalf("void unknown: %s", r.Code)
	}
}

func TestVoidReleasesReservation(t *testing.T) {
	c := NewDevClient()
	a := mkAcct(t, c, 200, 1, FlagDebitsMustNotExceedCredits)
	b := mkAcct(t, c, 200, 2, 0)
	f := mkAcct(t, c, 200, 3, 0)
	c.Transfer(Transfer{ID: MakeID(0, 1), DebitAccountID: f, CreditAccountID: a, Amount: 500, Ledger: 200, Code: CodeTopup})
	p := Transfer{ID: MakeID(0, 2), DebitAccountID: a, CreditAccountID: b, Amount: 400, Ledger: 200, Code: CodeAuthorise}
	if r, _ := c.PendingTransfer(p); r.Code != OK {
		t.Fatal(r.Code)
	}
	if r, _ := c.VoidPending(p.ID, 0); r.Code != OK {
		t.Fatalf("void: %s", r.Code)
	}
	bal, _, _ := c.Balance(a)
	if bal.DebitsPending != 0 || bal.AvailableKobo != 500 {
		t.Fatalf("void release: %+v", bal)
	}
	if r, _ := c.VoidPending(p.ID, 0); r.Code == OK {
		t.Fatal("double void accepted")
	}
}

func TestSnapshotRestore(t *testing.T) {
	c := NewDevClient()
	a := mkAcct(t, c, 200, 1, 0)
	b := mkAcct(t, c, 200, 2, 0)
	c.Transfer(Transfer{ID: MakeID(0, 1), DebitAccountID: a, CreditAccountID: b, Amount: 42, Ledger: 200, Code: CodeSettle})
	accts, trs, sers := c.Snapshot()
	c2 := NewDevClient()
	c2.Restore(accts, trs, sers)
	bal, res, _ := c2.Balance(b)
	if res.Code != OK || bal.CreditsPosted != 42 {
		t.Fatalf("restored balance: %+v %s", bal, res.Code)
	}
}
