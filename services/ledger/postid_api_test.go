package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/munisp/meridian-core-platform/services/ledger/internal/tb"
)

// FF-3 regression (HTTP API): POST /v1/transfers/{id}/post must honor the
// caller-supplied post_id, and GET /v1/transfers/{id} must resolve it.
// Before the fix postReq had no post_id field (silently ignored) and no
// single-transfer GET route existed, so the settlement refund sweeper's
// get_transfer(post_transfer_id) always returned None and the sweep marked
// a POSTED refund "voided".

func setupPostIDServer(t *testing.T) (*server, tb.Transfer) {
	t.Helper()
	dev := tb.NewDevClient()
	s := &server{client: dev, dev: dev, dir: t.TempDir(), thresh: newThresholdTracker()}
	mk := func(id int64, flags uint16) tb.ID {
		res, err := dev.CreateAccounts([]tb.Account{{
			ID: tb.MakeID(0, uint64(id)), Ledger: 700, Code: 1, Flags: flags,
		}})
		if err != nil || res[0].Code != tb.OK {
			t.Fatalf("account %d: %+v %v", id, res, err)
		}
		return tb.MakeID(0, uint64(id))
	}
	src := mk(11, tb.FlagDebitsMustNotExceedCredits)
	dst := mk(12, 0)
	f := mk(13, 0)
	if r, _ := dev.Transfer(tb.Transfer{ID: tb.MakeID(0, 1), DebitAccountID: f, CreditAccountID: src,
		Amount: 10_000, Ledger: 700, Code: tb.CodeTopup}); r.Code != tb.OK {
		t.Fatalf("topup: %s", r.Code)
	}
	pend := tb.Transfer{ID: tb.MakeID(0, 500), DebitAccountID: src, CreditAccountID: dst,
		Amount: 4_000, Ledger: 700, Code: tb.CodeAuthorise}
	if r, _ := dev.PendingTransfer(pend); r.Code != tb.OK {
		t.Fatalf("pending: %s", r.Code)
	}
	return s, pend
}

func TestPostPendingHonorsPostID(t *testing.T) {
	s, pend := setupPostIDServer(t)
	postID := tb.MakeID(0, 900)

	// post with post_id
	body, _ := json.Marshal(map[string]any{"post_id": postID.String(), "amount_kobo": 4_000})
	req := httptest.NewRequest("POST", "/v1/transfers/"+pend.ID.String()+"/post", strings.NewReader(string(body)))
	req.SetPathValue("id", pend.ID.String())
	rec := httptest.NewRecorder()
	s.postPending(rec, req)
	if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("post: %d %s", rec.Code, rec.Body.String())
	}

	// GET the post by its durable id
	greq := httptest.NewRequest("GET", "/v1/transfers/"+postID.String(), nil)
	greq.SetPathValue("id", postID.String())
	grec := httptest.NewRecorder()
	s.getTransfer(grec, greq)
	if grec.Code != http.StatusOK {
		t.Fatalf("get post by post_id: %d %s (pre-fix: 404 — post unreachable)", grec.Code, grec.Body.String())
	}
	var tr tb.Transfer
	if err := json.Unmarshal(grec.Body.Bytes(), &tr); err != nil {
		t.Fatal(err)
	}
	if tr.Pending || tr.PendingID != pend.ID {
		t.Fatalf("bad post record: %+v", tr)
	}

	// pending shows resolved-but-still-pending-flagged (distinguishes posted
	// from voided for the settlement sweeper)
	preq := httptest.NewRequest("GET", "/v1/transfers/"+pend.ID.String(), nil)
	preq.SetPathValue("id", pend.ID.String())
	prec := httptest.NewRecorder()
	s.getTransfer(prec, preq)
	if prec.Code != http.StatusOK {
		t.Fatalf("get pending: %d", prec.Code)
	}
	var pt tb.Transfer
	if err := json.Unmarshal(prec.Body.Bytes(), &pt); err != nil {
		t.Fatal(err)
	}
	if !pt.Pending || !pt.Resolved {
		t.Fatalf("pending must read pending=true resolved=true after post: %+v", pt)
	}

	// replay: same post_id again must be idempotent, not double-post
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("POST", "/v1/transfers/"+pend.ID.String()+"/post", strings.NewReader(string(body)))
	req2.SetPathValue("id", pend.ID.String())
	s.postPending(rec2, req2)
	if rec2.Code != http.StatusOK && rec2.Code != http.StatusCreated {
		t.Fatalf("idempotent replay: %d %s", rec2.Code, rec2.Body.String())
	}
}

func TestGetTransferNotFound(t *testing.T) {
	s, _ := setupPostIDServer(t)
	req := httptest.NewRequest("GET", "/v1/transfers/"+tb.MakeID(0, 424242).String(), nil)
	req.SetPathValue("id", tb.MakeID(0, 424242).String())
	rec := httptest.NewRecorder()
	s.getTransfer(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}
