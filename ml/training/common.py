"""Shared data prep for training / finetune / continual / evaluate."""
from __future__ import annotations

import numpy as np
import pandas as pd

from ..data import pipeline, synthetic


def load_frame(force_regen: bool = False, cache: str | None = None, **synth_kwargs):
    """Load transactions via the pipeline (Postgres if DATABASE_URL, else synthetic).

    Caches the raw frame + graph in ml/data/cache as parquet/npz so repeated
    model runs reuse one dataset.
    """
    from pathlib import Path
    cache_dir = Path(cache) if cache else Path(__file__).resolve().parents[1] / "data" / "cache"
    cache_dir.mkdir(parents=True, exist_ok=True)
    pq, gz = cache_dir / "transactions.parquet", cache_dir / "graph.npz"
    if not force_regen and pq.exists() and gz.exists():
        df = pd.read_parquet(pq)
        g = np.load(gz)
        graph = {"x": g["x"], "edge_index": g["edge_index"], "adj": g["adj"], "y": g["y"],
                 "n_nodes": int(g["n_nodes"])}
        return df, graph
    import os
    if os.environ.get("DATABASE_URL", "").strip():
        df = pipeline.extract_transactions()
        df = synthetic.add_features(df)
        graph = synthetic.build_graph(df, int(max(df["entity"].max(), df["counterparty"].max()) + 1))
    else:
        data = synthetic.generate(**synth_kwargs)
        df, graph = data.transactions, data.graph
    df.to_parquet(pq, index=False)
    np.savez(gz, x=graph["x"], edge_index=graph["edge_index"], adj=graph["adj"],
             y=graph["y"], n_nodes=graph["n_nodes"])
    return df, graph


def split_by_time(df: pd.DataFrame, train=0.6, val=0.2):
    d = pd.to_datetime(df["date"])
    q1, q2 = d.quantile(train), d.quantile(train + val)
    return df[d <= q1].copy(), df[(d > q1) & (d <= q2)].copy(), df[d > q2].copy()


def standardize(train: pd.DataFrame, *others: pd.DataFrame, features=None):
    features = features or list(synthetic.FEATURES)
    mu = train[features].mean()
    sd = train[features].std().replace(0, 1e-6)
    out = []
    for df in (train, *others):
        x = ((df[features] - mu) / sd).to_numpy(dtype=np.float32)
        out.append(x)
    return out, mu.to_dict(), sd.to_dict()


def xy(df, x):
    return x, df["label"].to_numpy(dtype=np.float32)


def auc(scores: np.ndarray, labels: np.ndarray) -> float:
    from sklearn.metrics import roc_auc_score
    labels = np.asarray(labels)
    if len(np.unique(labels)) < 2:
        return float("nan")
    return float(roc_auc_score(labels, scores))


def pr_auc(scores, labels) -> float:
    from sklearn.metrics import average_precision_score
    return float(average_precision_score(labels, scores))


def recall_at_k(scores, labels, k_frac: float = 0.05) -> float:
    """Recall among the top-k% highest-scored transactions."""
    n = len(scores)
    k = max(1, int(n * k_frac))
    idx = np.argsort(-np.asarray(scores))[:k]
    labels = np.asarray(labels)
    denom = max(labels.sum(), 1)
    return float(labels[idx].sum() / denom)
