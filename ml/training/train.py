"""Train all Meridian ML models on synthetic (or platform) data.

Usage: python training/train.py --model {fraud_mlp,fraud_autoencoder,credit,gnn,fusion,mcmc,all}
       [--epochs N] [--small]

Real training loops; time-based train/val/test split; metrics AUC / PR-AUC /
recall@k; weights + manifest registered via ml/registry/registry.py;
metrics.json written to ml/registry/metrics.json.
"""
from __future__ import annotations

import argparse
import json
import sys
import time
from pathlib import Path

import numpy as np
import torch
import torch.nn as nn

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))  # repo root so `ml.*` imports work

from ml.data import synthetic  # noqa: E402
from ml.models.credit_score import CreditScoreModel  # noqa: E402
from ml.models.fraud_autoencoder import FraudAutoencoder  # noqa: E402
from ml.models.fraud_mlp import FraudMLP  # noqa: E402
from ml.models.fraudfusion import FraudFusion, rule_scores  # noqa: E402
from ml.models.gnn_gcn import GCNRingDetector  # noqa: E402
from ml.models import mcmc  # noqa: E402
from ml.registry import registry  # noqa: E402
from ml.training.common import auc, load_frame, pr_auc, recall_at_k, split_by_time, standardize, xy  # noqa: E402

DEVICE = "cpu"
METRICS_PATH = Path(__file__).resolve().parents[1] / "registry" / "metrics.json"


def _window(df) -> str:
    d = df["date"].astype(str)
    return f"{d.min()}..{d.max()}"


# ---------------------------------------------------------------- fraud MLP
def train_fraud_mlp(df, epochs: int, seed: int = 0):
    torch.manual_seed(seed)
    tr, va, te = split_by_time(df)
    (xtr, xva, xte), mu, sd = standardize(tr, va, te)
    ytr, yva, yte = tr["label"].to_numpy(np.float32), va["label"].to_numpy(np.float32), te["label"].to_numpy(np.float32)
    model = FraudMLP()
    pos_w = torch.tensor(float(max((ytr == 0).sum(), 1) / max((ytr == 1).sum(), 1)))
    opt = torch.optim.Adam(model.parameters(), lr=1e-3, weight_decay=1e-5)
    lossf = nn.BCEWithLogitsLoss(pos_weight=pos_w)
    Xtr, Ytr = torch.tensor(xtr), torch.tensor(ytr)
    best_auc, best_state = -1.0, None
    for ep in range(epochs):
        model.train()
        perm = torch.randperm(len(Xtr))
        for i in range(0, len(Xtr), 4096):
            idx = perm[i:i + 4096]
            opt.zero_grad()
            loss = lossf(model(Xtr[idx]), Ytr[idx])
            loss.backward()
            opt.step()
        model.eval()
        with torch.no_grad():
            va_auc = auc(model.score(torch.tensor(xva)).numpy(), yva)
        if va_auc > best_auc:
            best_auc, best_state = va_auc, {k: v.clone() for k, v in model.state_dict().items()}
        if ep % 5 == 0 or ep == epochs - 1:
            print(f"  [mlp] epoch {ep} loss={loss.item():.4f} val_auc={va_auc:.4f}")
    model.load_state_dict(best_state)
    model.eval()
    with torch.no_grad():
        s = model.score(torch.tensor(xte)).numpy()
    metrics = {"auc": auc(s, yte), "pr_auc": pr_auc(s, yte), "recall_at_5pct": recall_at_k(s, yte)}
    registry.register("fraud_mlp", {"state_dict": model.state_dict(), "scaler": {"mu": mu, "sd": sd},
                                    "n_features": len(synthetic.FEATURES)},
                      metrics, _window(df))
    return model, metrics, {"xte": xte, "yte": yte, "test": te, "scaler": (mu, sd)}


# ------------------------------------------------------------- autoencoder
def train_autoencoder(df, epochs: int, seed: int = 0):
    torch.manual_seed(seed)
    tr, va, te = split_by_time(df)
    (xtr, xva, xte), mu, sd = standardize(tr, va, te)
    xtr_legit = xtr[tr["label"].to_numpy() == 0]
    model = FraudAutoencoder()
    opt = torch.optim.Adam(model.parameters(), lr=1e-3)
    lossf = nn.MSELoss()
    X = torch.tensor(xtr_legit)
    for ep in range(epochs):
        model.train()
        perm = torch.randperm(len(X))
        for i in range(0, len(X), 4096):
            idx = perm[i:i + 4096]
            opt.zero_grad()
            loss = lossf(model(X[idx]), X[idx])
            loss.backward()
            opt.step()
        if ep % 5 == 0 or ep == epochs - 1:
            print(f"  [ae] epoch {ep} recon_loss={loss.item():.5f}")
    model.eval()
    with torch.no_grad():
        s = model.anomaly_score(torch.tensor(xte)).numpy()
    yte = te["label"].to_numpy()
    # separation: mean fraud score / mean legit score
    sep = float(s[yte == 1].mean() / max(s[yte == 0].mean(), 1e-9))
    metrics = {"auc": auc(s, yte), "pr_auc": pr_auc(s, yte), "recall_at_5pct": recall_at_k(s, yte),
               "anomaly_separation": sep}
    lo, hi = float(np.quantile(model.anomaly_score(torch.tensor(xtr_legit)).numpy(), 0.01)), \
             float(np.quantile(model.anomaly_score(torch.tensor(xtr_legit)).numpy(), 0.99))
    registry.register("fraud_autoencoder", {"state_dict": model.state_dict(), "scaler": {"mu": mu, "sd": sd},
                                            "ae_lo": lo, "ae_hi": hi},
                      metrics, _window(df))
    return model, metrics, {"xte": xte, "yte": yte, "test": te, "ae_lo": lo, "ae_hi": hi, "scaler": (mu, sd)}


# ---------------------------------------------------------------- credit
def train_credit(df, epochs: int, seed: int = 0):
    """Entity-level credit model. Default label proxy: entity involved in fraud,
    or extremely thin/erratic activity (low count + long gaps)."""
    torch.manual_seed(seed)
    g = df.groupby("entity").agg(
        n=("tx_id", "size"), total=("amount_kobo", "sum"), maxa=("amount_kobo", "max"),
        night=("is_night", "mean"), gap=("days_since_prev_tx", "mean"),
        newcp=("is_new_counterparty", "mean"), vat=("vat_rate", "mean"),
        fraud=("label", "max"), ch_agent=("ch_agent", "mean"), ch_ussd=("ch_ussd", "mean"),
    ).reset_index()
    g["default"] = ((g["fraud"] == 1) | ((g["n"] < 5) & (g["gap"] > 0.5))).astype(int)
    feats = ["n", "total", "maxa", "night", "gap", "newcp", "vat", "ch_agent", "ch_ussd"]
    X = g[feats].copy()
    X["n"] = np.log1p(X["n"]); X["total"] = np.log1p(X["total"] / 100); X["maxa"] = np.log1p(X["maxa"] / 100)
    mu, sd = X.mean(), X.std().replace(0, 1e-6)
    Xn = ((X - mu) / sd).to_numpy(np.float32)
    y = g["default"].to_numpy(np.float32)
    rng = np.random.default_rng(seed)
    perm = rng.permutation(len(Xn))
    ntr = int(0.7 * len(Xn))
    tri, tei = perm[:ntr], perm[ntr:]
    model = CreditScoreModel(n_features=len(feats), n_stages=4)
    opt = torch.optim.Adam(model.parameters(), lr=5e-3, weight_decay=1e-5)
    bce, mse = nn.BCEWithLogitsLoss(), nn.MSELoss()
    Xtr, Ytr = torch.tensor(Xn[tri]), torch.tensor(y[tri])
    score_target = torch.tensor((1 - y[tri]) * 600 + 300, dtype=torch.float32)  # 300..900 target
    for ep in range(epochs):
        model.train()
        opt.zero_grad()
        logit, reg = model(Xtr)
        loss = bce(logit, Ytr) + 1e-4 * mse(reg, score_target)
        loss.backward()
        opt.step()
        if ep % 10 == 0 or ep == epochs - 1:
            print(f"  [credit] epoch {ep} loss={loss.item():.4f}")
    model.eval()
    with torch.no_grad():
        p = model.default_probability(torch.tensor(Xn[tei])).numpy()
    metrics = {"auc": auc(p, y[tei]), "pr_auc": pr_auc(p, y[tei]), "default_rate": float(y.mean())}
    registry.register("credit_score", {"state_dict": model.state_dict(), "features": feats,
                                       "scaler": {"mu": mu.to_dict(), "sd": sd.to_dict()}},
                      metrics, _window(df))
    return model, metrics, {}


# -------------------------------------------------------------------- GNN
def train_gnn(graph, epochs: int, seed: int = 0):
    torch.manual_seed(seed)
    x = torch.tensor(graph["x"]); adj = torch.tensor(graph["adj"]); y = torch.tensor(graph["y"])
    n = graph["n_nodes"]
    rng = np.random.default_rng(seed)
    idx = rng.permutation(n)
    ntr = int(0.7 * n)
    tr_mask = torch.zeros(n, dtype=torch.bool); tr_mask[idx[:ntr]] = True
    te_mask = ~tr_mask
    model = GCNRingDetector(in_dim=x.shape[1])
    opt = torch.optim.Adam(model.parameters(), lr=1e-2, weight_decay=1e-5)
    w = torch.tensor([1.0, float(max((y[tr_mask] == 0).sum(), 1) / max((y[tr_mask] == 1).sum(), 1))])
    lossf = nn.CrossEntropyLoss(weight=w)
    for ep in range(epochs):
        model.train()
        opt.zero_grad()
        out = model(x, adj)
        loss = lossf(out[tr_mask], y[tr_mask])
        loss.backward()
        opt.step()
        if ep % 10 == 0 or ep == epochs - 1:
            print(f"  [gnn] epoch {ep} loss={loss.item():.4f}")
    model.eval()
    with torch.no_grad():
        p = model.ring_probability(x, adj).numpy()
    # pick decision threshold maximizing F1 on the train split
    ytr_np, ptr = y.numpy()[tr_mask.numpy()], p[tr_mask.numpy()]
    best_t, best_f1 = 0.5, -1.0
    for t in np.linspace(0.05, 0.95, 19):
        pr = (ptr >= t).astype(int)
        tp = int(((pr == 1) & (ytr_np == 1)).sum())
        prec = tp / max(int((pr == 1).sum()), 1)
        rec = tp / max(int((ytr_np == 1).sum()), 1)
        f1 = 2 * prec * rec / max(prec + rec, 1e-9)
        if f1 > best_f1:
            best_f1, best_t = f1, float(t)
    yte, pte = y.numpy()[te_mask.numpy()], p[te_mask.numpy()]
    pred = (pte >= best_t).astype(int)
    tp = int(((pred == 1) & (yte == 1)).sum())
    precision = tp / max(int((pred == 1).sum()), 1)
    recall = tp / max(int((yte == 1).sum()), 1)
    metrics = {"ring_precision": float(precision), "ring_recall": float(recall),
               "auc": auc(pte, yte), "threshold": best_t, "n_ring_nodes_test": int((yte == 1).sum())}
    registry.register("gnn_gcn", {"state_dict": model.state_dict(), "in_dim": int(x.shape[1]),
                                  "threshold": best_t},
                      metrics, "graph")
    return model, metrics, {"graph": graph}


# ------------------------------------------------------------------ fusion
def train_fusion(df, mlp, ae, gnn, mlp_pack, ae_pack, graph, epochs: int = 200):
    tr, va, te = split_by_time(df)
    (xtr, xva, xte), _, _ = standardize(tr, va, te, )  # note: fit on tr again (same stats)
    # reuse each model's own scaler for correctness
    mu_m, sd_m = mlp_pack["scaler"]
    mu_a, sd_a = ae_pack["scaler"]
    def scale(d, mu, sd):
        return ((d[synthetic.FEATURES] - mu) / sd).to_numpy(np.float32)
    xva_m, xte_m = scale(va, mu_m, sd_m), scale(te, mu_m, sd_m)
    xva_a, xte_a = scale(va, mu_a, sd_a), scale(te, mu_a, sd_a)

    fusion = FraudFusion(mlp, ae, gnn)
    fusion.set_graph(torch.tensor(graph["x"]))
    fusion.ae_lo = torch.tensor(ae_pack["ae_lo"]); fusion.ae_hi = torch.tensor(ae_pack["ae_hi"])
    adj = torch.tensor(graph["adj"])

    def streams(d, xm, xa):
        r = torch.tensor(rule_scores(d), dtype=torch.float32)
        node_idx = torch.tensor(d["entity"].to_numpy())
        with torch.no_grad():
            s_mlp = mlp.score(torch.tensor(xm))
            ae_raw = ae.anomaly_score(torch.tensor(xa))
            s_ae = ((ae_raw - fusion.ae_lo) / (fusion.ae_hi - fusion.ae_lo).clamp(min=1e-6)).clamp(0, 1)
            s_gnn = gnn.ring_probability(fusion.gnn_x, adj)[node_idx]
        return torch.stack([s_mlp, s_ae, s_gnn, r], dim=1)

    sva = streams(va, xva_m, xva_a)
    yva = torch.tensor(va["label"].to_numpy(np.float32))
    weights = fusion.fit_weights(sva, yva, epochs=epochs)
    ste = streams(te, xte_m, xte_a)
    fused = fusion.forward(ste).detach().numpy()
    yte = te["label"].to_numpy()
    # best single stream on test
    singles = {name: auc(ste[:, i].numpy(), yte) for i, name in enumerate(FraudFusion.STREAMS)}
    best_single = max(singles.values())
    fa = auc(fused, yte)
    metrics = {"auc": fa, "pr_auc": pr_auc(fused, yte), "recall_at_5pct": recall_at_k(fused, yte),
               "weights": {n: float(w) for n, w in zip(FraudFusion.STREAMS, weights)},
               "single_stream_aucs": singles, "uplift_vs_best_single": float(fa - best_single)}
    pack = {"fusion_logits": fusion.logits.detach().clone(),
            "ae_lo": fusion.ae_lo.clone(), "ae_hi": fusion.ae_hi.clone(),
            "weights": metrics["weights"],
            "components": {"mlp": "fraud_mlp", "ae": "fraud_autoencoder", "gnn": "gnn_gcn"},
            "note": "sub-model weights are registered separately; this pack holds only fusion params"}
    registry.register("fraudfusion", pack, metrics, _window(df))
    return fusion, metrics, {}


# -------------------------------------------------------------------- MCMC
def run_mcmc(df, ae_pack):
    te = ae_pack["test"]; s_te_scores = None
    # recompute AE anomaly scores on the test split
    import torch as _t
    ae = ae_pack["model"]
    with _t.no_grad():
        s_te_scores = ae.anomaly_score(_t.tensor(ae_pack["xte"])).numpy()
    y = te["label"].to_numpy(int)
    rate = mcmc.mh_fraud_rate(int(y.sum()), len(y))
    thr = mcmc.mh_anomaly_threshold(s_te_scores, y)
    summary = {
        "fraud_rate": {"mean": rate.mean, "median": rate.median, "ci90": list(rate.ci90),
                       "acceptance": rate.acceptance_rate, "observed": float(y.mean())},
        "anomaly_threshold": {"mean": thr.mean, "median": thr.median, "ci90": list(thr.ci90),
                              "acceptance": thr.acceptance_rate},
    }
    registry.register("mcmc", {"posterior": summary}, {"acceptance_fraud_rate": rate.acceptance_rate,
                      "fraud_rate_mean": rate.mean}, _window(df))
    return summary


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--model", default="all",
                    choices=["fraud_mlp", "fraud_autoencoder", "credit", "gnn", "fusion", "mcmc", "all"])
    ap.add_argument("--epochs", type=int, default=20)
    ap.add_argument("--small", action="store_true", help="tiny dataset for smoke tests")
    ap.add_argument("--regen", action="store_true", help="regenerate synthetic data")
    args = ap.parse_args()

    torch.set_num_threads(max(1, torch.get_num_threads()))
    t0 = time.time()
    synth_kwargs = {"n_entities": 300, "n_agents": 25, "days": 45, "n_rings": 3} if args.small else \
                   {"n_entities": 1500, "n_agents": 80, "days": 120, "n_rings": 5}
    df, graph = load_frame(force_regen=args.regen, **({} if not args.small else synth_kwargs))
    if args.small:  # force small cache aside
        pass
    print(f"dataset: {len(df)} tx, fraud_rate={df['label'].mean():.4f}, graph nodes={graph['n_nodes']}, "
          f"ring_nodes={int(graph['y'].sum())}")

    results = {}
    want = lambda name: args.model in (name, "all")
    mlp = ae = gnn = None
    mlp_pack = ae_pack = None

    if want("fraud_mlp"):
        print("== fraud_mlp =="); mlp, results["fraud_mlp"], mlp_pack = train_fraud_mlp(df, args.epochs)
    if want("fraud_autoencoder"):
        print("== fraud_autoencoder =="); ae, results["fraud_autoencoder"], ae_pack = train_autoencoder(df, args.epochs)
        ae_pack["model"] = ae
    if want("credit"):
        print("== credit =="); _, results["credit_score"], _ = train_credit(df, args.epochs)
    if want("gnn"):
        print("== gnn =="); gnn, results["gnn_gcn"], _ = train_gnn(graph, max(args.epochs, 150))
    if want("fusion"):
        print("== fusion ==")
        if mlp is None:
            mlp, results["fraud_mlp"], mlp_pack = train_fraud_mlp(df, args.epochs)
        if ae is None:
            ae, results["fraud_autoencoder"], ae_pack = train_autoencoder(df, args.epochs); ae_pack["model"] = ae
        if gnn is None:
            gnn, results["gnn_gcn"], _ = train_gnn(graph, max(args.epochs, 150))
        _, results["fraudfusion"], _ = train_fusion(df, mlp, ae, gnn, mlp_pack, ae_pack, graph)
    if want("mcmc"):
        print("== mcmc ==")
        if ae_pack is None:
            ae, results["fraud_autoencoder"], ae_pack = train_autoencoder(df, args.epochs); ae_pack["model"] = ae
        results["mcmc"] = run_mcmc(df, ae_pack)

    METRICS_PATH.write_text(json.dumps(results, indent=2, default=float))
    print(f"\nmetrics.json -> {METRICS_PATH}  ({time.time()-t0:.1f}s)")
    print(json.dumps(results, indent=2, default=float))


if __name__ == "__main__":
    main()
