"""Evaluate registry-active models on the held-out test split.

Usage: python training/evaluate.py [--model fraud_mlp|fraud_autoencoder|gnn_gcn|all]
Reloads weights from the registry (never re-trains) and prints metrics.
"""
from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

import numpy as np
import torch

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))

from ml.data import synthetic  # noqa: E402
from ml.models.fraud_autoencoder import FraudAutoencoder  # noqa: E402
from ml.models.fraud_mlp import FraudMLP  # noqa: E402
from ml.models.gnn_gcn import GCNRingDetector  # noqa: E402
from ml.registry import registry  # noqa: E402
from ml.training.common import auc, load_frame, pr_auc, recall_at_k, split_by_time  # noqa: E402


def _scale(df, scaler):
    x = df[list(scaler["mu"].keys())].copy()
    for c in x.columns:
        x[c] = (x[c] - scaler["mu"][c]) / scaler["sd"][c]
    return x.to_numpy(np.float32)


def evaluate(model_name: str) -> dict:
    pack = registry.load_active(model_name)
    version, _ = registry.active(model_name)
    df, graph = load_frame()
    _, _, te = split_by_time(df)
    yte = te["label"].to_numpy(np.float32)

    if model_name == "fraud_mlp":
        m = FraudMLP(); m.load_state_dict(pack["state_dict"]); m.eval()
        with torch.no_grad():
            s = m.score(torch.tensor(_scale(te, pack["scaler"]))).numpy()
    elif model_name == "fraud_autoencoder":
        m = FraudAutoencoder(); m.load_state_dict(pack["state_dict"]); m.eval()
        with torch.no_grad():
            s = m.anomaly_score(torch.tensor(_scale(te, pack["scaler"]))).numpy()
    elif model_name == "gnn_gcn":
        m = GCNRingDetector(in_dim=pack["in_dim"]); m.load_state_dict(pack["state_dict"]); m.eval()
        with torch.no_grad():
            s = m.ring_probability(torch.tensor(graph["x"]), torch.tensor(graph["adj"])).numpy()
        yg = graph["y"]
        return {"version": version, "auc": auc(s, yg),
                "ring_precision_at_threshold": float(((s >= pack.get("threshold", 0.5)) & (yg == 1)).sum() /
                                                     max((s >= pack.get("threshold", 0.5)).sum(), 1))}
    else:
        raise ValueError(model_name)
    return {"version": version, "auc": auc(s, yte), "pr_auc": pr_auc(s, yte),
            "recall_at_5pct": recall_at_k(s, yte)}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--model", default="all")
    args = ap.parse_args()
    names = ["fraud_mlp", "fraud_autoencoder", "gnn_gcn"] if args.model == "all" else [args.model]
    out = {}
    for n in names:
        try:
            out[n] = evaluate(n)
        except FileNotFoundError as e:
            out[n] = {"error": str(e)}
    print(json.dumps(out, indent=2))


if __name__ == "__main__":
    main()
