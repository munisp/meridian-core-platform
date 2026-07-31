"""File-based model-registry client for the serving tier.

Duplicates the tiny manifest-reading / weight-loading helpers inline so the
serving tier does NOT import from ml.registry (owned by another component).
Codes to the manifest contract in plan-ml.md:

    ml/registry/manifest.json
    {
      "<model_name>": {
        "active": "<version>",
        "versions": {
          "<version>": {
            "weights": "weights/<model>-<version>.pt.b64",
            "baseline_stats": "baseline/<model>-<version>.json",   # optional
            "metrics": {...}                                        # optional
          }
        }
      }
    }

Weights are torch.save state_dicts, base64-wrapped (`.pt.b64`) so they can be
pushed through text-only file APIs. Decoding: base64 -> bytes -> torch.load.
"""
from __future__ import annotations

import base64
import io
import json
import os
from dataclasses import dataclass, field
from typing import Optional

DEFAULT_REGISTRY_DIR = os.environ.get(
    "ML_REGISTRY_DIR",
    os.path.join(os.path.dirname(__file__), "..", "registry"),
)


@dataclass
class ModelVersion:
    name: str
    version: str
    weights_path: str
    baseline_stats_path: Optional[str] = None
    metrics: dict = field(default_factory=dict)


class RegistryClient:
    """Reads manifest.json from a registry directory. Never raises on a
    missing/corrupt manifest: callers get empty views and must hot-skip."""

    def __init__(self, registry_dir: Optional[str] = None):
        self.registry_dir = os.path.abspath(registry_dir or DEFAULT_REGISTRY_DIR)

    @property
    def manifest_path(self) -> str:
        return os.path.join(self.registry_dir, "manifest.json")

    def load_manifest(self) -> dict:
        try:
            with open(self.manifest_path, "r", encoding="utf-8") as fh:
                data = json.load(fh)
            return data if isinstance(data, dict) else {}
        except (OSError, json.JSONDecodeError):
            return {}

    def _to_version(self, model: str, version: str, vinfo: dict) -> Optional[ModelVersion]:
        weights = vinfo.get("weights")
        if not weights:
            return None
        return ModelVersion(
            name=model,
            version=str(version),
            weights_path=os.path.join(self.registry_dir, weights),
            baseline_stats_path=(
                os.path.join(self.registry_dir, vinfo["baseline_stats"])
                if vinfo.get("baseline_stats")
                else None
            ),
            metrics=vinfo.get("metrics") or {},
        )

    def active_version(self, model: str) -> Optional[ModelVersion]:
        entry = self.load_manifest().get(model)
        if not isinstance(entry, dict):
            return None
        active = entry.get("active")
        vinfo = (entry.get("versions") or {}).get(active)
        if not active or not isinstance(vinfo, dict):
            return None
        return self._to_version(model, active, vinfo)

    def get_version(self, model: str, version: str) -> Optional[ModelVersion]:
        entry = self.load_manifest().get(model) or {}
        vinfo = (entry.get("versions") or {}).get(version)
        if not isinstance(vinfo, dict):
            return None
        return self._to_version(model, version, vinfo)

    def baseline_stats(self, model: str) -> Optional[dict]:
        mv = self.active_version(model)
        if not mv or not mv.baseline_stats_path:
            return None
        try:
            with open(mv.baseline_stats_path, "r", encoding="utf-8") as fh:
                return json.load(fh)
        except (OSError, json.JSONDecodeError):
            return None


def load_state_dict_b64(path: str) -> dict:
    """Decode a .pt.b64 weights file into a torch state_dict. Raises OSError
    if the file is missing; ImportError if torch is unavailable."""
    import torch  # local import: serving degrades gracefully without torch

    with open(path, "rb") as fh:
        raw = base64.b64decode(fh.read())
    return torch.load(io.BytesIO(raw), map_location="cpu", weights_only=True)


def build_mlp_from_state_dict(state_dict: dict):
    """Reconstruct a generic feed-forward net from a state_dict of
    Linear weight/bias pairs (layer ordering by key sort). ReLU between
    layers, identity on the final layer. Keeps serving independent of the
    training-side model classes."""
    import torch

    layer_keys = sorted(
        {
            k.rsplit(".", 1)[0]
            for k, v in state_dict.items()
            if k.endswith(".weight") and v.dim() == 2
        }
    )
    if not layer_keys:
        raise ValueError("state_dict has no 2-D Linear weights")
    layers = []
    for key in layer_keys:
        w = state_dict[f"{key}.weight"]
        b = state_dict.get(f"{key}.bias")
        lin = torch.nn.Linear(w.shape[1], w.shape[0], bias=b is not None)
        with torch.no_grad():
            lin.weight.copy_(w)
            if b is not None:
                lin.bias.copy_(b)
        layers.append(lin)

    class _MLP(torch.nn.Module):
        def __init__(self, mods):
            super().__init__()
            self.layers = torch.nn.ModuleList(mods)

        def forward(self, x):
            for i, layer in enumerate(self.layers):
                x = layer(x)
                if i < len(self.layers) - 1:
                    x = torch.relu(x)
            return x

    net = _MLP(layers)
    net.eval()
    return net
