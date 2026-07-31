"""Fine-tune: load active registry weights -> continue training on new data.

Usage: python training/finetune.py --model fraud_mlp [--epochs N] [--regen]
Supports fraud_mlp and fraud_autoencoder (the per-transaction torch models).
Registers the fine-tuned model as a NEW version (promotion is a separate step).
"""
from __future__ import annotations

import argparse
import sys
from pathlib import Path

import numpy as np
import torch
import torch.nn as nn

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))

from ml.data import synthetic  # noqa: E402
from ml.models.fraud_autoencoder import FraudAutoencoder  # noqa: E402
from ml.models.fraud_mlp import FraudMLP  # noqa: E402
from ml.registry import registry  # noqa: E402
from ml.training.common import auc, load_frame, pr_auc, recall_at_k, split_by_time  # noqa: E402


def _scale(df, scaler):
    mu, sd = scaler["mu"], scaler["sd"]
    feats = list(mu.keys())
    x = df[feats].copy()
    for c in feats:
        x[c] = (x[c] - mu[c]) / sd[c]
    return x.to_numpy(np.float32)


def finetune(model_name: str, epochs: int, lr: float = 2e-4, seed: int = 1, regen: bool = False):
    version, entry = registry.active(model_name)
    pack = registry.load_active(model_name)
    df, _ = load_frame(force_regen=regen, seed=seed + 100 if regen else 42,
                       n_entities=1500, n_agents=80, days=120, n_rings=5)
    tr, va, te = split_by_time(df)
    xte = _scale(te, pack["scaler"])
    yte = te["label"].to_numpy(np.float32)

    if model_name == "fraud_mlp":
        model = FraudMLP()
        model.load_state_dict(pack["state_dict"])
        ytr = tr["label"].to_numpy(np.float32)
        pos_w = torch.tensor(float(max((ytr == 0).sum(), 1) / max((ytr == 1).sum(), 1)))
        lossf = nn.BCEWithLogitsLoss(pos_weight=pos_w)
        X = torch.tensor(_scale(tr, pack["scaler"])); Y = torch.tensor(ytr)
        score_fn = lambda m, x: m.score(x)
    elif model_name == "fraud_autoencoder":
        model = FraudAutoencoder()
        model.load_state_dict(pack["state_dict"])
        lossf = nn.MSELoss()
        xt = _scale(tr, pack["scaler"])
        X = torch.tensor(xt[tr["label"].to_numpy() == 0]); Y = X
        score_fn = lambda m, x: m.anomaly_score(x)
    else:
        raise ValueError(f"finetune not supported for {model_name}")

    opt = torch.optim.Adam(model.parameters(), lr=lr)
    for ep in range(epochs):
        model.train()
        perm = torch.randperm(len(X))
        for i in range(0, len(X), 4096):
            idx = perm[i:i + 4096]
            opt.zero_grad()
            out = model(X[idx])
            loss = lossf(out, Y[idx]) if model_name == "fraud_autoencoder" else lossf(out, Y[idx])
            loss.backward()
            opt.step()
        print(f"  [finetune:{model_name}] epoch {ep} loss={loss.item():.4f}")
    model.eval()
    with torch.no_grad():
        s = score_fn(model, torch.tensor(xte)).numpy()
    metrics = {"auc": auc(s, yte), "pr_auc": pr_auc(s, yte), "recall_at_5pct": recall_at_k(s, yte),
               "finetuned_from": version}
    pack["state_dict"] = model.state_dict()
    d = df["date"].astype(str)
    new_version = registry.register(model_name, pack, metrics, f"{d.min()}..{d.max()}")
    print(f"registered {model_name} {new_version} (from {version}) metrics={metrics}")
    return new_version, metrics


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--model", required=True, choices=["fraud_mlp", "fraud_autoencoder"])
    ap.add_argument("--epochs", type=int, default=8)
    ap.add_argument("--regen", action="store_true")
    args = ap.parse_args()
    finetune(args.model, args.epochs, regen=args.regen)


if __name__ == "__main__":
    main()
