"""Unsupervised autoencoder for anomaly detection (reconstruction error = anomaly score).

Trained on legitimate transactions only; fraud should reconstruct poorly.
"""
from __future__ import annotations

import torch
import torch.nn as nn

from ..data.synthetic import N_FEATURES


class FraudAutoencoder(nn.Module):
    def __init__(self, n_features: int = N_FEATURES, hidden: tuple[int, ...] = (32, 16, 8)):
        super().__init__()
        enc, prev = [], n_features
        for h in hidden:
            enc += [nn.Linear(prev, h), nn.ReLU()]
            prev = h
        self.encoder = nn.Sequential(*enc)
        dec = []
        for h in reversed(hidden[:-1]):
            dec += [nn.Linear(prev, h), nn.ReLU()]
            prev = h
        dec.append(nn.Linear(prev, n_features))
        self.decoder = nn.Sequential(*dec)

    def forward(self, x: torch.Tensor) -> torch.Tensor:
        return self.decoder(self.encoder(x))

    @torch.no_grad()
    def anomaly_score(self, x: torch.Tensor) -> torch.Tensor:
        """Mean squared reconstruction error per row (higher = more anomalous)."""
        rec = self.forward(x)
        return ((rec - x) ** 2).mean(dim=1)
