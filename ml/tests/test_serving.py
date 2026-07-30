"""Serving-tier tests: fake registry manifest + tiny random weights written
in the .pt.b64 format (torch.save of a small state_dict, then base64).
Fixtures live ONLY under pytest tmp_path — never under ml/registry/."""
import base64
import io
import json
import os
import sys

import pytest

torch = pytest.importorskip("torch")
fastapi = pytest.importorskip("fastapi")
from fastapi.testclient import TestClient  # noqa: E402

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from serving.main import create_app  # noqa: E402


def _write_model(registry_dir, name, version, in_dim=10, out_dim=1, hidden=8, baseline=False):
    torch.manual_seed(42)
    sd = {
        "0.weight": torch.randn(hidden, in_dim),
        "0.bias": torch.randn(hidden),
        "2.weight": torch.randn(out_dim, hidden),
        "2.bias": torch.randn(out_dim),
    }
    buf = io.BytesIO()
    torch.save(sd, buf)
    weights_rel = f"weights/{name}-{version}.pt.b64"
    weights_abs = os.path.join(registry_dir, weights_rel)
    os.makedirs(os.path.dirname(weights_abs), exist_ok=True)
    with open(weights_abs, "wb") as fh:
        fh.write(base64.b64encode(buf.getvalue()))

    entry = {"weights": weights_rel}
    if baseline:
        base_rel = f"baseline/{name}-{version}.json"
        base_abs = os.path.join(registry_dir, base_rel)
        os.makedirs(os.path.dirname(base_abs), exist_ok=True)
        stats = {f"feature_{i}": {"bins": [j for j in range(11)],
                                  "counts": [10] * 10} for i in range(in_dim)}
        with open(base_abs, "w") as fh:
            json.dump(stats, fh)
        entry["baseline_stats"] = base_rel
    return entry


@pytest.fixture()
def registry_dir(tmp_path):
    d = str(tmp_path / "registry")
    os.makedirs(d)
    manifest = {}
    for name in ("fraud", "credit", "graph"):
        manifest[name] = {"active": "v1", "versions": {"v1": _write_model(d, name, "v1", baseline=True)}}
    # anomaly deliberately has NO weights -> hot-skip 503
    manifest["anomaly"] = {"active": "v1", "versions": {"v1": {"weights": "weights/missing.pt.b64"}}}
    with open(os.path.join(d, "manifest.json"), "w") as fh:
        json.dump(manifest, fh)
    return d


@pytest.fixture()
def client(registry_dir):
    return TestClient(create_app(registry_dir))


def _features():
    return [0.1 * i for i in range(10)]


def test_healthz_readyz(client):
    assert client.get("/healthz").status_code == 200
    r = client.get("/readyz")
    assert r.status_code == 200
    assert r.json()["models"]["fraud"]["active_version"] == "v1"


def test_score_single(client):
    r = client.post("/v1/score/fraud",
                    json={"entity_id": "tin-hash-abc", "features": _features(),
                          "amount_kobo": 1500000},
                    headers={"X-Dev-Role": "operator"})
    assert r.status_code == 200
    body = r.json()
    assert body["model"] == "fraud"
    assert body["version"] == "v1"
    assert 0.0 <= body["score"] <= 1.0
    assert body["amount_kobo"] == 1500000  # integer kobo preserved
    assert isinstance(body["amount_kobo"], int)


def test_score_batch(client):
    recs = [{"entity_id": f"e{i}", "features": _features()} for i in range(4)]
    r = client.post("/v1/score/credit", json={"records": recs})
    assert r.status_code == 200
    body = r.json()
    assert body["count"] == 4
    assert all("score" in x for x in body["results"])


def test_missing_weights_hot_skip_503(client):
    r = client.post("/v1/score/anomaly", json={"features": _features()})
    assert r.status_code == 503
    assert r.headers["content-type"].startswith("application/problem+json")
    body = r.json()
    assert body["status"] == 503
    # service still alive afterwards
    assert client.get("/healthz").status_code == 200


def test_unknown_model_404(client):
    r = client.post("/v1/score/nope", json={"features": _features()})
    assert r.status_code == 404


def test_fusion_uses_available_components(client):
    r = client.post("/v1/score/fusion", json={"features": _features()})
    assert r.status_code == 200
    assert 0.0 <= r.json()["score"] <= 1.0


def test_ab_metrics_endpoint(client):
    client.post("/v1/score/fraud", json={"entity_id": "e1", "features": _features()})
    r = client.get("/v1/ab/metrics")
    assert r.status_code == 200
    body = r.json()
    assert body["arms"]["champion"]["requests"] >= 1


def test_shadow_ab(client, monkeypatch):
    monkeypatch.setenv("ML_AB_CHALLENGER_VERSION", "v1")
    monkeypatch.setenv("ML_AB_CHALLENGER_PCT", "100")
    monkeypatch.setenv("ML_AB_SHADOW", "true")
    # rebuild router config from env on a fresh app
    app = create_app(client.app.state.store.registry.registry_dir)
    c = TestClient(app)
    r = c.post("/v1/score/fraud", json={"entity_id": "sticky-entity", "features": _features()})
    assert r.status_code == 200
    assert r.json()["arm"] == "champion"  # shadow: champion always served
    m = c.get("/v1/ab/metrics").json()
    assert m["shadow"] is True
    assert m["arms"]["challenger"]["shadowed"] >= 1


def test_monitoring_endpoints(client):
    for _ in range(3):
        client.post("/v1/score/fraud", json={"features": _features()})
    assert client.get("/v1/monitoring/performance").status_code == 200
    assert client.get("/v1/monitoring/drift").status_code == 200
    assert client.get("/v1/monitoring/alerts").status_code == 200


def test_dev_jwt_auth(client):
    import hashlib
    import hmac
    import time

    def b64(b):
        return base64.urlsafe_b64encode(b).rstrip(b"=").decode()

    secret = os.environ.get("MERIDIAN_DEV_JWT_SECRET", "meridian-dev-secret")
    header = b64(json.dumps({"alg": "HS256", "typ": "JWT"}).encode())
    payload = b64(json.dumps({"sub": "svc", "role": "service",
                              "exp": time.time() + 60}).encode())
    sig = b64(hmac.new(secret.encode(), f"{header}.{payload}".encode(),
                       hashlib.sha256).digest())
    r = client.post("/v1/score/fraud", json={"features": _features()},
                    headers={"Authorization": f"Bearer {header}.{payload}.{sig}"})
    assert r.status_code == 200
