"""B3 #1 regression: refund lane inputs are server-side only.

Before: the FastTrackRequest accepted caller-supplied credit_score /
filings_on_time / filings_total / prior_breaks, so ANY caller could force
auto_approve for a refund under ₦5m — real money movement on self-certified
trust inputs. After: the schema rejects those fields (extra=forbid) and the
lane is computed from platform-side credit/compliance stores.
"""
from __future__ import annotations

import os

os.environ.setdefault("AUTH_MODE", "dev")
os.environ.setdefault("DATA_DIR", "/tmp/settlement-test-b3-serverside")

import shutil  # noqa: E402

from fastapi.testclient import TestClient  # noqa: E402

shutil.rmtree(os.environ["DATA_DIR"], ignore_errors=True)

from app.main import app, _store  # noqa: E402

H = {"X-Dev-Role": "operator"}


def test_caller_supplied_trust_inputs_rejected():
    with TestClient(app) as c:
        for field, value in (("credit_score", 999), ("filings_on_time", 99),
                             ("filings_total", 99), ("prior_breaks", 0)):
            r = c.post("/v1/refunds/fasttrack", headers=H, json={
                "tin_hash": "tin-b3", "amount_kobo": 100_000, field: value})
            assert r.status_code == 422, (field, r.text)


def test_lane_from_server_side_profile():
    _store.put("taxpayer_credit_profiles", "tin-good", {
        "tin_hash": "tin-good", "credit_score": 800,
        "filings_on_time": 12, "filings_total": 12})
    # R4 S1a#3: bound original payment source required for auto execution.
    from app.refund_execution import payment_source_key, taxpayer_account
    _store.put("payment_sources", payment_source_key("tin-good", "*", None), {
        "tin_hash": "tin-good", "period": "*", "tax_type": None,
        "account_id": taxpayer_account("tin-good"), "source": "test"})
    with TestClient(app) as c:
        r = c.post("/v1/refunds/fasttrack", headers=H, json={
            "tin_hash": "tin-good", "amount_kobo": 100_000_000})
        assert r.status_code == 200, r.text
        assert r.json()["lane"] == "auto_approve"


def test_no_profile_fails_closed():
    with TestClient(app) as c:
        r = c.post("/v1/refunds/fasttrack", headers=H, json={
            "tin_hash": "tin-unknown", "amount_kobo": 100_000_000})
        assert r.status_code == 200, r.text
        assert r.json()["lane"] == "manual_review"
