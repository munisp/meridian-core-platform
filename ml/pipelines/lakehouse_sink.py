"""nrs.* -> bronze lakehouse sink (audit I1/I6).

Subscribes to the ACTUAL published nrs.* topics (the schemareg catalog,
packages/events/schemareg/topics.json), validates every envelope against
its registered JSON Schema, upgrades legacy raw maps at the edge (consumer
shim, N6), dedups on envelope id, and appends to bronze tables by
topic/date:

    <lakehouse>/bronze/<dataset>/dt=<YYYY-MM-DD>/part-*.parquet
    (dataset = topic family: nrs.psm.payments.v1 -> psm_payments)

Backend: ml.data.lakehouse.get_lakehouse — real Iceberg REST catalog when
ICEBERG_REST_URI is set, parquet+catalog.json dev fallback otherwise.

Modes:
  prod  : KAFKA_BROKERS set + kafka-python installed -> Kafka consumer group
  dev   : reads JSONL events from ML_EVENTS_FILE or stdin
  backfill: --backfill DIR [DIR...] replays service outbox.jsonl files
            ({seq, topic, envelope} records) through the same pipeline.

Dedup state: <state_dir>/seen-ids.jsonl (append-only; loaded on start).
Validation: PROFILE=prod rejects unregistered/invalid events to the DLQ
file (<state_dir>/sink.dlq.jsonl); dev logs a warning and still writes
(audit: dev=warn, prod=reject).
"""
from __future__ import annotations

import json
import logging
import os
import sys
from pathlib import Path
from typing import Any, Iterable

from meridian_events.envelope import Envelope
from meridian_events.schemareg import (Registry, UnregisteredTopicError,
                                       ValidationFailedError)
from meridian_events.shim import coerce_envelope, is_canonical_envelope

try:  # repo-root invocation (python -m ml.pipelines.lakehouse_sink)
    from ml.data.lakehouse import Lakehouse, get_lakehouse
except ImportError:  # ml/ on sys.path (tests, in-ml runs)
    from data.lakehouse import Lakehouse, get_lakehouse

log = logging.getLogger("ml.pipelines.lakehouse_sink")
logging.basicConfig(level=os.environ.get("LOG_LEVEL", "INFO"),
                    format="%(message)s")

STATE_DIR = Path(os.environ.get("ML_SINK_STATE_DIR", "./data/sink-state"))
PROD = os.environ.get("PROFILE") == "prod"


def topic_to_dataset(topic: str) -> str:
    """nrs.psm.payments.v1 -> psm_payments (topic family, dots->underscores)."""
    t = topic
    if t.startswith("nrs."):
        t = t[4:]
    if t.rsplit(".v", 1)[-1].isdigit():
        t = t.rsplit(".v", 1)[0]
    return t.replace(".", "_").replace("-", "_")


class SeenIds:
    """Persistent dedup set on envelope id (survives sink restarts)."""

    def __init__(self, state_dir: Path) -> None:
        self.path = state_dir / "seen-ids.jsonl"
        state_dir.mkdir(parents=True, exist_ok=True)
        self._ids: set[str] = set()
        if self.path.exists():
            with self.path.open() as fh:
                for line in fh:
                    line = line.strip()
                    if line:
                        self._ids.add(line)

    def check_and_add(self, event_id: str) -> bool:
        """True if the id is new (and now recorded)."""
        if event_id in self._ids:
            return False
        self._ids.add(event_id)
        with self.path.open("a") as fh:
            fh.write(event_id + "\n")
        return True


class LakehouseSink:
    def __init__(self, lakehouse: Lakehouse | None = None,
                 registry: Registry | None = None,
                 state_dir: Path = STATE_DIR,
                 lakehouse_root: str = "./data/lakehouse") -> None:
        self.registry = registry or Registry()
        self.lh = lakehouse or get_lakehouse(lakehouse_root)
        self.seen = SeenIds(state_dir)
        self.dlq_path = state_dir / "sink.dlq.jsonl"
        self.stats = {"written": 0, "deduped": 0, "dlq": 0, "upgraded": 0}

    # -- single message ------------------------------------------------------
    def handle(self, topic: str, msg: Any, *, source_hint: str = "unknown") -> dict:
        """Validate/dedup/write one message. Returns a per-event receipt."""
        upgraded = not is_canonical_envelope(msg)
        try:
            env = coerce_envelope(topic, msg, source_hint=source_hint)
        except TypeError as exc:
            return self._dlq(topic, msg, f"uncoercible: {exc}")
        if upgraded:
            self.stats["upgraded"] += 1

        try:
            self.registry.validate_envelope(env)
        except UnregisteredTopicError as exc:
            if PROD:
                return self._dlq(topic, env.to_dict(), f"unregistered: {exc}")
            log.warning("profile=dev component=sink unregistered topic=%s (writing anyway)", topic)
        except ValidationFailedError as exc:
            if PROD:
                return self._dlq(topic, env.to_dict(), f"invalid: {exc}")
            log.warning("profile=dev component=sink invalid envelope topic=%s: %s", topic, exc)

        if not self.seen.check_and_add(env.id):
            self.stats["deduped"] += 1
            return {"topic": topic, "id": env.id, "status": "deduped"}

        day = (env.time or "")[:10] or None
        record = {
            "id": env.id, "type": env.type, "source": env.source,
            "time": env.time, "tenant_id": env.tenant_id,
            "trace_id": env.trace_id,
            "rule_pack_version": env.rule_pack_version,
            "data": env.data,
        }
        receipt = self.lh.write("bronze", topic_to_dataset(topic), [record],
                                partition=day)
        self.stats["written"] += 1
        return {"topic": topic, "id": env.id, "status": "written",
                "partition": receipt["partition"]}

    def _dlq(self, topic: str, msg: Any, reason: str) -> dict:
        self.stats["dlq"] += 1
        with self.dlq_path.open("a") as fh:
            fh.write(json.dumps({"topic": topic, "reason": reason,
                                 "message": msg}, default=str) + "\n")
        log.warning("component=sink dlq topic=%s reason=%s", topic, reason)
        return {"topic": topic, "status": "dlq", "reason": reason}

    # -- batch sources ---------------------------------------------------------
    def handle_batch(self, items: Iterable[tuple[str, Any]],
                     *, source_hint: str = "unknown") -> list[dict]:
        return [self.handle(t, m, source_hint=source_hint) for t, m in items]

    def backfill(self, outbox_dirs: Iterable[str | os.PathLike]) -> dict:
        """Replay dev file-outbox JSONL dirs through the sink (backfill mode)."""
        n = 0
        for d in outbox_dirs:
            d = Path(d)
            files = [d] if d.is_file() else sorted(d.rglob("outbox.jsonl"))
            for f in files:
                hint = f"backfill:{f.parent.name}"
                with f.open() as fh:
                    for line in fh:
                        line = line.strip()
                        if not line:
                            continue
                        try:
                            rec = json.loads(line)
                        except json.JSONDecodeError:
                            continue
                        topic = rec.get("topic")
                        msg = rec.get("envelope", rec)
                        if not topic:
                            continue
                        self.handle(topic, msg, source_hint=hint)
                        n += 1
        return {"backfilled": n, **self.stats}


# -- consumers -----------------------------------------------------------------

def _topics(registry: Registry) -> list[str]:
    override = os.environ.get("ML_SINK_TOPICS")
    if override:
        return [t.strip() for t in override.split(",") if t.strip()]
    return registry.topics()


def run_kafka(sink: LakehouseSink, topics: list[str]) -> None:  # pragma: no cover
    from kafka import KafkaConsumer
    consumer = KafkaConsumer(
        *topics,
        bootstrap_servers=os.environ["KAFKA_BROKERS"].split(","),
        value_deserializer=lambda b: json.loads(b.decode("utf-8")),
        group_id=os.environ.get("ML_SINK_GROUP", "lakehouse-bronze-sink"),
        enable_auto_commit=True,
    )
    log.info("profile=prod component=sink topics=%d", len(topics))
    for msg in consumer:
        sink.handle(msg.topic, msg.value, source_hint="kafka")


def run_embedded(sink: LakehouseSink) -> None:  # pragma: no cover
    source = open(os.environ["ML_EVENTS_FILE"]) if os.environ.get("ML_EVENTS_FILE") else sys.stdin
    log.info("profile=dev component=sink source=stdin/file")
    with source:
        for line in source:
            line = line.strip()
            if not line:
                continue
            try:
                rec = json.loads(line)
            except json.JSONDecodeError:
                continue
            topic = rec.get("topic") or rec.get("type")
            if not topic:
                continue
            sink.handle(topic, rec.get("envelope", rec), source_hint="embedded")


def main(argv: list[str] | None = None) -> None:  # pragma: no cover
    import argparse
    p = argparse.ArgumentParser(description="nrs.* -> bronze lakehouse sink")
    p.add_argument("--backfill", nargs="+", metavar="DIR",
                   help="replay outbox.jsonl dirs then exit")
    p.add_argument("--lakehouse", default=os.environ.get("ML_LAKEHOUSE_DIR", "./data/lakehouse"))
    args = p.parse_args(argv)

    sink = LakehouseSink(lakehouse_root=args.lakehouse)
    if args.backfill:
        log.info("component=sink mode=backfill dirs=%s", args.backfill)
        print(json.dumps(sink.backfill(args.backfill), indent=2))
        return
    topics = _topics(sink.registry)
    if os.environ.get("KAFKA_BROKERS"):
        try:
            run_kafka(sink, topics)
            return
        except ImportError:
            log.warning("component=sink kafka-python missing; embedded mode")
    run_embedded(sink)


if __name__ == "__main__":  # pragma: no cover
    main()
