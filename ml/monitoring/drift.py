"""Drift detection: PSI + Kolmogorov-Smirnov vs training baseline stats.

Baseline stats are loaded from the registry manifest contract:
  manifest.json -> <model>.versions[<active>].baseline_stats
  JSON file shaped either as:
    {"feature_0": {"bins": [e0,e1,...], "counts": [c0,...]}, ...}
  or (simpler) {"feature_0": [sample values...], ...}
A missing baseline simply disables drift reporting for that model
(hot-skip philosophy: monitoring must never break serving).

Alerts are emitted through monitoring.performance.emit_alert (Kafka if
KAFKA_BROKERS set and kafka-python installed, else append-only file).
"""
from __future__ import annotations

import math
import os
import threading
import time
from collections import defaultdict, deque
from typing import Optional

PSI_ALERT_THRESHOLD = float(os.environ.get("ML_DRIFT_PSI_THRESHOLD", "0.25"))
KS_ALERT_THRESHOLD = float(os.environ.get("ML_DRIFT_KS_THRESHOLD", "0.15"))
WINDOW = int(os.environ.get("ML_DRIFT_WINDOW", "500"))
MAX_FEATURES = 64


def population_stability_index(expected_counts, actual_counts, eps: float = 1e-6) -> float:
    """PSI between two histograms (aligned bins). >=0.25 = significant drift.
    Laplace smoothing (+0.5/bin) damps false positives from sparse tail bins
    in small windows."""
    expected_counts = [c + 0.5 for c in expected_counts]
    actual_counts = [c + 0.5 for c in actual_counts]
    total_e = max(sum(expected_counts), eps)
    total_a = max(sum(actual_counts), eps)
    psi = 0.0
    for e, a in zip(expected_counts, actual_counts):
        pe = max(e / total_e, eps)
        pa = max(a / total_a, eps)
        psi += (pe - pa) * math.log(pe / pa)
    return psi


def ks_statistic(sample_a, sample_b) -> float:
    """Two-sample Kolmogorov-Smirnov D statistic (pure python)."""
    a, b = sorted(sample_a), sorted(sample_b)
    if not a or not b:
        return 0.0
    i = j = 0
    d = 0.0
    na, nb = len(a), len(b)
    while i < na and j < nb:
        if a[i] <= b[j]:
            i += 1
        else:
            j += 1
        d = max(d, abs(i / na - j / nb))
    return d


def _histogram(values, edges) -> list:
    counts = [0] * (len(edges) - 1)
    for v in values:
        for idx in range(len(edges) - 1):
            if edges[idx] <= v < edges[idx + 1] or (idx == len(edges) - 2 and v == edges[-1]):
                counts[idx] += 1
                break
    return counts


def _edges_from(values, n_bins: int = 10):
    lo, hi = min(values), max(values)
    if hi <= lo:
        hi = lo + 1.0
    step = (hi - lo) / n_bins
    return [lo + i * step for i in range(n_bins + 1)]


class DriftMonitor:
    """Sliding-window feature distribution monitor per model."""

    def __init__(self, registry=None, window: int = WINDOW):
        self.window = window
        self._lock = threading.Lock()
        self._samples: dict[str, deque] = defaultdict(lambda: deque(maxlen=window))
        self._baselines: dict[str, dict] = {}
        self._alerts: deque = deque(maxlen=500)
        if registry is not None:
            for model in ("fraud", "credit", "graph", "anomaly"):
                stats = registry.baseline_stats(model)
                if stats:
                    self._baselines[model] = stats

    def set_baseline(self, model: str, stats: dict) -> None:
        self._baselines[model] = stats

    def observe(self, model: str, features) -> None:
        with self._lock:
            self._samples[model].append([float(f) for f in features[:MAX_FEATURES]])
        self._maybe_alert(model)

    # -- internals ---------------------------------------------------------

    def _baseline_feature(self, model: str, idx: int):
        stats = self._baselines.get(model) or {}
        entry = stats.get(f"feature_{idx}") or stats.get(str(idx))
        if entry is None:
            return None
        if isinstance(entry, dict) and "bins" in entry and "counts" in entry:
            return entry
        if isinstance(entry, list) and entry:
            edges = _edges_from([float(v) for v in entry])
            return {"bins": edges, "counts": _histogram([float(v) for v in entry], edges)}
        return None

    def compare(self, model: str):
        """Return per-feature PSI/KS of the current window vs baseline."""
        with self._lock:
            window = list(self._samples.get(model, ()))
        if not window:
            return {"model": model, "n": 0, "features": {}}
        n_feat = min(len(r) for r in window)
        out = {}
        for idx in range(n_feat):
            col = [r[idx] for r in window]
            base = self._baseline_feature(model, idx)
            if base is None:
                continue
            edges = base["bins"]
            counts_now = _histogram(col, edges)
            psi = population_stability_index(base["counts"], counts_now)
            # reconstruct approximate baseline sample for KS:
            # spread reconstructed points evenly within each bin (deterministic)
            # so KS is not inflated by coarse midpoint-only reconstruction
            base_sample = []
            for i, c in enumerate(base["counts"]):
                c = int(c)
                width = edges[i + 1] - edges[i]
                for j in range(c):
                    base_sample.append(edges[i] + (j + 0.5) / max(c, 1) * width)
            ks = ks_statistic(base_sample[:2000], col[:2000])
            out[f"feature_{idx}"] = {"psi": round(psi, 5), "ks": round(ks, 5)}
        return {"model": model, "n": len(window), "features": out}

    def _maybe_alert(self, model: str) -> None:
        with self._lock:
            n = len(self._samples.get(model, ()))
        if n < max(100, self.window // 5) or n % 50 != 0:
            return
        rep = self.compare(model)
        for feat, m in rep["features"].items():
            if m["psi"] >= PSI_ALERT_THRESHOLD or m["ks"] >= KS_ALERT_THRESHOLD:
                self._emit(model, feat, m)

    def _emit(self, model: str, feature: str, metrics: dict) -> None:
        alert = {
            "type": "drift",
            "model": model,
            "feature": feature,
            "metrics": metrics,
            "ts": time.time(),
        }
        with self._lock:
            self._alerts.append(alert)
        try:
            from .performance import emit_alert

            emit_alert(alert)
        except Exception:
            pass

    def report(self) -> dict:
        models = set(self._baselines) | set(self._samples)
        return {
            "thresholds": {"psi": PSI_ALERT_THRESHOLD, "ks": KS_ALERT_THRESHOLD},
            "models": {m: self.compare(m) for m in sorted(models)},
        }

    def recent_alerts(self) -> list:
        with self._lock:
            return list(self._alerts)
