import os, sys, tempfile
from pathlib import Path
os.environ["DATA_DIR"] = tempfile.mkdtemp()
sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from fastapi.testclient import TestClient
from app.main import app, reconcile, ReconRecord

H = {"X-Dev-Role": "operator"}

def R(ref, amt):
    return ReconRecord(reference=ref, amount_kobo=amt)

def test_reconcile_logic():
    res = reconcile(
        [R("a", 100), R("b", 200), R("c", 300), R("d", 400)],
        [R("a", 100), R("b", 250), R("c", 300)],
        [R("a", 100), R("b", 200), R("c", 300), R("e", 500)],
    )
    assert res["matched"] == 2  # a, c
    kinds = {(b["reference"], b["kind"]) for b in res["breaks"]}
    assert ("b", "amount_mismatch") in kinds
    assert ("d", "missing") in kinds and ("e", "missing") in kinds
    b = next(x for x in res["breaks"] if x["reference"] == "b")
    assert b["amounts_kobo"]["pssp"] == 250 and b["max_delta_kobo"] == 50
    d = next(x for x in res["breaks"] if x["reference"] == "d")
    assert set(d["missing_in"]) == {"pssp", "treasury"}

def test_api_run_and_breaks():
    with TestClient(app) as c:
        r = c.post("/v1/recon/pssp/run", headers=H, json={
            "platform": [{"reference": "r1", "amount_kobo": 1000}],
            "pssp": [{"reference": "r1", "amount_kobo": 1000}],
            "treasury": [{"reference": "r1", "amount_kobo": 900}]})
        assert r.status_code == 200, r.text
        body = r.json()
        assert body["run"]["matched"] == 0 and body["run"]["break_count"] == 1
        r = c.get("/v1/recon/breaks", headers=H)
        assert r.json()["count"] == 1
        bid = r.json()["breaks"][0]["id"]
        r = c.post(f"/v1/recon/breaks/{bid}/resolve", headers=H,
                   json={"resolution": "treasury corrected", "note": "manual"})
        assert r.json()["break"]["status"] == "resolved"
        assert c.get("/v1/recon/breaks?status=open", headers=H).json()["count"] == 0
        assert c.post("/v1/recon/pssp/run", headers=H, json={}).status_code == 400
