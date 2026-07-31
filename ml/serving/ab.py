"""Champion/challenger A/B routing for the serving tier.

Config-driven (env or JSON config file):
  - traffic split percentage to the challenger
  - sticky assignment: entity id hash -> arm (stable across requests)
  - shadow mode: challenger is scored but never served (response always
    comes from the champion); shadow scores still counted for comparison
  - per-arm metrics counters exposed via snapshot() -> GET /v1/ab/metrics
"""
from __future__ import annotations

import hashlib
import json
import os
import threading
import time
from dataclasses import dataclass, field
from typing import Optional


@dataclass
class ABConfig:
    enabled: bool = False
    model: str = "fraud"                # model this experiment applies to
    champion_version: Optional[str] = None   # None -> registry active
    challenger_version: Optional[str] = None
    challenger_pct: float = 0.0         # 0..100 of traffic to challenger
    shadow: bool = False                # challenger scored but not served

    @staticmethod
    def from_env() -> "ABConfig":
        cfg = ABConfig()
        path = os.environ.get("ML_AB_CONFIG")
        if path:
            try:
                with open(path, "r", encoding="utf-8") as fh:
                    data = json.load(fh)
                for k in ("enabled", "model", "champion_version", "challenger_version",
                          "challenger_pct", "shadow"):
                    if k in data:
                        setattr(cfg, k, data[k])
            except (OSError, json.JSONDecodeError):
                pass
        if os.environ.get("ML_AB_CHALLENGER_VERSION"):
            cfg.enabled = True
            cfg.challenger_version = os.environ["ML_AB_CHALLENGER_VERSION"]
        if os.environ.get("ML_AB_CHALLENGER_PCT"):
            cfg.enabled = True
            cfg.challenger_pct = float(os.environ["ML_AB_CHALLENGER_PCT"])
        if os.environ.get("ML_AB_SHADOW", "").lower() in ("1", "true", "yes"):
            cfg.enabled = True
            cfg.shadow = True
        if os.environ.get("ML_AB_MODEL"):
            cfg.model = os.environ["ML_AB_MODEL"]
        cfg.challenger_pct = max(0.0, min(100.0, float(cfg.challenger_pct)))
        return cfg


@dataclass
class ArmMetrics:
    requests: int = 0
    served: int = 0            # responses actually served from this arm
    shadowed: int = 0          # shadow-scored only
    errors: int = 0
    score_sum: float = 0.0
    latency_ms_sum: float = 0.0

    def snapshot(self) -> dict:
        return {
            "requests": self.requests,
            "served": self.served,
            "shadowed": self.shadowed,
            "errors": self.errors,
            "avg_score": (self.score_sum / self.requests) if self.requests else None,
            "avg_latency_ms": (self.latency_ms_sum / self.requests) if self.requests else None,
        }


class ABRouter:
    """Sticky champion/challenger router with per-arm counters."""

    def __init__(self, config: Optional[ABConfig] = None):
        self.config = config or ABConfig.from_env()
        self._lock = threading.Lock()
        self._arms = {"champion": ArmMetrics(), "challenger": ArmMetrics()}
        self.started_at = time.time()

    def _bucket(self, entity_key: str) -> float:
        """Stable bucket in [0, 100) from the entity id hash. The entity id
        is already a pseudonymised hash upstream; we hash again defensively
        and never store or log the raw value."""
        digest = hashlib.sha256(entity_key.encode("utf-8")).digest()
        return (int.from_bytes(digest[:8], "big") % 10_000) / 100.0

    def assign(self, entity_key: str) -> str:
        """Return 'champion' or 'challenger'. Sticky by entity hash."""
        cfg = self.config
        if not cfg.enabled or not cfg.challenger_version or cfg.challenger_pct <= 0:
            return "champion"
        return "challenger" if self._bucket(entity_key) < cfg.challenger_pct else "champion"

    def serving_arm(self, entity_key: str) -> str:
        """Arm whose response is actually served. Shadow mode always serves
        the champion."""
        arm = self.assign(entity_key)
        if arm == "challenger" and self.config.shadow:
            return "champion"
        return arm

    def record(self, arm: str, *, served: bool, score: Optional[float],
               latency_ms: float, error: bool = False) -> None:
        with self._lock:
            m = self._arms[arm]
            m.requests += 1
            if served:
                m.served += 1
            else:
                m.shadowed += 1
            if error:
                m.errors += 1
            if score is not None:
                m.score_sum += float(score)
            m.latency_ms_sum += float(latency_ms)

    def snapshot(self) -> dict:
        with self._lock:
            arms = {k: v.snapshot() for k, v in self._arms.items()}
        cfg = self.config
        return {
            "enabled": cfg.enabled,
            "model": cfg.model,
            "champion_version": cfg.champion_version,
            "challenger_version": cfg.challenger_version,
            "challenger_pct": cfg.challenger_pct,
            "shadow": cfg.shadow,
            "uptime_s": round(time.time() - self.started_at, 3),
            "arms": arms,
        }
