"""I3 refund fast-track lane tests."""
import os
import sys
import tempfile
from pathlib import Path

os.environ["DATA_DIR"] = tempfile.mkdtemp()
sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from fastapi.testclient import TestClient  # noqa: E402

from app.main import app  # noqa: E402
from app.refund import decide_refund_lane  # noqa: E402

H = {"X-Dev-Role": "operator"}


def test_auto_approve_lane():
    doc = decide_refund_lane("tin-h", 300_000_000, 780, 11, 12, prior_breaks=0)
    assert doc["lane"] == "auto_approve"
    assert doc["compliance_ratio"] > 0.9
    assert doc["amount_kobo"] == 300_000_000  # integer kobo


def test_manual_review_lane_on_amount_cap():
    # ₦12m: above the ₦5m auto cap, within the ₦20m review cap, good score
    doc = decide_refund_lane("tin-h", 1_200_000_000, 800, 12, 12)
    assert doc["lane"] == "manual_review"
    assert any("auto cap" in r for r in doc["reasons"])


def test_manual_review_on_open_breaks():
    doc = decide_refund_lane("tin-h", 100_000_000, 800, 12, 12, prior_breaks=2)
    assert doc["lane"] == "manual_review"
    assert any("breaks" in r for r in doc["reasons"])


def test_standard_lane_low_score_and_big_amount():
    assert decide_refund_lane("tin-h", 100_000_000, 400, 12, 12)["lane"] == "standard"
    assert decide_refund_lane("tin-h", 5_000_000_000, 900, 12, 12)["lane"] == "standard"


def test_endpoint_and_manual_review_event():
    from app.main import _store
    # B3 #1: credit/filing inputs are server-side only; seed the profile store.
    _store.put("taxpayer_credit_profiles", "tin-x", {
        "tin_hash": "tin-x", "credit_score": 800,
        "filings_on_time": 12, "filings_total": 12})
    _store.put("taxpayer_credit_profiles", "tin-y", {
        "tin_hash": "tin-y", "credit_score": 750,
        "filings_on_time": 10, "filings_total": 10})
    with TestClient(app) as c:
        r = c.post("/v1/refunds/fasttrack", headers=H, json={
            "tin_hash": "tin-x", "amount_kobo": 1_200_000_000, "tax_type": "vat"})
        assert r.status_code == 200, r.text
        assert r.json()["lane"] == "manual_review"
        r = c.post("/v1/refunds/fasttrack", headers=H, json={
            "tin_hash": "tin-y", "amount_kobo": 100_000_000})
        assert r.json()["lane"] == "auto_approve"
