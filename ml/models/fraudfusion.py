"""FraudFusion: weighted late-fusion ensemble of MLP + AE + GNN + rules.

Learned convex combination (softmax weights over 4 score streams) trained on
validation labels. Rule stream is deterministic domain logic (structuring
proximity to the N10m threshold, night-burst, VAT mismatch) so the fusion can
fall back on interpretable signals.
"""
from __future__ import annotations

import numpy as np
import torch
import torch.nn as nn

from .fraud_autoencoder import FraudAutoencoder
from .fraud_mlp import FraudMLP
from .gnn_gcn import GCNRingDetector


def rule_scores(df) -> np.ndarray:
    """Deterministic fraud-rule score in [0,1] from feature/raw columns."""
    amt_naira = df["amount_kobo"].to_numpy(dtype=float) / 100.0
    # proximity to structuring threshold from below (8m..10m naira -> high)
    s_struct = np.clip((amt_naira - 8_000_000) / 2_000_000, 0, 1)
    s_night = df["is_night"].to_numpy(dtype=float) * np.clip(df["amount_vs_p90"].to_numpy(dtype=float) * 4, 0, 1)
    vat = df["vat_rate"].to_numpy(dtype=float) * 0.2
    is_einv = df["ch_einvoice"].to_numpy(dtype=float)
    s_vat = is_einv * np.clip((0.075 - vat) / 0.075, 0, 1)  # einvoice w/ under-declared VAT
    s_dorm = np.clip(df["days_since_prev_tx"].to_numpy(dtype=float) * 2, 0, 1) * np.clip(amt_naira / 5_000_000, 0, 1)
    return np.clip(np.maximum.reduce([s_struct, s_night, s_vat, s_dorm]), 0, 1)


class FraudFusion(nn.Module):
    """Late fusion over (mlp, ae, gnn, rules) score streams with learned weights."""

    STREAMS = ["mlp", "ae", "gnn", "rules"]

    def __init__(self, mlp: FraudMLP | None = None, ae: FraudAutoencoder | None = None,
                 gnn: GCNRingDetector | None = None):
        super().__init__()
        self.mlp = mlp or FraudMLP()
        self.ae = ae or FraudAutoencoder()
        self.gnn = gnn or GCNRingDetector()
        self.logits = nn.Parameter(torch.zeros(len(self.STREAMS)))  # -> softmax weights
        # calibration for the AE stream (recon error -> [0,1]); set during fit
        self.register_buffer("ae_lo", torch.tensor(0.0))
        self.register_buffer("ae_hi", torch.tensor(1.0))
        self.register_buffer("gnn_x", torch.zeros(1, 8))  # graph node features, set via set_graph()

    def set_graph(self, x: torch.Tensor):
        self.gnn_x = x

    @property
    def weights(self) -> torch.Tensor:
        return torch.softmax(self.logits, dim=0)

    def stream_scores(self, x: torch.Tensor, adj: torch.Tensor | None, rules: torch.Tensor,
                      node_index: torch.Tensor | None = None) -> torch.Tensor:
        """Returns (B, 4) per-stream scores in [0,1]."""
        with torch.no_grad():
            s_mlp = self.mlp.score(x)
            ae_raw = self.ae.anomaly_score(x)
            s_ae = ((ae_raw - self.ae_lo) / (self.ae_hi - self.ae_lo).clamp(min=1e-6)).clamp(0, 1)
            if adj is not None:
                p_ring = self.gnn.ring_probability(self.gnn_x, adj)
                s_gnn = p_ring[node_index] if node_index is not None else torch.zeros_like(s_mlp)
            else:
                s_gnn = torch.zeros_like(s_mlp)
        return torch.stack([s_mlp, s_ae, s_gnn, rules], dim=1)

    def forward(self, stream: torch.Tensor) -> torch.Tensor:
        """stream: (B,4) -> fused score (B,)."""
        return (stream * self.weights).sum(dim=1)

    def fit_weights(self, stream: torch.Tensor, y: torch.Tensor, epochs: int = 200, lr: float = 0.05):
        opt = torch.optim.Adam([self.logits], lr=lr)
        lossf = nn.BCELoss()
        for _ in range(epochs):
            opt.zero_grad()
            p = self.forward(stream).clamp(1e-6, 1 - 1e-6)
            loss = lossf(p, y)
            loss.backward()
            opt.step()
        return self.weights.detach().numpy()
