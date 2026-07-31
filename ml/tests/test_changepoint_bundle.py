"""Tests for I6 (behaviour change-point detection) and I2 (audit-case
auto-bundling)."""
import os
import sys

import pytest

fastapi = pytest.importorskip("fastapi")
from fastapi.testclient import TestClient  # noqa: E402

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from monitoring.changepoint import CUSUMDetector, detect_stream  # noqa: E402
from serving.audit_bundle import bundle_audit_cases  # noqa: E402
from serving.main import create_app  # noqa: E402


def test_cusum_detects_level_shift():
    vals = [10.0] * 20 + [35.0] * 10
    alerts = detect_stream("tin-h-1", "filing_amount", vals, warmup=8)
    assert alerts, "CUSUM must signal a sustained level shift"
    a = alerts[0]
    assert a["type"] == "nrs.ml.changepoint.v1"
    assert a["detector"] == "cusum"
    assert a["index"] >= 20  # signals after the shift begins


def test_cusum_quiet_on_stationary():
    vals = [10.0 + (0.5 if i % 2 else -0.5) for i in range(40)]
    assert detect_stream("tin-h-2", "filing_count", vals, warmup=8) == []


def test_bocpd_lite_detects_shift():
    vals = [5.0] * 15 + [25.0] * 15
    alerts = detect_stream("tin-h-3", "vat_ratio", vals, detector="bocpd_lite",
                           threshold=0.3)
    assert alerts and alerts[0]["detector"] == "bocpd_lite"


def test_bundling_merges_overlapping_rings():
    dets = [
        {"ring_id": "r1", "members": ["a", "b", "c"], "ring_probability": 0.9,
         "evidence_refs": ["ev-1"]},
        {"ring_id": "r2", "members": ["c", "d"], "ring_probability": 0.7,
         "evidence_refs": ["ev-2"]},
        {"ring_id": "r3", "members": ["x", "y"], "ring_probability": 0.4},
    ]
    out = bundle_audit_cases(dets, model_versions={"graph": "v1"},
                             rule_packs=["rp-bank-thresholds"])
    assert out["bundle_count"] == 2  # r1+r2 share member c
    big = out["cases"][0]
    assert big["severity"] == "high"
    assert set(big["member_tin_hashes"]) == {"a", "b", "c", "d"}
    assert big["shared_evidence"]["evidence_refs"] == ["ev-1", "ev-2"]
    assert big["shared_evidence"]["model_versions"] == {"graph": "v1"}


def test_changepoint_and_bundle_endpoints(tmp_path):
    os.environ.setdefault("AUTH_MODE", "dev")
    client = TestClient(create_app(str(tmp_path)))
    r = client.post("/v1/monitoring/changepoint",
                    json={"entity": "tin-h-9", "metric": "filing_amount",
                          "values": [10.0] * 12 + [40.0] * 8},
                    headers={"X-Dev-Role": "operator"})
    assert r.status_code == 200 and r.json()["alert_count"] >= 1
    r = client.post("/v1/audit/bundle",
                    json={"detections": [
                        {"ring_id": "r1", "members": ["a", "b"], "ring_probability": 0.85}]},
                    headers={"X-Dev-Role": "operator"})
    assert r.status_code == 200 and r.json()["bundle_count"] == 1
