"""F2 end-to-end funds flow tests (audit Flow 2 + W4 coverage).

Runs LAST (zz_) because it mutates module ledger state shared with other
suites. Real funds movement is asserted on the in-process TB-semantics
ledger: pending hold -> post, compensation voids, sweeper resume, idempotent
replay never double-paying, and the manual-approve endpoint executing the
SAME refund workflow.
"""
from __future__ import annotations

import os

os.environ.setdefault("AUTH_MODE", "dev")
os.environ.setdefault("DATA_DIR", "/tmp/settlement-test-zz-fundsflow")

import shutil  # noqa: E402

from fastapi.testclient import TestClient  # noqa: E402

shutil.rmtree(os.environ["DATA_DIR"], ignore_errors=True)

from app.main import app, _store, _executor  # noqa: E402
from app.refund_execution import refund_id, treasury_account, taxpayer_account  # noqa: E402

c = TestClient(app)
H = {"X-Dev-Role": "operator"}


def _auto_approvable(tin, amount=300_000_000, period="2026-07"):
    """Server-side profile making the refund auto-approvable."""
    _store.put("taxpayer_credit_profiles", tin, {
        "tin_hash": tin, "credit_score": 800,
        "filings_on_time": 12, "filings_total": 12})
    # R4 S1a#3: destination bound to the original payment source.
    from app.refund_execution import payment_source_key, taxpayer_account
    _store.put("payment_sources", payment_source_key(tin, period, "vat"), {
        "tin_hash": tin, "period": period, "tax_type": "vat",
        "account_id": taxpayer_account(tin), "source": "test"})
    return {"tin_hash": tin, "amount_kobo": amount, "tax_type": "vat",
            "period": period}


def _posted_to(tin):
    acct = _executor.ledger.accounts.get(taxpayer_account(tin))
    return acct["credits_posted"] if acct else 0


def test_approved_refund_moves_money_pending_to_post():
    req = _auto_approvable("tin-flow")
    r = c.post("/v1/refunds/fasttrack", headers=H, json=req)
    assert r.status_code == 200, r.text
    doc = r.json()
    assert doc["lane"] == "auto_approve", doc
    exe = doc["execution"]
    assert exe["status"] == "posted", exe
    assert exe["pending_transfer_id"] != exe["post_transfer_id"]
    # real funds movement on the ledger
    assert _posted_to("tin-flow") == req["amount_kobo"]
    t = _executor.ledger.get_transfer(exe["post_transfer_id"])
    assert t and t["debit"] == treasury_account() and t["credit"] == taxpayer_account("tin-flow")


def test_idempotent_replay_never_double_pays():
    req = _auto_approvable("tin-dupe")
    r1 = c.post("/v1/refunds/fasttrack", headers=H, json=req)
    r2 = c.post("/v1/refunds/fasttrack", headers=H, json=req)
    assert r1.status_code == r2.status_code == 200
    assert r2.json()["idempotent_replay"] is True
    assert r1.json()["execution"]["post_transfer_id"] == r2.json()["execution"]["post_transfer_id"]
    assert _posted_to("tin-dupe") == req["amount_kobo"]  # exactly one post


def test_502_then_retry_executes_same_refund_once():
    """funds-flow #3: first call fails during execution -> 502; retry must
    execute the SAME refund to completion (not replay a decision that never
    moved money), and still exactly one post on the ledger."""
    req = _auto_approvable("tin-retry")
    original = _executor.ledger.post_pending_as
    calls = {"n": 0}
    def flaky(*a, **k):
        calls["n"] += 1
        if calls["n"] == 1:
            raise ConnectionError("simulated ledger failure on first post")
        return original(*a, **k)
    _executor.ledger.post_pending_as = flaky
    try:
        r1 = c.post("/v1/refunds/fasttrack", headers=H, json=req)
        assert r1.status_code in (500, 502), r1.text
    finally:
        _executor.ledger.post_pending_as = original
    r2 = c.post("/v1/refunds/fasttrack", headers=H, json=req)
    assert r2.status_code == 200, r2.text
    assert r2.json()["execution"]["status"] == "posted"
    assert _posted_to("tin-retry") == req["amount_kobo"]


def test_post_failure_compensates_void():
    """W4: a failing post voids the pending hold (compensation), the
    execution is marked post_failed, and NO money reaches the taxpayer."""
    req = _auto_approvable("tin-void", amount=10_000_000)
    original = _executor.ledger.post_pending_as
    def boom(*a, **k):
        raise ConnectionError("ledger post deadlock")
    _executor.ledger.post_pending_as = boom
    _auto_approvable(tin="tin-void")  # seeds the payment-source binding
    try:
        import pytest
        with pytest.raises(ConnectionError):
            _executor.execute(tin_hash="tin-void", period="2026-07", tax_type="vat",
                              amount_kobo=10_000_000,
                              decision={"lane": "auto_approve"}, approved_by="test")
    finally:
        _executor.ledger.post_pending_as = original
    rec = _store.get("refund_executions", refund_id("tin-void", "2026-07", "vat"))
    assert rec["status"] == "post_failed", rec
    pend = _executor.ledger.get_transfer(rec["pending_transfer_id"])
    assert pend is None or not pend.get("pending") or pend.get("resolved")
    assert _posted_to("tin-void") == 0


def test_crash_after_pending_sweep_resumes():
    """W4: crash between pending and post -> sweeper resumes the SAME
    execution (no re-decision, no double pay)."""
    doc = {"lane": "auto_approve"}
    _auto_approvable(tin="tin-crash")  # seeds the payment-source binding
    exe = _executor.execute(tin_hash="tin-crash", period="2026-07", tax_type="vat",
                            amount_kobo=10_000_000, decision=doc, approved_by="test")
    assert exe["status"] == "posted"
    # simulate a second execution stranded in pending by hand
    rid2 = refund_id("tin-crash2", "2026-07", "vat")
    tre, tax = treasury_account(), taxpayer_account("tin-crash2")
    from app.refund_execution import deterministic_id
    _executor.ledger.ensure_account(tax, 1, 0, "refund-taxpayer")
    pend_id = deterministic_id("ref-pend:" + rid2)
    _executor.ledger.create_pending({"id": pend_id, "debit": tre, "credit": tax,
                                     "amount_kobo": 5_000_000, "code": 1,
                                     "timeout_seconds": 1800})
    _store.put("refund_executions", rid2, {
        "refund_id": rid2, "tin_hash": "tin-crash2", "period": "2026-07",
        "tax_type": "vat", "amount_kobo": 5_000_000, "status": "pending",
        "pending_transfer_id": pend_id,
        "post_transfer_id": deterministic_id("ref-post:" + rid2)})
    out = _executor.sweep_pending()
    assert out["resumed"] >= 1, out
    rec = _store.get("refund_executions", rid2)
    assert rec["status"] == "posted", rec
    assert _posted_to("tin-crash2") == 5_000_000


def test_manual_approve_endpoint_executes():
    with TestClient(app) as c:
        # seed a manual_review decision (>₦5m auto cap, good profile)
        req = _auto_approvable("tin-manual", amount=600_000_000, period="2026-08")
        r = c.post("/v1/refunds/fasttrack", headers=H, json=req)
        assert r.json()["lane"] == "manual_review", r.json()
        rid = r.json()["refund_id"]
        # R4: maker!=checker — a different principal must approve.
        checker = {"X-Dev-Role": "operator", "X-Dev-Sub": "checker-1"}
        r2 = c.post(f"/v1/refunds/{rid}/approve", headers=checker)
        assert r2.status_code == 200, r2.text
        assert r2.json()["execution"]["status"] == "posted"
        # second approve replays (no double pay)
        r3 = c.post(f"/v1/refunds/{rid}/approve", headers=checker)
        assert r3.status_code == 200, r3.text
        assert _posted_to("tin-manual") == req["amount_kobo"]


def test_unknown_refund_approve_404():
    r = c.post("/v1/refunds/ref-does-not-exist/approve", headers=H)
    assert r.status_code == 404
