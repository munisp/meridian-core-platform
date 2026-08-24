"""B3 #1 regression: refund fast-track lane must be decided on SERVER-SIDE
credit/compliance data, never on caller-supplied fields.

Pre-fix these tests fail: a caller could self-certify credit_score=1000 and
filings 12/12 and get an auto-executed refund. Post-fix: caller-supplied
trust fields are rejected (422), and without a server-side profile the
decision fails closed to manual_review/standard (never auto_approve).
"""
import os
import sys
import tempfile
from pathlib import Path

os.environ["DATA_DIR"] = tempfile.mkdtemp()
sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from fastapi.testclient import TestClient  # noqa: E402

from app.main import app, _store  # noqa: E402
from app.refund import decide_refund_lane  # noqa: E402

H = {"X-Dev-Role": "operator"}


def test_caller_supplied_credit_fields_rejected():
    """Caller-typed credit_score/filings must NOT be accepted on the
    execute path — the request is rejected outright."""
    with TestClient(app) as c:
        r = c.post("/v1/refunds/fasttrack", headers=H, json={
            "tin_hash": "tin-attacker", "amount_kobo": 499_000_000,
            "credit_score": 1000, "filings_on_time": 99, "filings_total": 99})
        assert r.status_code == 422, r.text


def test_no_server_profile_fails_closed_never_auto_approve():
    """With no server-side credit/compliance profile the lane must fail
    closed: no auto-execution even for a small amount."""
    with TestClient(app) as c:
        r = c.post("/v1/refunds/fasttrack", headers=H, json={
            "tin_hash": "tin-no-profile", "amount_kobo": 100_000_000})
        assert r.status_code == 200, r.text
        body = r.json()
        assert body["lane"] != "auto_approve"
        assert body["lane"] == "manual_review"
        assert "execution" not in body
        assert any("server-side" in reason for reason in body["reasons"])


def test_server_side_profile_enables_auto_approve():
    """When the platform's own store has a strong profile, auto_approve
    works — proving the server-side path is the one consulted."""
    _store.put("taxpayer_credit_profiles", "tin-good", {
        "tin_hash": "tin-good", "credit_score": 800,
        "filings_on_time": 12, "filings_total": 12})
    with TestClient(app) as c:
        r = c.post("/v1/refunds/fasttrack", headers=H, json={
            "tin_hash": "tin-good", "amount_kobo": 100_000_000})
        assert r.status_code == 200, r.text
        assert r.json()["lane"] == "auto_approve"
        assert r.json()["execution"]["status"] == "posted"


def test_server_side_weak_profile_not_auto():
    _store.put("taxpayer_credit_profiles", "tin-weak", {
        "tin_hash": "tin-weak", "credit_score": 400,
        "filings_on_time": 3, "filings_total": 12})
    with TestClient(app) as c:
        r = c.post("/v1/refunds/fasttrack", headers=H, json={
            "tin_hash": "tin-weak", "amount_kobo": 100_000_000})
        assert r.json()["lane"] != "auto_approve"


def test_decide_refund_lane_none_inputs_fail_closed():
    doc = decide_refund_lane("tin-h", 100_000_000, None, None, None)
    assert doc["lane"] == "manual_review"
    assert any("server-side" in r for r in doc["reasons"])
    doc = decide_refund_lane("tin-h", 5_000_000_000, None, None, None)
    assert doc["lane"] == "standard"
