package tb

import "testing"

// Regression test for the real-TigerBeetle gate defect: the authorise path
// creates pending transfers with code=1 (CodeAuthorise), but the capture
// path posted them with code=2 (CodeCapture). A real cluster rejects that
// with PENDING_TRANSFER_HAS_DIFFERENT_CODE; the DevClient used to mask it.
//
// These tests pin the invariant: post/void MUST resolve under the
// originating pending transfer's code, and a mismatched explicit code MUST
// be rejected by every LedgerClient implementation.

func TestPostPendingReusesOriginatingCode(t *testing.T) {
	c := NewDevClient()
	src := mkAcct(t, c, 200, 1, FlagDebitsMustNotExceedCredits)
	dst := mkAcct(t, c, 200, 2, 0)
	f := mkAcct(t, c, 200, 3, 0)
	if r, _ := c.Transfer(Transfer{ID: MakeID(0, 1), DebitAccountID: f, CreditAccountID: src,
		Amount: 10_000, Ledger: 200, Code: CodeTopup}); r.Code != OK {
		t.Fatal(r.Code)
	}
	pend := Transfer{ID: MakeID(0, 11), DebitAccountID: src, CreditAccountID: dst,
		Amount: 4_000, Ledger: 200, Code: CodeAuthorise}
	if r, _ := c.PendingTransfer(pend); r.Code != OK {
		t.Fatalf("authorise: %s", r.Code)
	}
	// capture with code=0 (auto): must succeed and reuse the authorise code
	if r, _ := c.PostPending(pend.ID, 0, 0); r.Code != OK {
		t.Fatalf("post with reused code: %s", r.Code)
	}
	posted, res, err := c.GetTransfer(pend.ID)
	if err != nil || res.Code != OK {
		t.Fatalf("lookup: %+v %v", res, err)
	}
	if posted.Code != CodeAuthorise {
		t.Fatalf("post must carry the pending transfer's code %d, got %d", CodeAuthorise, posted.Code)
	}
	if posted.Resolved || posted.Pending {
		t.Fatalf("posted resolution must not remain pending: %+v", posted)
	}
}

func TestVoidPendingReusesOriginatingCode(t *testing.T) {
	c := NewDevClient()
	src := mkAcct(t, c, 200, 1, FlagDebitsMustNotExceedCredits)
	dst := mkAcct(t, c, 200, 2, 0)
	f := mkAcct(t, c, 200, 3, 0)
	if r, _ := c.Transfer(Transfer{ID: MakeID(0, 1), DebitAccountID: f, CreditAccountID: src,
		Amount: 10_000, Ledger: 200, Code: CodeTopup}); r.Code != OK {
		t.Fatal(r.Code)
	}
	pend := Transfer{ID: MakeID(0, 21), DebitAccountID: src, CreditAccountID: dst,
		Amount: 4_000, Ledger: 200, Code: CodeAuthorise}
	if r, _ := c.PendingTransfer(pend); r.Code != OK {
		t.Fatalf("authorise: %s", r.Code)
	}
	if r, _ := c.VoidPending(pend.ID, 0); r.Code != OK {
		t.Fatalf("void with reused code: %s", r.Code)
	}
	voided, res, err := c.GetTransfer(pend.ID)
	if err != nil || res.Code != OK {
		t.Fatalf("lookup: %+v %v", res, err)
	}
	if voided.Code != CodeAuthorise {
		t.Fatalf("void must carry the pending transfer's code %d, got %d", CodeAuthorise, voided.Code)
	}
	if !voided.Resolved || voided.Pending {
		t.Fatalf("void resolution must be resolved and non-pending: %+v", voided)
	}
}

func TestPostVoidRejectMismatchedCode(t *testing.T) {
	c := NewDevClient()
	src := mkAcct(t, c, 200, 1, FlagDebitsMustNotExceedCredits)
	dst := mkAcct(t, c, 200, 2, 0)
	f := mkAcct(t, c, 200, 3, 0)
	if r, _ := c.Transfer(Transfer{ID: MakeID(0, 1), DebitAccountID: f, CreditAccountID: src,
		Amount: 10_000, Ledger: 200, Code: CodeTopup}); r.Code != OK {
		t.Fatal(r.Code)
	}
	mkPend := func(serial uint64) Transfer {
		p := Transfer{ID: MakeID(0, serial), DebitAccountID: src, CreditAccountID: dst,
			Amount: 1_000, Ledger: 200, Code: CodeAuthorise}
		if r, _ := c.PendingTransfer(p); r.Code != OK {
			t.Fatalf("authorise %d: %s", serial, r.Code)
		}
		return p
	}
	// post with code=2 against a code=1 pending: rejected, reservation intact
	p1 := mkPend(31)
	if r, _ := c.PostPending(p1.ID, 0, CodeCapture); r.Code != PendingTransferHasDifferentAttr {
		t.Fatalf("mismatched post must be rejected, got %s", r.Code)
	}
	bal, _, _ := c.Balance(src)
	if bal.DebitsPending != 1_000 {
		t.Fatalf("rejected post must leave the reservation intact: %+v", bal)
	}
	// void with code=3 against a code=1 pending: rejected, reservation intact
	if r, _ := c.VoidPending(p1.ID, CodeVoid); r.Code != PendingTransferHasDifferentAttr {
		t.Fatalf("mismatched void must be rejected, got %s", r.Code)
	}
	// matching explicit code is accepted (authorise code 1)
	p2 := mkPend(32)
	if r, _ := c.PostPending(p2.ID, 0, CodeAuthorise); r.Code != OK {
		t.Fatalf("post with the matching code must succeed, got %s", r.Code)
	}
	p3 := mkPend(33)
	if r, _ := c.VoidPending(p3.ID, CodeAuthorise); r.Code != OK {
		t.Fatalf("void with the matching code must succeed, got %s", r.Code)
	}
}
