"""Tests for the unified lakehouse layer, the nrs.*->bronze sink (dedup,
partitioning, backfill, legacy shim), and the ML consumer topic mapping."""
import json
import os
import sys

import pytest

ML_DIR = os.path.join(os.path.dirname(__file__), "..")
REPO_ROOT = os.path.join(ML_DIR, "..")
sys.path.insert(0, ML_DIR)
sys.path.insert(0, REPO_ROOT)
sys.path.insert(0, os.path.join(REPO_ROOT, "packages", "events", "python"))

from meridian_events.envelope import new_envelope  # noqa: E402

from data.lakehouse import ParquetLakehouse, get_lakehouse  # noqa: E402
from pipelines.lakehouse_sink import LakehouseSink, topic_to_dataset  # noqa: E402
from pipelines import kafka_consumer  # noqa: E402


def pay_env(eid_seed: str = "a") -> dict:
    return new_envelope("nrs.psm.payments.v1", "svc-test", {
        "reference": f"ref-{eid_seed}", "amount_kobo": 12500,
        "tin_hash": "a" * 64, "occurred_at": "2025-03-04T10:15:00Z"}).to_dict()


# -- lakehouse layer -------------------------------------------------------------

def test_parquet_lakehouse_catalog(tmp_path):
    lh = ParquetLakehouse(str(tmp_path))
    lh.write("bronze", "psm_payments", [{"id": "1", "amount_kobo": 5}], partition="2025-03-04")
    lh.write("bronze", "psm_payments", [{"id": "2", "amount_kobo": 7, "extra": "x"}],
             partition="2025-03-04")
    cat = json.loads((tmp_path / "catalog.json").read_text())
    tbl = cat["tables"]["bronze.psm_payments"]
    assert len(tbl["snapshots"]) == 2
    assert tbl["schema_version"] == 1  # column set changed once
    assert "extra" in tbl["columns"]
    rows = lh.read("bronze", "psm_payments")
    assert len(rows) == 2
    ds = lh.datasets("bronze")
    assert ds[0]["dataset"] == "psm_payments" and ds[0]["rows"] == 2


def test_get_lakehouse_dev_fallback(tmp_path, monkeypatch):
    monkeypatch.delenv("ICEBERG_REST_URI", raising=False)
    assert isinstance(get_lakehouse(str(tmp_path)), ParquetLakehouse)


def test_iceberg_guard(monkeypatch, tmp_path):
    monkeypatch.setenv("ICEBERG_REST_URI", "http://localhost:8181")
    try:
        import pyiceberg  # noqa: F401
    except ImportError:
        with pytest.raises(RuntimeError, match="pyiceberg"):
            get_lakehouse(str(tmp_path))
    else:
        pytest.skip("pyiceberg installed; needs a live REST catalog")


# -- sink -------------------------------------------------------------------------

def test_topic_to_dataset():
    assert topic_to_dataset("nrs.psm.payments.v1") == "psm_payments"
    assert topic_to_dataset("nrs.onb.ussd.v1") == "onb_ussd"
    assert topic_to_dataset("nrs.mbs.preclearance.v1") == "mbs_preclearance"


def test_sink_writes_bronze_by_topic_and_date(tmp_path):
    sink = LakehouseSink(lakehouse_root=str(tmp_path / "lh"),
                         state_dir=tmp_path / "st")
    r = sink.handle("nrs.psm.payments.v1", pay_env())
    assert r["status"] == "written"
    assert r["partition"] is not None
    ds_dir = tmp_path / "lh" / "bronze" / "psm_payments"
    parts = [d for d in os.listdir(ds_dir) if d.startswith("dt=")]
    assert parts, "bronze partition must exist"
    rows = sink.lh.read("bronze", "psm_payments")
    assert len(rows) == 1
    assert rows[0]["type"] == "nrs.psm.payments.v1"
    # payload preserved + queryable
    assert json.loads(rows[0]["data"])["amount_kobo"] == 12500 or \
        rows[0]["data"]["amount_kobo"] == 12500


def test_sink_dedup_on_event_id(tmp_path):
    sink = LakehouseSink(lakehouse_root=str(tmp_path / "lh"),
                         state_dir=tmp_path / "st")
    env = pay_env()
    assert sink.handle("nrs.psm.payments.v1", env)["status"] == "written"
    assert sink.handle("nrs.psm.payments.v1", env)["status"] == "deduped"
    # dedup survives restart
    sink2 = LakehouseSink(lakehouse_root=str(tmp_path / "lh"),
                          state_dir=tmp_path / "st")
    assert sink2.handle("nrs.psm.payments.v1", env)["status"] == "deduped"
    assert sink2.stats["written"] == 0


def test_sink_upgrades_legacy_ussd_map(tmp_path):
    sink = LakehouseSink(lakehouse_root=str(tmp_path / "lh"),
                         state_dir=tmp_path / "st")
    raw = {"action": "onb.register", "session_id": "s-9", "msisdn": "08030001122"}
    r = sink.handle("nrs.onb.ussd.v1", raw, source_hint="ussd-gateway")
    assert r["status"] == "written"
    assert sink.stats["upgraded"] == 1
    rows = sink.lh.read("bronze", "onb_ussd")
    data = rows[0]["data"]
    if isinstance(data, str):
        data = json.loads(data)
    assert data["upgraded_legacy"] is True
    assert rows[0]["source"] == "ussd-gateway(legacy-shim)"


def test_sink_backfill_from_outbox_dirs(tmp_path):
    # fixture: two service outbox dirs with JSONL records
    for svc, topic in (("ledger", "nrs.ledger.transfers.v1"),
                       ("ussd", "nrs.onb.ussd.v1")):
        d = tmp_path / "outboxes" / svc
        d.mkdir(parents=True)
        recs = []
        if svc == "ledger":
            env = new_envelope(topic, "ledger", {"transfer_id": "t1", "amount_kobo": 100}).to_dict()
            recs.append({"seq": 1, "topic": topic, "envelope": env})
        else:
            recs.append({"seq": 1, "topic": topic,
                         "envelope": {"action": "onb.tin_status", "session_id": "z1"}})
        (d / "outbox.jsonl").write_text("\n".join(json.dumps(r) for r in recs) + "\n")
    sink = LakehouseSink(lakehouse_root=str(tmp_path / "lh"),
                         state_dir=tmp_path / "st")
    result = sink.backfill([tmp_path / "outboxes"])
    assert result["backfilled"] == 2
    assert result["written"] == 2
    assert sink.lh.read("bronze", "ledger_transfers")
    assert sink.lh.read("bronze", "onb_ussd")


# -- consumer topic mapping -------------------------------------------------------

def test_consumer_defaults_to_real_nrs_topics(monkeypatch):
    monkeypatch.delenv("ML_CONSUME_TOPICS", raising=False)
    monkeypatch.delenv("ML_LEGACY_TOPICS", raising=False)
    topics = kafka_consumer.consume_topics()
    assert "nrs.psm.payments.v1" in topics
    assert "txs.events" not in topics and "filings.events" not in topics


def test_consumer_legacy_alias(monkeypatch):
    monkeypatch.delenv("ML_CONSUME_TOPICS", raising=False)
    monkeypatch.setenv("ML_LEGACY_TOPICS", "1")
    topics = kafka_consumer.consume_topics()
    assert "txs.events" in topics and "nrs.psm.payments.v1" in topics


def test_consumer_unwraps_envelope_and_maps_channel():
    env = new_envelope("nrs.pos.receipts.v1", "pos-vat",
                       {"amount_kobo": 9900, "tin": "123"}).to_dict()
    data = kafka_consumer.unwrap_event(env, topic="nrs.pos.receipts.v1")
    assert data["channel"] == "pos"
    assert data["amount_kobo"] == 9900
    assert data["id"] == env["id"]
    ev = kafka_consumer.pseudonymise(data)
    assert "tin" not in ev and len(ev["tin_hash"]) == 64


def test_consumer_scored_is_envelope():
    kafka_consumer.SERVING_URL = "http://127.0.0.1:1"  # force scoring failure
    out = kafka_consumer.process_event(
        {"amount_kobo": 100, "tin": "x"}, topic="nrs.psm.payments.v1")
    if kafka_consumer.new_envelope is not None:
        assert out["type"] == "nrs.ml.scored.v1"
        assert out["data"]["scoring_ok"] is False
        assert len(out["id"]) == 26
