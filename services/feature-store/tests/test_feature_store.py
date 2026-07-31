import os, sys, tempfile
from pathlib import Path
os.environ["DATA_DIR"] = tempfile.mkdtemp()
sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from fastapi.testclient import TestClient
from app.main import app

H = {"X-Dev-Role": "operator"}

def test_materialise_and_online():
    with TestClient(app) as c:
        recs = [
            {"pseudo_tin": "p1", "divergence_kobo": 100, "ts": 1000},
            {"pseudo_tin": "p1", "divergence_kobo": 300, "ts": 2000},
            {"pseudo_tin": "p2", "divergence_kobo": 50, "ts": 1500},
        ]
        r = c.post("/v1/features/materialise", headers=H, json={
            "feature": {"name": "fv_filing_divergence_30d", "entity_key": "pseudo_tin",
                        "value_field": "divergence_kobo", "agg": "sum"},
            "source_records": recs})
        assert r.status_code == 200, r.text
        assert r.json()["entities_written"] == 2
        r = c.get("/v1/features/online/p1/fv_filing_divergence_30d", headers=H)
        # kobo features are int64 (A9): integer payload, never float64
        assert r.json()["value_kobo"] == 400 and isinstance(r.json()["value_kobo"], int)
        # last agg
        c.post("/v1/features/materialise", headers=H, json={
            "feature": {"name": "fv_last_div", "entity_key": "pseudo_tin",
                        "value_field": "divergence_kobo", "agg": "last"},
            "source_records": recs})
        r = c.get("/v1/features/online/p1/fv_last_div", headers=H)
        assert r.json()["value_kobo"] == 300  # latest ts
        # batch
        r = c.post("/v1/features/batch", headers=H, json={
            "entities": ["p1", "p2", "p3"], "features": ["fv_filing_divergence_30d"]})
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
        # count + window
        r = c.post("/v1/features/materialise", headers=H, json={
            "feature": {"name": "fv_cnt", "entity_key": "pseudo_tin", "agg": "count"},
            "source_records": recs})
        assert r.json()["entities_written"] == 2

def test_auth_and_404():
    with TestClient(app) as c:
        assert c.get("/v1/features/online/x/y").status_code in (401, 422)
        assert c.get("/v1/features/online/x/y", headers=H).status_code == 404
        r = c.get("/healthz"); assert r.json()["service"] == "feature-store"
