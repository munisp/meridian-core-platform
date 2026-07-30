"""Grad-boosted-style MLP for credit scoring.

Architecture mimics a shallow additive ensemble: K weak residual blocks
("boosting stages"), each a small MLP whose output is scaled by a learnable
shrinkage weight and added to the running logit -- gradient boosting as a
single differentiable network. Dual head: default-probability classifier
and score regressor (300-900 style credit score).
"""
from __future__ import annotations

import torch
import torch.nn as nn

from ..data.synthetic import N_FEATURES


class BoostStage(nn.Module):
    def __init__(self, n_features: int, hidden: int = 24):
        super().__init__()
        self.body = nn.Sequential(nn.Linear(n_features, hidden), nn.ReLU(), nn.Linear(hidden, 1))
        self.shrinkage = nn.Parameter(torch.tensor(0.3))

    def forward(self, x):
        return self.shrinkage * self.body(x).squeeze(-1)


class CreditScoreModel(nn.Module):
    def __init__(self, n_features: int = N_FEATURES, n_stages: int = 4, hidden: int = 24):
        super().__init__()
        self.base = nn.Linear(n_features, 1)
        self.stages = nn.ModuleList([BoostStage(n_features, hidden) for _ in range(n_stages)])
        self.reg_head = nn.Linear(n_features, 1)

    def forward(self, x: torch.Tensor) -> tuple[torch.Tensor, torch.Tensor]:
        """Returns (default_logit, score_regression_target)."""
        logit = self.base(x).squeeze(-1)
        for s in self.stages:
            logit = logit + s(x)
        return logit, self.reg_head(x).squeeze(-1)

    @torch.no_grad()
    def default_probability(self, x: torch.Tensor) -> torch.Tensor:
        return torch.sigmoid(self.forward(x)[0])

    @torch.no_grad()
    def credit_score(self, x: torch.Tensor, p_min: float = 300.0, p_max: float = 900.0) -> torch.Tensor:
        p = self.default_probability(x)
        return p_max - (p_max - p_min) * p  # higher default prob -> lower score
