"""Continual training loop: pull latest platform data via the pipeline ->
fine-tune -> evaluate -> promote to registry if better, else rollback.

Usage: python training/continual.py --model fraud_mlp [--epochs N] [--metric auc]

"Latest platform data" = Postgres extract when DATABASE_URL is set (prod),
otherwise a freshly regenerated synthetic batch with a new seed (dev stand-in
for a new data window). Promotion compares the candidate metric against the
currently active version's stored metric; rollback restores the prior active.
"""
from __future__ import annotations

import argparse
import json
import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))

from ml.registry import registry  # noqa: E402
from ml.training.finetune import finetune  # noqa: E402

METRICS_PATH = Path(__file__).resolve().parents[1] / "registry" / "metrics.json"


def continual(model_name: str, epochs: int, metric: str = "auc") -> dict:
    act = registry.active(model_name)
    if act is None:
        raise SystemExit(f"no active version for {model_name}; run train.py first")
    active_version, active_entry = act
    baseline = float(active_entry["metrics"].get(metric, float("nan")))
    print(f"continual: {model_name} active={active_version} baseline {metric}={baseline:.4f}")

    # pull latest data (pipeline inside finetune; regen simulates a new window in dev)
    new_version, metrics = finetune(model_name, epochs, regen=True)
    candidate = float(metrics.get(metric, float("nan")))
    decision = {"model": model_name, "metric": metric, "baseline": baseline,
                "candidate": candidate, "candidate_version": new_version, "ts": time.time()}
    if candidate >= baseline:
        registry.promote(model_name, new_version)
        decision["action"] = "promoted"
    else:
        # keep current active; drop candidate by rolling manifest back
        registry.rollback(model_name)
        decision["action"] = "rolled_back"
    print(json.dumps(decision, indent=2))
    log_path = Path(__file__).resolve().parents[1] / "registry" / "continual_log.jsonl"
    with log_path.open("a") as f:
        f.write(json.dumps(decision) + "\n")
    return decision


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--model", default="fraud_mlp",
                    choices=["fraud_mlp", "fraud_autoencoder"])
    ap.add_argument("--epochs", type=int, default=8)
    ap.add_argument("--metric", default="auc")
    args = ap.parse_args()
    continual(args.model, args.epochs, args.metric)


if __name__ == "__main__":
    main()
