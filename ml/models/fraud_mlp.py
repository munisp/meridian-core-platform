"""PyTorch MLP classifier for transaction fraud (binary)."""
from __future__ import annotations

import torch
import torch.nn as nn

from ..data.synthetic import N_FEATURES


class FraudMLP(nn.Module):
    def __init__(self, n_features: int = N_FEATURES, hidden: tuple[int, ...] = (64, 32), dropout: float = 0.2):
        super().__init__()
        layers, prev = [], n_features
        for h in hidden:
            layers += [nn.Linear(prev, h), nn.ReLU(), nn.Dropout(dropout)]
            prev = h
        layers.append(nn.Linear(prev, 1))
        self.net = nn.Sequential(*layers)

    def forward(self, x: torch.Tensor) -> torch.Tensor:
        """Returns raw logit (B,)."""
        return self.net(x).squeeze(-1)

    @torch.no_grad()
    def score(self, x: torch.Tensor) -> torch.Tensor:
        return torch.sigmoid(self.forward(x))
