"""I3 refund fast-track lane — REAL evaluation (no caller-controlled lane).

R2 regression: the refund decision must be computed from request + policy
caps, never from a caller-supplied "lane".
"""
from __future__ import annotations

import os

os.environ.setdefault("AUTH_MODE", "dev")
os.environ.setdefault("DATA_DIR", "/tmp/settlement-test-r2-fasttrack")

import shutil  # noqa: E402

from fastapi.testclient import TestClient  # noqa: E402

shutil.rmtree(os.environ["DATA_DIR"], ignore_errors=True)

from app.main import app, _store  # noqa: E402


def test_decide_refund_lane_unit():
    from app.refund import decide_refund_lane
    doc = decide_refund_lane(tin_hash="t1", amount_kobo=100_000_000,
                             credit_score=780, filings_on_time=12, filings_total=12,
                             prior_breaks=0)
    assert doc["lane"] == "auto_approve"
    assert doc["risk"] == "low"
    # above the auto cap -> manual review
    doc = decide_refund_lane(tin_hash="t1", amount_kobo=600_000_000,
                             credit_score=780, filings_on_time=12, filings_total=12,
                             prior_breaks=0)
    assert doc["lane"] == "manual_review"
    # poor compliance -> manual review
    doc = decide_refund_lane(tin_hash="t1", amount_kobo=100_000_000,
                             credit_score=300, filings_on_time=3, filings_total=12,
                             prior_breaks=0)
    assert doc["lane"] == "manual_review"
    # above the review cap -> standard lane
    doc = decide_refund_lane(tin_hash="t1", amount_kobo=5_000_000_000,
                             credit_score=780, filings_on_time=12, filings_total=12,
                             prior_breaks=0)
    assert doc["lane"] == "standard"


def test_endpoint_and_manual_review_event():
    with TestClient(app) as c:
        h = {"X-Dev-Role": "operator"}
        # B3 #1: lane inputs are server-side; seed the profile store.
        _store.put("taxpayer_credit_profiles", "tin-x", {
            "tin_hash": "tin-x", "credit_score": 780,
            "filings_on_time": 12, "filings_total": 12})
        r = c.post("/v1/refunds/fasttrack", headers=h, json={
            "tin_hash": "tin-x", "amount_kobo": 1_200_000_000,
            "period": "2026-07"})
        assert r.status_code == 200, r.text
        doc = r.json()
        assert doc["lane"] == "manual_review"
        assert doc["credit_score"] == 780
        ev = _store.get("refund_manual_review", doc["refund_id"])
        assert ev is not None and ev["type"] == "nrs.refund.manual_review.v1"
        assert ev["decision"]["refund_id"] == doc["refund_id"]
        # B3 #1: caller-supplied trust inputs are REJECTED, not applied.
        _store.put("taxpayer_credit_profiles", "tin-y", {
            "tin_hash": "tin-y", "credit_score": 780,
            "filings_on_time": 12, "filings_total": 12})
        r = c.post("/v1/refunds/fasttrack", headers=h, json={
            "tin_hash": "tin-y", "amount_kobo": 100_000_000, "credit_score": 999})
        assert r.status_code == 422, r.text
        # R4 S1a#3: auto_approve additionally requires a bound original
        # payment source; seed it for tin-y.
        from app.refund_execution import payment_source_key, taxpayer_account
        _store.put("payment_sources", payment_source_key("tin-y", "*", "vat"), {
            "tin_hash": "tin-y", "period": "*", "tax_type": "vat",
            "account_id": taxpayer_account("tin-y"), "source": "test"})
        r = c.post("/v1/refunds/fasttrack", headers=h, json={
            "tin_hash": "tin-y", "amount_kobo": 100_000_000, "tax_type": "vat"})
        assert r.json()["lane"] == "auto_approve"
        # no profile on record: lane cannot be caller-influenced either
        r = c.post("/v1/refunds/fasttrack", headers=h, json={
            "tin_hash": "tin-attacker", "amount_kobo": 100_000_000,
            "credit_score": 999, "prior_breaks": 0})
        assert r.status_code == 422, r.text
        # absent profile fails closed -> never auto_approve
        r = c.post("/v1/refunds/fasttrack", headers=h, json={
            "tin_hash": "tin-noprofile", "amount_kobo": 100_000_000})
        assert r.json()["lane"] == "manual_review"
