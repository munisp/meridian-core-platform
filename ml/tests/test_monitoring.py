"""Monitoring tests: PSI/KS correctness on shifted vs unshifted samples,
plus performance-monitor alert emission (file fallback)."""
import json
import os
import random
import sys

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from monitoring.drift import (  # noqa: E402
    DriftMonitor,
    ks_statistic,
    population_stability_index,
)
from monitoring.performance import PerformanceMonitor  # noqa: E402


def test_psi_identical_distributions_near_zero():
    counts_a = [100, 200, 300, 200, 100]
    counts_b = [102, 198, 305, 199, 96]
    psi = population_stability_index(counts_a, counts_b)
    assert psi < 0.01


def test_psi_shifted_distribution_large():
    counts_a = [500, 300, 100, 50, 10]
    counts_b = [10, 50, 100, 300, 500]
    psi = population_stability_index(counts_a, counts_b)
    assert psi > 0.25  # conventional significant-drift threshold


def test_ks_unshifted_small():
    rng = random.Random(7)
    a = [rng.gauss(0, 1) for _ in range(400)]
    b = [rng.gauss(0, 1) for _ in range(400)]
    assert ks_statistic(a, b) < 0.1


def test_ks_shifted_large():
    rng = random.Random(7)
    a = [rng.gauss(0, 1) for _ in range(400)]
    b = [rng.gauss(2.0, 1) for _ in range(400)]
    assert ks_statistic(a, b) > 0.5


def test_ks_edge_cases():
    assert ks_statistic([], [1, 2]) == 0.0
    assert 0.0 <= ks_statistic([1], [1]) <= 1.0


def _baseline(mean):
    bins = [mean - 3 + 0.6 * i for i in range(11)]
    return {"feature_0": {"bins": bins, "counts": [1, 3, 8, 20, 35, 35, 20, 8, 3, 1]}}


def test_drift_monitor_unshifted_no_alert():
    rng = random.Random(1)
    mon = DriftMonitor(registry=None, window=200)
    mon.set_baseline("fraud", _baseline(0.0))
    for _ in range(200):
        mon.observe("fraud", [rng.gauss(0, 1)])
    rep = mon.compare("fraud")
    assert rep["features"]["feature_0"]["psi"] < 0.25
    assert not any(a["model"] == "fraud" for a in mon.recent_alerts())


def test_drift_monitor_shifted_alerts():
    rng = random.Random(1)
    mon = DriftMonitor(registry=None, window=200)
    mon.set_baseline("fraud", _baseline(0.0))
    for _ in range(200):
        mon.observe("fraud", [rng.gauss(4.0, 1)])  # hard shift
    rep = mon.compare("fraud")
    assert rep["features"]["feature_0"]["psi"] > 0.25
    assert rep["features"]["feature_0"]["ks"] > 0.1
    assert any(a["type"] == "drift" for a in mon.recent_alerts())


def test_performance_monitor_latency_histogram():
    mon = PerformanceMonitor(window=100)
    for i in range(60):
        mon.record("fraud", score=0.5, latency_ms=float(i % 20))
    snap = mon.snapshot()["models"]["fraud"]
    assert snap["requests"] == 60
    assert snap["latency_ms"]["p50"] is not None
    assert snap["latency_ms"]["histogram"]["le_20ms"] == 60
    assert snap["score"]["mean"] == 0.5


def test_performance_monitor_latency_alert_file_fallback(tmp_path, monkeypatch):
    alerts_file = tmp_path / "alerts.jsonl"
    monkeypatch.setenv("ML_ALERTS_FILE", str(alerts_file))
    monkeypatch.delenv("KAFKA_BROKERS", raising=False)
    # reset module-level producer + file path captured at import time
    import monitoring.performance as perf_mod

    monkeypatch.setattr(perf_mod, "ALERTS_FILE", str(alerts_file))
    monkeypatch.setattr(perf_mod, "_producer", None)
    monkeypatch.setattr(perf_mod, "P95_ALERT_MS", 10.0)
    mon = perf_mod.PerformanceMonitor(window=200)
    for _ in range(50):
        mon.record("fraud", score=0.5, latency_ms=500.0)  # way over threshold
    lines = [json.loads(x) for x in alerts_file.read_text().splitlines() if x.strip()]
    assert any(a["type"] == "latency" and a["model"] == "fraud" for a in lines)
    assert any(a["type"] == "latency" for a in mon.recent_alerts())
