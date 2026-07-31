"""feature-store tests: materialise aggregations, kobo int64 handling (A9),
online/batch reads, RFC7807 errors."""
import os
import sys
import tempfile
from pathlib import Path

os.environ["DATA_DIR"] = tempfile.mkdtemp()
sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from fastapi.testclient import TestClient  # noqa: E402

from app.main import app  # noqa: E402

H = {"X-Dev-Role": "operator"}


def test_materialise_and_online():
    with TestClient(app) as c:
        r = c.post("/v1/features/materialise", headers=H, json={
            "feature": {"name": "fv_filing_divergence_30d", "entity_key": "pseudo_tin",
                        "value_field": "divergence_kobo", "agg": "sum", "window_days": 30},
            "source_records": [
                {"pseudo_tin": "p1", "divergence_kobo": 150, "ts": 1000000000},
                {"pseudo_tin": "p1", "divergence_kobo": 250, "ts": 1000000100},
                {"pseudo_tin": "p2", "divergence_kobo": 99999999999, "ts": 1000000200},
            ]})
        assert r.status_code == 200, r.text
        assert r.json()["kobo"] is True
        r = c.get("/v1/features/online/p1/fv_filing_divergence_30d", headers=H)
        # kobo features are int64 (A9): integer payload, never float64
        assert r.json()["value_kobo"] == 400 and isinstance(r.json()["value_kobo"], int)

        # last wins by ts
        r = c.post("/v1/features/materialise", headers=H, json={
            "feature": {"name": "fv_last_div", "entity_key": "pseudo_tin",
                        "value_field": "divergence_kobo", "agg": "last"},
            "source_records": [
                {"pseudo_tin": "p1", "divergence_kobo": 100, "ts": 1000},
                {"pseudo_tin": "p1", "divergence_kobo": 300, "ts": 2000},
            ]})
        assert r.status_code == 200
        r = c.get("/v1/features/online/p1/fv_last_div", headers=H)
        assert r.json()["value_kobo"] == 300  # latest ts


def test_batch_read():
    with TestClient(app) as c:
        c.post("/v1/features/materialise", headers=H, json={
            "feature": {"name": "fv_filing_divergence_30d", "entity_key": "pseudo_tin",
                        "value_field": "divergence_kobo", "agg": "sum"},
            "source_records": [{"pseudo_tin": "p1", "divergence_kobo": 400, "ts": 1000}]})
        r = c.post("/v1/features/batch", headers=H,
                   json={"entities": ["p1", "p3"], "features": ["fv_filing_divergence_30d"]})
        assert r.status_code == 200
        feats = r.json()["features"]
        assert feats["p1"]["fv_filing_divergence_30d"] == 400
        assert feats["p3"]["fv_filing_divergence_30d"] is None
        # kobo float rejection (A9): floats in _kobo fields are refused
        r = c.post("/v1/features/materialise", headers=H, json={
            "feature": {"name": "fv_bad", "entity_key": "pseudo_tin",
                        "value_field": "divergence_kobo", "agg": "sum"},
            "source_records": [{"pseudo_tin": "p1", "divergence_kobo": 1.5, "ts": 3000}]})
        assert r.status_code == 422
        assert r.headers["content-type"].startswith("application/problem+json")
        # non-kobo feature still uses double column
        r = c.post("/v1/features/materialise", headers=H, json={
            "feature": {"name": "fv_ratio", "entity_key": "pseudo_tin",
                        "value_field": "ratio", "agg": "avg"},
            "source_records": [{"pseudo_tin": "p1", "ratio": 0.5, "ts": 1000},
                               {"pseudo_tin": "p1", "ratio": 1.5, "ts": 1001}]})
        assert r.status_code == 200 and r.json()["kobo"] is False
        r = c.get("/v1/features/online/p1/fv_ratio", headers=H)
        assert r.json()["value"] == 1.0
