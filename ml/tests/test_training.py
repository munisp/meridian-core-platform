"""End-to-end tiny training run: generate small synthetic set, train each model
a few epochs, assert weights are saved in the registry and fraud AUC > 0.6.

Run: cd ml && python -m pytest tests/test_training.py   (or: python tests/test_training.py)
"""
from __future__ import annotations

import json
import sys
from pathlib import Path

ML_ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ML_ROOT.parent))

from ml.data import synthetic  # noqa: E402
from ml.registry import registry  # noqa: E402
from ml.training.common import load_frame  # noqa: E402
from ml.training import train as T  # noqa: E402

EPOCHS = 6


def _dataset(cache_dir=None):
    cache = str(cache_dir) if cache_dir else str(ML_ROOT / "data" / "cache_test")
    return load_frame(force_regen=True, cache=cache,
                      n_entities=300, n_agents=25, days=45, n_rings=3, seed=7)


def test_end_to_end(tmp_path):
    # Redirect the file registry into tmp_path so the training run never
    # dirties the real checked-in ml/registry/ (manifest + v2 weights).
    import shutil

    dst = tmp_path / "registry"
    shutil.copytree(ML_ROOT / "registry", dst,
                    ignore=shutil.ignore_patterns("__pycache__"))
    registry.REGISTRY_DIR = dst
    registry.MANIFEST = dst / "manifest.json"
    registry._MANIFEST_CACHE = None
    T.METRICS_PATH = dst / "metrics.json"
    try:
        _run_end_to_end(tmp_path)
    finally:
        registry.REGISTRY_DIR = ML_ROOT / "registry"
        registry.MANIFEST = registry.REGISTRY_DIR / "manifest.json"
        registry._MANIFEST_CACHE = None


def _run_end_to_end(tmp_path):
    df, graph = _dataset(cache_dir=tmp_path / "data_cache")
    assert len(df) > 2000 and df["label"].sum() > 50
    assert set(synthetic.FEATURES) <= set(df.columns)

    mlp, m_mlp, mlp_pack = T.train_fraud_mlp(df, EPOCHS)
    ae, m_ae, ae_pack = T.train_autoencoder(df, EPOCHS)
    ae_pack["model"] = ae
    _, m_credit, _ = T.train_credit(df, EPOCHS)
    gnn, m_gnn, _ = T.train_gnn(graph, 40)
    _, m_fusion, _ = T.train_fusion(df, mlp, ae, gnn, mlp_pack, ae_pack, graph, epochs=100)
    m_mcmc = T.run_mcmc(df, ae_pack)

    # weights saved + manifest updated
    for name in ["fraud_mlp", "fraud_autoencoder", "credit_score", "gnn_gcn", "fraudfusion", "mcmc"]:
        act = registry.active(name)
        assert act is not None, f"{name} not registered"
        path = registry.REGISTRY_DIR / act[1]["path"]
        assert path.exists() and path.suffix == ".b64", f"missing weights for {name}"
        registry.load_weights(path)  # decodes

    # quality gates (fraud task)
    assert m_mlp["auc"] > 0.6, m_mlp
    assert m_ae["auc"] > 0.6, m_ae
    assert m_credit["auc"] > 0.6, m_credit
    assert m_gnn["auc"] > 0.6, m_gnn
    assert m_fusion["auc"] >= min(m_mlp["auc"], m_ae["auc"]) - 0.05
    assert 0.0 < m_mcmc["fraud_rate"]["mean"] < 0.5
    assert m_mcmc["fraud_rate"]["acceptance"] > 0.1

    print(json.dumps({"mlp": m_mlp, "ae": m_ae, "credit": m_credit, "gnn": m_gnn,
                      "fusion_auc": m_fusion["auc"], "mcmc_rate": m_mcmc["fraud_rate"]["mean"]},
                     indent=2, default=float))


if __name__ == "__main__":
    import tempfile

    test_end_to_end(Path(tempfile.mkdtemp()))
    print("PASS")
