"""A8 (dup-reference breaks + idempotency) and I5 (revenue event stream)
tests. RFC7807 error shape (A10) checked too."""
import os
import sys
import tempfile
from pathlib import Path

os.environ["DATA_DIR"] = tempfile.mkdtemp()
sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from fastapi.testclient import TestClient  # noqa: E402

from app.main import app, reconcile, ReconRecord  # noqa: E402

H = {"X-Dev-Role": "operator"}


def R(ref, amt, **meta):
    return ReconRecord(reference=ref, amount_kobo=amt, meta=meta)


def test_duplicate_reference_emits_break():
    res = reconcile(
        [R("a", 100), R("a", 150)],  # dup within platform, differing amounts
        [R("a", 100)],
        [R("a", 100)],
    )
    kinds = [(b["reference"], b["kind"]) for b in res["breaks"]]
    assert ("a", "duplicate_reference") in kinds
    dup = next(b for b in res["breaks"] if b["kind"] == "duplicate_reference")
    assert dup["side"] == "platform"
    assert dup["amounts_kobo"] == {"first": 100, "duplicate": 150}
    # amount_mismatch also fires (kept-first 100 vs 100 vs 100 matches actually)
    assert res["matched"] == 1  # a matches at kept amount across all sides


def test_idempotent_recon_run():
    with TestClient(app) as c:
        payload = {
            "idempotency_key": "run-2024-q1-001",
            "platform": [{"reference": "x1", "amount_kobo": 500}],
            "pssp": [{"reference": "x1", "amount_kobo": 500}],
            "treasury": [{"reference": "x1", "amount_kobo": 400}],
        }
        r1 = c.post("/v1/recon/pssp/run", headers=H, json=payload)
        r2 = c.post("/v1/recon/pssp/run", headers=H, json=payload)
        assert r1.status_code == 200 and r2.status_code == 200
        assert r2.json().get("idempotent_replay") is True
        assert r1.json()["run"]["run_id"] == r2.json()["run"]["run_id"]


def test_revenue_events_and_aggregate():
    with TestClient(app) as c:
        r = c.post("/v1/recon/pssp/run", headers=H, json={
            "platform": [{"reference": "rev1", "amount_kobo": 7500000,
                          "meta": {"tax_type": "vat", "state": "lagos"}}],
            "pssp": [{"reference": "rev1", "amount_kobo": 7500000}],
            "treasury": [{"reference": "rev1", "amount_kobo": 7500000}]})
        assert r.status_code == 200
        assert r.json()["revenue_events_emitted"] == 1
        ev = c.get("/v1/revenue/events", headers=H).json()["events"]
        doc = next(e for e in ev if e["reference"] == "rev1")
        assert doc["type"] == "nrs.revenue.settled.v1"
        assert doc["amount_kobo"] == 7500000 and isinstance(doc["amount_kobo"], int)
        agg = c.get("/v1/revenue/aggregate?group_by=tax_type", headers=H).json()
        vat = next(b for b in agg["buckets"] if b["key"] == "vat")
        assert vat["total_kobo"] == 7500000


def test_rfc7807_on_error():
    with TestClient(app) as c:
        r = c.post("/v1/recon/pssp/run", headers=H, json={})
        assert r.status_code == 400
        assert r.headers["content-type"].startswith("application/problem+json")
        body = r.json()
        assert body["status"] == 400 and "title" in body
