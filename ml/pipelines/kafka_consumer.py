"""Platform-event consumer -> online features -> score -> emit scored events.

Consumes the REAL published nrs.* payment/transaction topics (audit I6 —
the previous defaults `txs.events,filings.events` are published by no
service and would idle forever), unwraps the canonical envelope (legacy
raw maps are upgraded via the consumer shim), builds a small online
feature vector per event, calls the serving tier (HTTP, ML_SERVING_URL)
for fraud/fusion scores, and emits scored events as canonical
`nrs.ml.scored.v1` envelopes.

Topic config:
  ML_CONSUME_TOPICS  override list (default: the payment-ish nrs.* topics)
  ML_LEGACY_TOPICS=1 additionally subscribe to the legacy aliases
                     txs.events,filings.events (pre-audit names)
  ML_SCORED_TOPIC    scored output topic (default nrs.ml.scored.v1)

Dev fallback: when KAFKA_BROKERS is unset or kafka-python is unavailable the
consumer runs in "embedded" mode reading newline-delimited JSON events from
stdin / ML_EVENTS_FILE and writing scored events to ML_SCORED_FILE
(append-only JSONL). Startup never fails on missing prod vars.

Privacy: raw NIN/TIN/MSISDN in events are immediately pseudonymised to
SHA-256 hashes and never logged raw. Amounts remain integer kobo.
"""
from __future__ import annotations

import hashlib
import json
import logging
import os
import sys
import time
import urllib.request
from typing import Optional

try:
    from meridian_events.envelope import new_envelope
    from meridian_events.shim import coerce_envelope, is_canonical_envelope
except ImportError:  # pragma: no cover - package path not set
    new_envelope = None
    is_canonical_envelope = lambda m: False  # noqa: E731
    coerce_envelope = None

logger = logging.getLogger("ml.pipelines.consumer")
logging.basicConfig(level=os.environ.get("LOG_LEVEL", "INFO"))

SERVING_URL = os.environ.get("ML_SERVING_URL", "http://localhost:8090")

# Real published nrs.* topics carrying payment/transaction events.
DEFAULT_TOPICS = [
    "nrs.psm.payments.v1",
    "nrs.psm.remittance.v1",
    "nrs.pos.receipts.v1",
    "nrs.pos.receipt.v1",
    "nrs.mbs.preclearance.v1",
    "nrs.onb.capture.ingested.v1",
]
LEGACY_ALIAS_TOPICS = ["txs.events", "filings.events"]  # pre-audit names (I6)


def consume_topics() -> list[str]:
    topics = [t.strip() for t in os.environ.get(
        "ML_CONSUME_TOPICS", ",".join(DEFAULT_TOPICS)).split(",") if t.strip()]
    if os.environ.get("ML_LEGACY_TOPICS") == "1":
        topics += [t for t in LEGACY_ALIAS_TOPICS if t not in topics]
    return topics


CONSUME_TOPICS = consume_topics()
SCORED_TOPIC = os.environ.get("ML_SCORED_TOPIC", "nrs.ml.scored.v1")
SCORED_FILE = os.environ.get("ML_SCORED_FILE", "ml-scored-events.jsonl")
SCORE_MODEL = os.environ.get("ML_SCORE_MODEL", "fraud")

# topic family -> feature channel (matches build_features one-hot)
_TOPIC_CHANNEL = {
    "psm": "agent",
    "pos": "pos",
    "mbs": "einvoice",
    "onb": "agent",
    "onb.ussd": "ussd",
    "pssp": "pssp",
}


def channel_for_topic(topic: Optional[str]) -> Optional[str]:
    if not topic:
        return None
    fam = topic[4:] if topic.startswith("nrs.") else topic
    for prefix, ch in sorted(_TOPIC_CHANNEL.items(), key=lambda kv: -len(kv[0])):
        if fam.startswith(prefix):
            return ch
    return None

_SENSITIVE_KEYS = ("nin", "tin", "msisdn", "phone", "account_number")


def pseudonymise(event: dict) -> dict:
    """Hash sensitive identifier fields in-place-safe copy. Integer kobo
    amounts are passed through untouched."""
    ev = dict(event)
    for key in list(ev):
        if key.lower() in _SENSITIVE_KEYS and ev[key] is not None:
            ev[f"{key}_hash"] = hashlib.sha256(str(ev[key]).encode()).hexdigest()
            del ev[key]
    return ev


def build_features(event: dict) -> list[float]:
    """Small deterministic online feature vector from a platform event.
    Amounts arrive as integer kobo; log-scaled to a float feature."""
    import math

    amount_kobo = int(event.get("amount_kobo") or event.get("amount") or 0)
    hour = float(event.get("hour", time.gmtime().tm_hour))
    channel = str(event.get("channel", "unknown"))
    channels = ["pos", "agent", "ussd", "einvoice", "pssp"]
    chan_onehot = [1.0 if channel == c else 0.0 for c in channels]
    tx_count_24h = float(event.get("tx_count_24h", 1))
    return [
        math.log1p(max(amount_kobo, 0)),
        hour / 24.0,
        tx_count_24h,
        1.0 if event.get("cross_border") else 0.0,
        1.0 if event.get("dormant_reactivated") else 0.0,
        *chan_onehot,
    ]


def score(features: list[float], entity_hash: str, amount_kobo: Optional[int],
          model: str = SCORE_MODEL, timeout: float = 5.0) -> Optional[dict]:
    payload = json.dumps({
        "entity_id": entity_hash,
        "features": features,
        "amount_kobo": amount_kobo,
    }).encode()
    req = urllib.request.Request(
        f"{SERVING_URL}/v1/score/{model}", data=payload,
        headers={"Content-Type": "application/json", "X-Dev-Role": "pipeline"},
    )
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            return json.loads(resp.read())
    except Exception as exc:
        logger.warning("component=ml-consumer scoring failed model=%s err=%s", model, exc)
        return None


def unwrap_event(event: dict, topic: Optional[str] = None) -> dict:
    """Accept a canonical envelope OR a legacy raw map; return the domain
    payload with envelope context folded in (event id, channel default)."""
    if is_canonical_envelope(event) or (
            isinstance(event, dict) and "data" in event and "type" in event):
        data = dict(event.get("data") or {})
        data.setdefault("id", event.get("id"))
        if "hour" not in data and event.get("time"):
            try:
                data["hour"] = int(str(event["time"])[11:13])
            except (ValueError, IndexError):
                pass
        data.setdefault("channel", channel_for_topic(event.get("type") or topic))
        return data
    if isinstance(event, dict):
        ev = dict(event)
        ev.setdefault("channel", channel_for_topic(topic))
        return ev
    return {}


def process_event(event: dict, topic: Optional[str] = None) -> dict:
    ev = pseudonymise(unwrap_event(event, topic))
    entity_hash = ev.get("tin_hash") or ev.get("entity_id") or hashlib.sha256(
        json.dumps(ev, sort_keys=True, default=str).encode()).hexdigest()
    amount_kobo = ev.get("amount_kobo") or ev.get("amount")
    features = build_features(ev)
    result = score(features, entity_hash, amount_kobo)
    scored = {
        "event_id": ev.get("id") or ev.get("event_id"),
        "entity_hash": entity_hash,
        "model": SCORE_MODEL,
        "score": (result or {}).get("score"),
        "model_version": (result or {}).get("version"),
        "amount_kobo": amount_kobo,
        "scored_at": time.time(),
        "scoring_ok": result is not None,
    }
    logger.info("component=ml-consumer entity=%s score=%s ok=%s",
                entity_hash[:16], scored["score"], scored["scoring_ok"])
    if new_envelope is not None:
        return new_envelope(SCORED_TOPIC, "ml-consumer", scored).to_dict()
    return scored


class _FileSink:
    def __init__(self, path: str = SCORED_FILE):
        self.path = path

    def send(self, record: dict) -> None:
        with open(self.path, "a", encoding="utf-8") as fh:
            fh.write(json.dumps(record) + "\n")


def _kafka_sink():
    from kafka import KafkaProducer

    producer = KafkaProducer(
        bootstrap_servers=os.environ["KAFKA_BROKERS"].split(","),
        value_serializer=lambda v: json.dumps(v).encode("utf-8"),
    )

    def send(record: dict) -> None:
        producer.send(SCORED_TOPIC, record)

    return send


def run() -> None:  # pragma: no cover - long-running loop
    brokers = os.environ.get("KAFKA_BROKERS")
    if brokers:
        try:
            from kafka import KafkaConsumer

            consumer = KafkaConsumer(
                *CONSUME_TOPICS,
                bootstrap_servers=brokers.split(","),
                value_deserializer=lambda b: json.loads(b.decode("utf-8")),
                group_id=os.environ.get("ML_CONSUMER_GROUP", "ml-scoring"),
                enable_auto_commit=True,
            )
            send = _kafka_sink()
            logger.info("profile=prod component=ml-consumer topics=%s", CONSUME_TOPICS)
            for msg in consumer:
                send(process_event(msg.value, topic=msg.topic))
            return
        except ImportError:
            logger.warning("component=ml-consumer kafka-python missing; embedded mode")

    logger.info("profile=dev component=ml-consumer source=stdin sink=%s", SCORED_FILE)
    sink = _FileSink()
    source = open(os.environ["ML_EVENTS_FILE"]) if os.environ.get("ML_EVENTS_FILE") else sys.stdin
    with source:
        for line in source:
            line = line.strip()
            if not line:
                continue
            try:
                rec = json.loads(line)
                sink.send(process_event(rec, topic=rec.get("type") or rec.get("topic")))
            except json.JSONDecodeError:
                logger.warning("component=ml-consumer skipping malformed event line")


if __name__ == "__main__":  # pragma: no cover
    run()
