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
        # Real registry artifacts use "path"; the documented contract and
        # older fixtures use "weights". Accept both.
        weights = vinfo.get("weights") or vinfo.get("path")
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


def unwrap_payload(payload: dict) -> tuple[dict, dict]:
    """Normalise a registry weight artifact to (state_dict, meta).

    ml/training/train.py registers payloads wrapped as
    ``{"state_dict": ..., "scaler": {"mu": ..., "sd": ...}, ...}`` while some
    fixtures store a bare (flat) state_dict. Accept both so serving can load
    the real checked-in artifacts (audit: every /v1/score/* 503'd on the
    wrapped format).
    """
    if (
        isinstance(payload, dict)
        and isinstance(payload.get("state_dict"), dict)
        and any(str(k).endswith(".weight") for k in payload["state_dict"])
    ):
        meta = {k: v for k, v in payload.items() if k != "state_dict"}
        return payload["state_dict"], meta
    if isinstance(payload, dict):
        return payload, {}
    raise ValueError("unrecognised weights payload format")


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


def build_credit_from_state_dict(state_dict: dict):
    """Reconstruct the CreditScoreModel additive ensemble (base linear +
    K shrinkage-scaled residual MLP stages reading the raw input). The
    generic chained-MLP builder cannot represent this architecture."""
    import torch

    base_w, base_b = state_dict["base.weight"], state_dict.get("base.bias")
    stage_ids = sorted(
        {k.split(".")[1] for k in state_dict if k.startswith("stages.")},
        key=int,
    )

    class _Credit(torch.nn.Module):
        def __init__(self):
            super().__init__()
            self.base = torch.nn.Linear(base_w.shape[1], base_w.shape[0],
                                        bias=base_b is not None)
            with torch.no_grad():
                self.base.weight.copy_(base_w)
                if base_b is not None:
                    self.base.bias.copy_(base_b)
            self.stages = torch.nn.ModuleList()
            for sid in stage_ids:
                w0 = state_dict[f"stages.{sid}.body.0.weight"]
                b0 = state_dict.get(f"stages.{sid}.body.0.bias")
                w2 = state_dict[f"stages.{sid}.body.2.weight"]
                b2 = state_dict.get(f"stages.{sid}.body.2.bias")
                shrink = float(state_dict.get(f"stages.{sid}.shrinkage", 1.0))
                body = torch.nn.Sequential(
                    torch.nn.Linear(w0.shape[1], w0.shape[0], bias=b0 is not None),
                    torch.nn.ReLU(),
                    torch.nn.Linear(w2.shape[1], w2.shape[0], bias=b2 is not None),
                )
                with torch.no_grad():
                    body[0].weight.copy_(w0)
                    if b0 is not None:
                        body[0].bias.copy_(b0)
                    body[2].weight.copy_(w2)
                    if b2 is not None:
                        body[2].bias.copy_(b2)
                body.shrinkage = shrink  # type: ignore[attr-defined]
                self.stages.append(body)

        def forward(self, x):
            logit = self.base(x).squeeze(-1)
            for stage in self.stages:
                logit = logit + stage.shrinkage * stage(x).squeeze(-1)
            return logit

    net = _Credit()
    net.eval()
    return net
