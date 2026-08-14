"""F-10: pagination bounds on list endpoints.

/v1/recon/breaks, /v1/recon/investigations and /v1/revenue/events accept
limit (default 50, max 500) + offset; out-of-bounds values are rejected 422
by FastAPI query validation instead of silently unbounded.

The suite shares one in-process store, so seeds use a unique status marker
and are removed again — no cross-test pollution.
"""
import os
import sys
import tempfile
from pathlib import Path

os.environ["DATA_DIR"] = tempfile.mkdtemp()
sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

import pytest  # noqa: E402
from fastapi.testclient import TestClient  # noqa: E402

from app.main import app, _store  # noqa: E402

H = {"X-Dev-Role": "operator"}
MARK = "pg-test-open"


@pytest.fixture()
def seeded_breaks():
    ids = [f"pg-{i}" for i in range(60)]
    for i in ids:
        _store.put("breaks", i, {"reference": i, "status": MARK})
    yield ids
    for i in ids:
        _store.delete("breaks", i)


def test_list_breaks_default_limit_and_total(seeded_breaks):
    with TestClient(app) as c:
        r = c.get(f"/v1/recon/breaks?status={MARK}", headers=H)
        assert r.status_code == 200
        body = r.json()
        assert body["limit"] == 50 and body["offset"] == 0
        assert body["count"] == 50
        assert body["total"] == 60  # pre-pagination total is still reported


def test_list_breaks_limit_offset_window(seeded_breaks):
    with TestClient(app) as c:
        q = f"status={MARK}&limit=25"
        p1 = c.get(f"/v1/recon/breaks?{q}&offset=0", headers=H).json()
        p2 = c.get(f"/v1/recon/breaks?{q}&offset=25", headers=H).json()
        p3 = c.get(f"/v1/recon/breaks?{q}&offset=50", headers=H).json()
        assert p1["count"] == 25 and p2["count"] == 25 and p3["count"] == 10
        refs = [{b["reference"] for b in p["breaks"]} for p in (p1, p2, p3)]
        assert not (refs[0] & refs[1] or refs[0] & refs[2] or refs[1] & refs[2])
        assert refs[0] | refs[1] | refs[2] == {f"pg-{i}" for i in range(60)}


def test_pagination_bounds_rejected():
    with TestClient(app) as c:
        for url in ("/v1/recon/breaks?limit=0",
                    "/v1/recon/breaks?limit=501",
                    "/v1/recon/breaks?offset=-1",
                    "/v1/recon/investigations?limit=99999",
                    "/v1/recon/investigations?offset=-5",
                    "/v1/revenue/events?limit=0",
                    "/v1/revenue/events?limit=1000"):
            r = c.get(url, headers=H)
            assert r.status_code == 422, (url, r.status_code)


def test_revenue_events_limit_caps_page():
    ids = [f"ev-pg-{i}" for i in range(55)]
    for i in ids:
        _store.put("revenue_events", i, {"reference": i, "amount_kobo": 1})
    try:
        with TestClient(app) as c:
            body = c.get("/v1/revenue/events", headers=H).json()
            assert len(body["events"]) == 50 and body["total"] >= 55
            big = c.get("/v1/revenue/events?limit=500", headers=H).json()
            assert len(big["events"]) == body["total"]
    finally:
        for i in ids:
            _store.delete("revenue_events", i)
