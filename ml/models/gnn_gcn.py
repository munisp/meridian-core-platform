"""Pure-torch 2-layer GCN for agent-collusion-ring detection (no torch-geometric).

Uses precomputed symmetric-normalized adjacency  A_hat = D^-1/2 (A+I) D^-1/2
from ml.data.synthetic.build_graph. CPU-only.
"""
from __future__ import annotations

import torch
import torch.nn as nn


class GraphConv(nn.Module):
    def __init__(self, in_dim: int, out_dim: int):
        super().__init__()
        self.lin = nn.Linear(in_dim, out_dim)

    def forward(self, x: torch.Tensor, adj: torch.Tensor) -> torch.Tensor:
        return self.lin(adj @ x)


class GCNRingDetector(nn.Module):
    def __init__(self, in_dim: int = 8, hidden: int = 32, dropout: float = 0.3):
        super().__init__()
        self.conv1 = GraphConv(in_dim, hidden)
        self.conv2 = GraphConv(hidden, 2)  # binary: in-ring vs not
        self.dropout = nn.Dropout(dropout)

    def forward(self, x: torch.Tensor, adj: torch.Tensor) -> torch.Tensor:
        h = torch.relu(self.conv1(x, adj))
        h = self.dropout(h)
        return self.conv2(h, adj)  # logits (N, 2)

    @torch.no_grad()
    def ring_probability(self, x: torch.Tensor, adj: torch.Tensor) -> torch.Tensor:
        return torch.softmax(self.forward(x, adj), dim=1)[:, 1]
