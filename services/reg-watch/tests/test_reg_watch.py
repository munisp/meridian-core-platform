import os, sys, tempfile
from pathlib import Path
os.environ["DATA_DIR"] = tempfile.mkdtemp()
sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from fastapi.testclient import TestClient
from app.main import app

OP = {"X-Dev-Role": "operator"}
BOARD = {"X-Dev-Role": "board"}

def test_gates_seeded():
    with TestClient(app) as c:
        r = c.get("/v1/gates", headers=OP)
        gates = {g["id"]: g for g in r.json()["gates"]}
        for gid in ("g1_ctcs_confirmed", "g2_rivers_case", "g8_presumptive_reg",
                    "carf.transmit_enabled", "qdmtt_upgrade"):
            assert gid in gates, gid
        assert gates["g1_ctcs_confirmed"]["state"] == "armed"

def test_flip_requires_board_and_ref():
    with TestClient(app) as c:
        r = c.post("/v1/gates/g8_presumptive_reg/flip", headers=OP,
                   json={"state": "disarmed", "reason": "reg gazetted", "authorization_ref": "BM-2026-014"})
        assert r.status_code == 403
        r = c.post("/v1/gates/g8_presumptive_reg/flip", headers=BOARD,
                   json={"state": "disarmed", "reason": "reg gazetted", "authorization_ref": ""})
        assert r.status_code == 422
        r = c.post("/v1/gates/g8_presumptive_reg/flip", headers=BOARD,
                   json={"state": "disarmed", "reason": "reg gazetted", "authorization_ref": "BM-2026-014"})
        assert r.status_code == 200
        assert r.json()["gate"]["state"] == "disarmed"
        assert r.json()["gate"]["flipped_by"] == "dev-board"
        # no-op flip
        r = c.post("/v1/gates/g8_presumptive_reg/flip", headers=BOARD,
                   json={"state": "disarmed", "reason": "again", "authorization_ref": "BM-2026-015"})
        assert "no-op" in r.json()["note"]
        # unknown gate
        assert c.post("/v1/gates/nope/flip", headers=BOARD,
                      json={"state": "armed", "reason": "x", "authorization_ref": "r"}).status_code == 404

def test_gazette_watch():
    with TestClient(app) as c:
        r = c.get("/v1/gazette-watch", headers=OP)
        assert len(r.json()["sources"]) >= 2
        src = r.json()["sources"][0]["source"]
        r = c.post("/v1/gazette-watch/findings", headers=OP,
                   json={"source": src, "finding": "WHT regs gazetted", "reference": "Gazette No. 91"})
        assert r.status_code == 200
        assert r.json()["source"]["findings"][0]["finding"] == "WHT regs gazetted"
