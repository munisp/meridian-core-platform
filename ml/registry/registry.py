"""Versioned file-based model registry at ml/registry/.

Manifest contract (binding with Agent B serving):
  manifest.json: {model_name: {"active": "vN", "versions": {"vN": {
      "path": "weights/<model>_vN.pt.b64", "metrics": {...},
      "trained_at": ISO8601, "data_window": "start..end"}}}}

Weights: torch.save(state_dict) -> base64 text wrapper (*.pt.b64) so the
artifact is text-safe for push_files. save_weights/load_weights handle both
state_dicts and plain numpy/object payloads.

MLflow adapter: activated ONLY if MLFLOW_TRACKING_URI is set (import-guarded);
otherwise the file-based registry is the source of truth.
"""
from __future__ import annotations

import base64
import io
import json
import logging
import os
from datetime import datetime, timezone
from pathlib import Path

log = logging.getLogger("ml.registry")
logging.basicConfig(level=logging.INFO, format="%(message)s")

REGISTRY_DIR = Path(__file__).resolve().parent
MANIFEST = REGISTRY_DIR / "manifest.json"


def save_weights(obj, path: str | Path):
    """torch.save to bytes, base64-wrap, write text file (*.pt.b64).

    Verifies the write (shared/network filesystems can drop or delay file
    visibility); retries a few times before giving up.
    """
    import torch
    buf = io.BytesIO()
    torch.save(obj, buf)
    b64 = base64.b64encode(buf.getvalue()).decode("ascii")
    p = Path(path)
    for attempt in range(5):
        p.write_text(b64)
        try:
            if p.exists() and p.read_text() == b64:
                return
        except OSError:
            pass
        import time
        time.sleep(0.2 * (attempt + 1))
    log.warning("save_weights: could not verify %s after retries", path)


def load_weights(path: str | Path):
    """Inverse of save_weights. Falls back to plain torch.load for raw .pt."""
    import torch
    p = Path(path)
    raw = p.read_bytes()
    try:
        data = base64.b64decode(raw, validate=True)
    except Exception:
        data = raw
    return torch.load(io.BytesIO(data), map_location="cpu", weights_only=False)


_MANIFEST_CACHE: dict | None = None


def _load_manifest() -> dict:
    """Load the manifest. Within a process, a cache is authoritative after first
    load so that stale filesystem reads cannot drop previously registered
    entries during a single writer's run (observed on the shared /mnt FS)."""
    global _MANIFEST_CACHE
    if _MANIFEST_CACHE is not None:
        return json.loads(json.dumps(_MANIFEST_CACHE))  # defensive copy
    if MANIFEST.exists():
        try:
            _MANIFEST_CACHE = json.loads(MANIFEST.read_text())
        except (json.JSONDecodeError, OSError):
            _MANIFEST_CACHE = {}
    else:
        _MANIFEST_CACHE = {}
    return json.loads(json.dumps(_MANIFEST_CACHE))


def _save_manifest(m: dict):
    global _MANIFEST_CACHE
    _MANIFEST_CACHE = json.loads(json.dumps(m))
    payload = json.dumps(m, indent=2, sort_keys=True)
    for attempt in range(5):
        MANIFEST.write_text(payload)
        try:
            if json.loads(MANIFEST.read_text()) == m:
                return
        except (json.JSONDecodeError, OSError):
            pass
        import time
        time.sleep(0.2 * (attempt + 1))
    log.warning("_save_manifest: could not verify manifest write after retries")


def _mlflow():
    """MLflow client if MLFLOW_TRACKING_URI set, else None (H1-style selection)."""
    uri = os.environ.get("MLFLOW_TRACKING_URI", "").strip()
    if not uri:
        log.info("profile=dev component=ml-registry backend=file")
        return None
    try:
        import mlflow  # optional
        mlflow.set_tracking_uri(uri)
        log.info("profile=prod component=ml-registry backend=mlflow uri=%s", uri)
        return mlflow
    except ImportError:
        log.warning("MLFLOW_TRACKING_URI set but mlflow not installed; using file registry")
        return None


def register(model_name: str, state, metrics: dict, data_window: str,
             trained_at: str | None = None) -> str:
    """Save weights as next version, record in manifest. Returns version id e.g. 'v3'."""
    m = _load_manifest()
    entry = m.setdefault(model_name, {"active": None, "versions": {}})
    n = len(entry["versions"]) + 1
    version = f"v{n}"
    path = REGISTRY_DIR / "weights" / f"{model_name}_{version}.pt.b64"
    path.parent.mkdir(parents=True, exist_ok=True)
    save_weights(state, path)
    entry["versions"][version] = {
        "path": f"weights/{model_name}_{version}.pt.b64",
        "metrics": metrics,
        "trained_at": trained_at or datetime.now(timezone.utc).isoformat(),
        "data_window": data_window,
    }
    if entry["active"] is None:
        entry["active"] = version
    _save_manifest(m)
    mlf = _mlflow()
    if mlf:
        try:
            with mlf.start_run(run_name=f"{model_name}-{version}"):
                mlf.log_metrics({k: v for k, v in metrics.items() if isinstance(v, (int, float))})
                mlf.log_param("data_window", data_window)
                mlf.log_artifact(str(path))
        except Exception as e:  # MLflow is advisory only
            log.warning("mlflow log failed: %s", e)
    return version


def promote(model_name: str, version: str) -> bool:
    m = _load_manifest()
    entry = m.get(model_name)
    if not entry or version not in entry["versions"]:
        return False
    entry["active"] = version
    _save_manifest(m)
    return True


def rollback(model_name: str) -> str | None:
    """Demote active to previous version. Returns new active version."""
    m = _load_manifest()
    entry = m.get(model_name)
    if not entry:
        return None
    versions = sorted(entry["versions"], key=lambda v: int(v[1:]))
    cur = entry["active"]
    if cur in versions and versions.index(cur) > 0:
        entry["active"] = versions[versions.index(cur) - 1]
        _save_manifest(m)
    return entry["active"]


def active(model_name: str) -> tuple[str, dict] | None:
    """Returns (version, manifest_entry) for the active version, else None."""
    m = _load_manifest()
    entry = m.get(model_name)
    if not entry or not entry["active"]:
        return None
    v = entry["active"]
    return v, entry["versions"][v]


def load_active(model_name: str):
    """Load the active version's weights. Raises FileNotFoundError if missing."""
    act = active(model_name)
    if act is None:
        raise FileNotFoundError(f"no active version for {model_name}")
    return load_weights(REGISTRY_DIR / act[1]["path"])
