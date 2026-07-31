"""Behaviour change-point detection (REAL) — CUSUM / BOCPD-lite over
per-taxpayer filing behaviour streams.

Emits ``nrs.ml.changepoint.v1`` alert dicts when a taxpayer's behaviour
metric (e.g. monthly filing count, mean amount, VAT ratio) shifts
persistently. Two detectors:

  - CUSUMDetector: two-sided cumulative-sum with drift allowance k and
    decision threshold h. Classic, deterministic, O(1) per observation.
  - BOCPDLite: a light Bayesian online change-point approximation tracking
    the run-length posterior with a constant hazard and a Gaussian
    predictive model (per run-length mean/precision). O(R) per observation
    with run-length pruning.

Both are dependency-free (stdlib math only) so the serving tier can run
them without numpy/scipy.
"""
from __future__ import annotations

import math
import time
from dataclasses import dataclass, field
from typing import Optional

ALERT_TYPE = "nrs.ml.changepoint.v1"


@dataclass
class ChangePointAlert:
    entity: str
    metric: str
    index: int
    value: float
    detector: str
    detail: str
    at: str = field(default_factory=lambda: time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()))
    type: str = ALERT_TYPE

    def to_dict(self) -> dict:
        return {
            "type": self.type,
            "entity": self.entity,
            "metric": self.metric,
            "index": self.index,
            "value": self.value,
            "detector": self.detector,
            "detail": self.detail,
            "at": self.at,
        }


class CUSUMDetector:
    """Two-sided CUSUM. Baseline mean/std are estimated from the first
    ``warmup`` observations (or supplied). Signals when the cumulative
    deviation exceeds ``h * std`` with drift allowance ``k * std``."""

    def __init__(self, entity: str, metric: str, warmup: int = 8,
                 k: float = 0.5, h: float = 5.0,
                 baseline_mean: Optional[float] = None,
                 baseline_std: Optional[float] = None):
        self.entity, self.metric = entity, metric
        self.warmup, self.k, self.h = warmup, k, h
        self._vals: list[float] = []
        self.mean, self.std = baseline_mean, baseline_std
        self.s_pos = self.s_neg = 0.0
        self.alerts: list[ChangePointAlert] = []

    def _fit_baseline(self):
        xs = self._vals[: self.warmup]
        self.mean = sum(xs) / len(xs)
        var = sum((x - self.mean) ** 2 for x in xs) / max(len(xs) - 1, 1)
        self.std = max(math.sqrt(var), 1e-9)

    def observe(self, value: float, index: Optional[int] = None) -> Optional[ChangePointAlert]:
        idx = len(self._vals) if index is None else index
        self._vals.append(value)
        if self.mean is None and len(self._vals) >= self.warmup:
            self._fit_baseline()
        if self.mean is None or self.std is None:
            return None
        z = (value - self.mean) / self.std
        self.s_pos = max(0.0, self.s_pos + z - self.k)
        self.s_neg = max(0.0, self.s_neg - z - self.k)
        if self.s_pos > self.h or self.s_neg > self.h:
            direction = "up" if self.s_pos > self.h else "down"
            alert = ChangePointAlert(
                entity=self.entity, metric=self.metric, index=idx, value=value,
                detector="cusum",
                detail=f"CUSUM {direction}-shift: S={max(self.s_pos, self.s_neg):.2f} > h={self.h}",
            )
            self.alerts.append(alert)
            self.s_pos = self.s_neg = 0.0  # reset after signalling
            return alert
        return None


class BOCPDLite:
    """BOCPD-lite: constant-hazard run-length posterior with Gaussian
    predictive (unknown mean, unit-variance-scaled). Signals when the MAP
    run resets (a young run takes over the posterior) — confirming the
    change one observation after it happens."""

    def __init__(self, entity: str, metric: str, hazard: float = 1.0 / 50.0,
                 threshold: float = 0.5, max_run: int = 200,
                 prior_var: float = 10.0):
        self.entity, self.metric = entity, metric
        self.hazard, self.threshold = hazard, threshold
        self.max_run, self.prior_var = max_run, prior_var
        # run-length states: [mean, precision, log-prob, run_age]
        self._states: list[list[float]] = [[0.0, 1.0 / prior_var, 0.0, 0.0]]
        self.alerts: list[ChangePointAlert] = []
        self._alerted_reset = False

    def observe(self, value: float, index: int = 0) -> Optional[ChangePointAlert]:
        log_h = math.log(self.hazard)
        log_1h = math.log1p(-self.hazard)
        new_states: list[list[float]] = []
        # growth + change-point hypotheses
        cp_logs: list[float] = []
        for mean, prec, lp, age in self._states:
            # predictive N(mean, 1/prec + 1) for unit observation noise
            var = 1.0 / prec + 1.0
            log_pred = -0.5 * math.log(2 * math.pi * var) - (value - mean) ** 2 / (2 * var)
            # growth: posterior update of the Gaussian mean
            new_prec = prec + 1.0
            new_mean = (prec * mean + value) / new_prec
            new_states.append([new_mean, new_prec, lp + log_pred + log_1h, age + 1.0])
            cp_logs.append(lp + log_pred + log_h)
        # change-point state: reset to prior
        m = max(cp_logs)
        cp_log = m + math.log(sum(math.exp(c - m) for c in cp_logs))
        new_states.append([value, 1.0 + 1.0 / self.prior_var, cp_log, 0.0])
        # normalise
        mx = max(s[2] for s in new_states)
        tot = sum(math.exp(s[2] - mx) for s in new_states)
        for s in new_states:
            s[2] -= mx + math.log(tot)
        # change posterior = prob mass on run-age-0 states (before pruning).
        cp_prob = sum(math.exp(s[2]) for s in new_states if s[3] == 0.0)
        # prune
        new_states.sort(key=lambda s: -s[2])
        self._states = new_states[: self.max_run]
        # Retrospective signal: the MAP run has just reset (a young run holds
        # >= threshold of the posterior) — confirms the change one
        # observation after it happens.
        map_age = self._states[0][3]
        map_prob = math.exp(self._states[0][2])
        if index >= 3 and map_age <= 1.0 and map_prob >= self.threshold:
            if not self._alerted_reset:
                self._alerted_reset = True
                alert = ChangePointAlert(
                    entity=self.entity, metric=self.metric, index=index, value=value,
                    detector="bocpd_lite",
                    detail=f"BOCPD-lite run reset: MAP run age {int(map_age)} holds "
                           f"{map_prob:.2f} of posterior (cp mass {cp_prob:.2f})",
                )
                self.alerts.append(alert)
                return alert
        else:
            self._alerted_reset = False
        return None


def detect_stream(entity: str, metric: str, values: list[float],
                  detector: str = "cusum", **kw) -> list[dict]:
    """Run a detector over a whole behaviour stream; returns alert dicts."""
    det = (CUSUMDetector if detector == "cusum" else BOCPDLite)(entity, metric, **kw)
    out = []
    for i, v in enumerate(values):
        a = det.observe(float(v), i)
        if a is not None:
            out.append(a.to_dict())
    return out
