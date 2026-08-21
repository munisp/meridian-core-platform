package tb

import "testing"

// FF-3 regression: callers that durably persist a post id before the crash
// window (settlement refund executor) must be able to post under that id and
// resolve it later via GetTransfer. Before this, the HTTP API silently
// ignored post_id and the post was unreachable by its durable id.

func postPendingAsSetup(t *testing.T) (*DevClient, Transfer) {
	t.Helper()
	c := NewDevClient()
	src := mkAcct(t, c, 200, 1, FlagDebitsMustNotExceedCredits)
	dst := mkAcct(t, c, 200, 2, 0)
	f := mkAcct(t, c, 200, 3, 0)
	if r, _ := c.Transfer(Transfer{ID: MakeID(0, 1), DebitAccountID: f, CreditAccountID: src,
		Amount: 10_000, Ledger: 200, Code: CodeTopup}); r.Code != OK {
		t.Fatal(r.Code)
	}
	pend := Transfer{ID: MakeID(0, 31), DebitAccountID: src, CreditAccountID: dst,
		Amount: 4_000, Ledger: 200, Code: CodeAuthorise}
	if r, _ := c.PendingTransfer(pend); r.Code != OK {
		t.Fatalf("pending: %s", r.Code)
	}
	return c, pend
}

func TestPostPendingAsRecordsUnderSuppliedID(t *testing.T) {
	c, pend := postPendingAsSetup(t)
	postID := MakeID(0, 99)
	if r, _ := c.PostPendingAs(pend.ID, postID, 0, 0); r.Code != OK {
		t.Fatalf("post as: %s %s", r.Code, r.Message)
	}
	post, res, err := c.GetTransfer(postID)
	if err != nil || res.Code != OK {
		t.Fatalf("post lookup by supplied id failed: %+v %v", res, err)
	}
	if post.Pending || post.PendingID != pend.ID || post.Amount != 4_000 || post.Code != CodeAuthorise {
		t.Fatalf("bad post record: %+v", post)
	}
	// pending remains visible, marked resolved, still flagged pending so a
	// caller can distinguish posted (pending=true, resolved=true) from
	// voided (pending=false, resolved=true)
	pt, res, err := c.GetTransfer(pend.ID)
	if err != nil || res.Code != OK {
		t.Fatalf("pending lookup: %+v %v", res, err)
	}
	if !pt.Pending || !pt.Resolved {
		t.Fatalf("pending must be resolved but still pending-flagged: %+v", pt)
	}
	// balances moved
	bal, _, _ := c.Balance(pend.DebitAccountID)
	if bal.DebitsPosted != 4_000 || bal.DebitsPending != 0 {
		t.Fatalf("bad balance: %+v", bal)
	}
}

func TestPostPendingAsIdempotentReplay(t *testing.T) {
	c, pend := postPendingAsSetup(t)
	postID := MakeID(0, 99)
	if r, _ := c.PostPendingAs(pend.ID, postID, 2_500, 0); r.Code != OK {
		t.Fatalf("post as: %s", r.Code)
	}
	// same args again -> OK, no double posting
	if r, _ := c.PostPendingAs(pend.ID, postID, 2_500, 0); r.Code != OK {
		t.Fatalf("replay: %s", r.Code)
	}
	bal, _, _ := c.Balance(pend.DebitAccountID)
	if bal.DebitsPosted != 2_500 {
		t.Fatalf("double post detected: %+v", bal)
	}
}

func TestPostPendingAsRejectsCodeMismatchAndIDReuse(t *testing.T) {
	c, pend := postPendingAsSetup(t)
	if r, _ := c.PostPendingAs(pend.ID, MakeID(0, 98), 0, CodeCapture); r.Code != PendingTransferHasDifferentAttr {
		t.Fatalf("code mismatch must be rejected, got %s", r.Code)
	}
	// postID colliding with an unrelated existing transfer is rejected
	if r, _ := c.PostPendingAs(pend.ID, MakeID(0, 1), 0, 0); r.Code != Exists {
		t.Fatalf("foreign id reuse must be rejected, got %s", r.Code)
	}
	// pending must still be unresolved and postable
	if r, _ := c.PostPendingAs(pend.ID, MakeID(0, 97), 0, 0); r.Code != OK {
		t.Fatalf("post after rejected attempts: %s", r.Code)
	}
}
