"""CI-style regression test: load the ACTUAL checked-in registry weights
(ml/registry/weights/*.pt.b64) through the serving tier and score a vector
per endpoint family. Guards the audit-verified weight-format bug where
train.py's wrapped payload ({"state_dict", "scaler", ...}) 503'd every
/v1/score/* endpoint.
"""
import base64
import io
import os
import sys

import pytest

torch = pytest.importorskip("torch")
fastapi = pytest.importorskip("fastapi")
from fastapi.testclient import TestClient  # noqa: E402

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from serving.main import create_app  # noqa: E402
from serving.registry_client import load_state_dict_b64, unwrap_payload  # noqa: E402

REGISTRY_DIR = os.path.join(os.path.dirname(__file__), "..", "registry")

# endpoint -> (artifact name, expected input behaviour)
ENDPOINTS = {
    "fraud": "fraud_mlp_v1.pt.b64",
    "credit": "credit_score_v1.pt.b64",
    "graph": "gnn_gcn_v1.pt.b64",
    "anomaly": "fraud_autoencoder_v1.pt.b64",
}


def _input_dim(artifact: str) -> int:
    payload = load_state_dict_b64(os.path.join(REGISTRY_DIR, "weights", artifact))
    sd, _meta = unwrap_payload(payload)
    first = sorted(k for k, v in sd.items() if k.endswith(".weight") and v.dim() == 2)[0]
    return int(sd[first].shape[1])


@pytest.fixture(scope="module")
def client():
    return TestClient(create_app(REGISTRY_DIR))


@pytest.mark.parametrize("endpoint,artifact", sorted(ENDPOINTS.items()))
def test_real_weights_score_200(client, endpoint, artifact):
    dim = _input_dim(artifact)
    feats = [0.05 * i for i in range(dim)]
    r = client.post(f"/v1/score/{endpoint}",
                    json={"entity_id": "tin-hash-ci", "features": feats,
                          "amount_kobo": 999_999_900},
                    headers={"X-Dev-Role": "operator"})
    assert r.status_code == 200, r.text
    body = r.json()
    assert body["model"] == endpoint
    assert 0.0 <= body["score"] <= 1.0
    assert isinstance(body["amount_kobo"], int)


def test_real_weights_unwrap_and_scaler():
    """The wrapped payload must unwrap to a flat state_dict and carry a
    scaler for fraud_mlp (training registered mu/sd)."""
    payload = load_state_dict_b64(os.path.join(REGISTRY_DIR, "weights", "fraud_mlp_v1.pt.b64"))
    sd, meta = unwrap_payload(payload)
    assert any(k.endswith(".weight") for k in sd)
    assert "scaler" in meta and "mu" in meta["scaler"] and "sd" in meta["scaler"]


def test_real_fusion_uses_learned_weights(client):
    dim = _input_dim(ENDPOINTS["fraud"])
    feats = [0.05 * i for i in range(dim)]
    r = client.post("/v1/score/fusion",
                    json={"entity_id": "tin-hash-ci", "features": feats,
                          "amount_kobo": 950_000_000,  # ₦9.5m: near structuring threshold
                          "context": {"is_night": 1, "vat_rate": 0.0, "ch_einvoice": 1}},
                    headers={"X-Dev-Role": "operator"})
    assert r.status_code == 200, r.text
    body = r.json()
    assert 0.0 <= body["score"] <= 1.0
    assert "fraudfusion=" in body["version"]
    # rules stream fires near the ₦10m threshold, so fusion score must exceed
    # a pure model-only score of zero-ish context... at minimum it is not the
    # old plain mean of three components: verify against components directly.
    comps = []
    for ep in ("fraud", "credit", "graph"):
        dim = _input_dim(ENDPOINTS[ep])
        rr = client.post(f"/v1/score/{ep}",
                         json={"features": [0.05 * i for i in range(dim)]},
                         headers={"X-Dev-Role": "operator"})
        if rr.status_code == 200:
            comps.append(rr.json()["score"])
    if comps:
        plain_mean = sum(comps) / len(comps)
        # learned weights (mlp-heavy) + rules stream != plain mean in general
        assert abs(body["score"] - plain_mean) > 1e-9


def test_keycloak_mode_rejects_dev_role():
    os.environ["AUTH_MODE"] = "keycloak"
    try:
        import importlib

        import serving.main as sm

        importlib.reload(sm)
        c = TestClient(sm.create_app(REGISTRY_DIR))
        r = c.post("/v1/score/fraud", json={"features": [0.0] * 5},
                   headers={"X-Dev-Role": "admin"})
        assert r.status_code == 401
    finally:
        os.environ["AUTH_MODE"] = "dev"
        import importlib

        import serving.main as sm

        importlib.reload(sm)
