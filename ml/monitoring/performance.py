"""Serving performance monitor: latency histograms, score-distribution
shift, alert thresholds, and alert emission.

Alert transport:
  - KAFKA_BROKERS set AND kafka-python importable -> KafkaProducer to
    topic `ml.monitoring.alerts` (log profile=prod component=ml-monitoring)
  - otherwise -> append-only JSONL file fallback (ML_ALERTS_FILE,
    default ./ml-monitoring-alerts.jsonl) (profile=dev)
Startup never fails on missing brokers/library.
"""
from __future__ import annotations

import json
import logging
import os
import threading
import time
from collections import defaultdict, deque
from typing import Optional

logger = logging.getLogger("ml.monitoring")

LATENCY_BUCKETS_MS = [1, 2, 5, 10, 20, 50, 100, 250, 500, 1000, 5000]
P95_ALERT_MS = float(os.environ.get("ML_PERF_P95_ALERT_MS", "50"))
MEAN_SCORE_SHIFT_ALERT = float(os.environ.get("ML_PERF_SCORE_SHIFT_ALERT", "0.20"))
WINDOW = int(os.environ.get("ML_PERF_WINDOW", "1000"))
ALERTS_TOPIC = os.environ.get("ML_ALERTS_TOPIC", "ml.monitoring.alerts")
ALERTS_FILE = os.environ.get("ML_ALERTS_FILE", "ml-monitoring-alerts.jsonl")

_producer = None
_producer_lock = threading.Lock()


def _get_producer():
    """Lazily build a Kafka producer; None -> file fallback."""
    global _producer
    with _producer_lock:
        if _producer is not None:
            return _producer
        brokers = os.environ.get("KAFKA_BROKERS")
        if not brokers:
            logger.info("profile=dev component=ml-monitoring sink=file path=%s", ALERTS_FILE)
            _producer = False
            return None
        try:
            from kafka import KafkaProducer

            _producer = KafkaProducer(
                bootstrap_servers=brokers.split(","),
                value_serializer=lambda v: json.dumps(v).encode("utf-8"),
            )
            logger.info("profile=prod component=ml-monitoring sink=kafka topic=%s", ALERTS_TOPIC)
        except Exception as exc:
            logger.warning("profile=dev component=ml-monitoring kafka unavailable: %s", exc)
            _producer = False
            return None
        return _producer


def emit_alert(alert: dict) -> None:
    """Emit an alert event to Kafka if available, else append-only file."""
    alert = dict(alert)
    alert.setdefault("ts", time.time())
    producer = _get_producer()
    if producer:
        try:
            producer.send(ALERTS_TOPIC, alert)
            return
        except Exception as exc:  # fall through to file
            logger.warning("component=ml-monitoring kafka send failed: %s", exc)
    try:
        with open(ALERTS_FILE, "a", encoding="utf-8") as fh:
            fh.write(json.dumps(alert) + "\n")
    except OSError as exc:
        logger.warning("component=ml-monitoring alert sink failed: %s", exc)


def _percentile(sorted_vals, q: float) -> Optional[float]:
    if not sorted_vals:
        return None
    k = (len(sorted_vals) - 1) * q
    lo = int(k)
    hi = min(lo + 1, len(sorted_vals) - 1)
    frac = k - lo
    return sorted_vals[lo] * (1 - frac) + sorted_vals[hi] * frac


class PerformanceMonitor:
    """Sliding-window latency + score stats per model with alert rules."""

    def __init__(self, window: int = WINDOW):
        self.window = window
        self._lock = threading.Lock()
        self._latency: dict[str, deque] = defaultdict(lambda: deque(maxlen=window))
        self._scores: dict[str, deque] = defaultdict(lambda: deque(maxlen=window))
        self._baseline_mean_score: dict[str, float] = {}
        self._alerts: deque = deque(maxlen=500)
        self._requests = defaultdict(int)

    def record(self, model: str, score: Optional[float], latency_ms: float) -> None:
        with self._lock:
            self._requests[model] += 1
            self._latency[model].append(float(latency_ms))
            if score is not None:
                self._scores[model].append(float(score))
        self._check(model)

    def _check(self, model: str) -> None:
        with self._lock:
            n = self._requests[model]
            lat = sorted(self._latency[model])
            scores = list(self._scores[model])
            base_mean = self._baseline_mean_score.get(model)
        if n < 50 or n % 50 != 0:
            return
        p95 = _percentile(lat, 0.95)
        if p95 is not None and p95 > P95_ALERT_MS:
            self._raise({"type": "latency", "model": model, "p95_ms": round(p95, 3),
                         "threshold_ms": P95_ALERT_MS})
        if base_mean is not None and scores:
            mean_now = sum(scores) / len(scores)
            if abs(mean_now - base_mean) >= MEAN_SCORE_SHIFT_ALERT:
                self._raise({"type": "score_shift", "model": model,
                             "baseline_mean": base_mean, "window_mean": round(mean_now, 5)})

    def set_score_baseline(self, model: str, mean_score: float) -> None:
        self._baseline_mean_score[model] = float(mean_score)

    def _raise(self, alert: dict) -> None:
        alert["ts"] = time.time()
        with self._lock:
            self._alerts.append(alert)
        emit_alert(alert)

    def snapshot(self) -> dict:
        with self._lock:
            models = set(self._latency) | set(self._scores)
            out = {}
            for m in sorted(models):
                lat = sorted(self._latency[m])
                scores = sorted(self._scores[m])
                hist = {f"le_{b}ms": sum(1 for v in lat if v <= b) for b in LATENCY_BUCKETS_MS}
                out[m] = {
                    "requests": self._requests[m],
                    "latency_ms": {
                        "p50": _percentile(lat, 0.50),
                        "p95": _percentile(lat, 0.95),
                        "p99": _percentile(lat, 0.99),
                        "histogram": hist,
                    },
                    "score": {
                        "mean": (sum(scores) / len(scores)) if scores else None,
                        "p05": _percentile(scores, 0.05),
                        "p50": _percentile(scores, 0.50),
                        "p95": _percentile(scores, 0.95),
                        "baseline_mean": self._baseline_mean_score.get(m),
                    },
                }
        return {"thresholds": {"p95_alert_ms": P95_ALERT_MS,
                               "score_shift_alert": MEAN_SCORE_SHIFT_ALERT},
                "models": out}

    def recent_alerts(self) -> list:
        with self._lock:
            return list(self._alerts)
