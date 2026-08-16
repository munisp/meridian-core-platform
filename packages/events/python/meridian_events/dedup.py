"""Generic consumer-side dedup middleware (assurance R7; §6.3 "consumer
crash" / duplicate-delivery cells).

Before this module, dedup was re-implemented per handler and several flows
had no consumer-side dedup at all (audit w2: "no generic processed-event
table — known platform gap"). `DeduplicatingConsumer` wraps any handler
with exactly-once *effect* semantics over an at-least-once bus:

  - same event id + same payload  -> skipped (handler NOT re-executed);
  - same event id + DIFFERENT payload -> DedupConflict (poison; route to
    DLQ/operators, never silently apply);
  - handler raises -> nothing is marked, so the redelivery re-executes
    (crash-before-mark is safe);
  - records carry a TTL so the dedup table does not grow without bound;
    expiry re-opens processing (same policy class as the idempotency TTLs
    on the funds flows).

The store is durable (JsonStore) so a process restart does not reopen the
dedup window — the "delayed restart / recovery" cell.
"""
from __future__ import annotations

import hashlib
import json
import time
from typing import Any, Callable

from .envelope import Envelope
from .store import JsonStore

DEDUP_COLLECTION = "processed_events"
DEFAULT_TTL_SECONDS = 7 * 24 * 3600  # align with the funds-flow idem TTLs


class DedupConflict(Exception):
    """Same event id redelivered with a different payload."""


def payload_hash(data: Any) -> str:
    raw = json.dumps(data, sort_keys=True, separators=(",", ":"), default=str)
    return hashlib.sha256(raw.encode()).hexdigest()


class DeduplicatingConsumer:
    """Wraps a handler with durable processed-event dedup."""

    def __init__(self, store: JsonStore, *, ttl_seconds: int = DEFAULT_TTL_SECONDS,
                 now: Callable[[], float] = time.time) -> None:
        self.store = store
        self.ttl_seconds = ttl_seconds
        self.now = now

    def _lookup(self, event_id: str) -> dict | None:
        rec = self.store.get(DEDUP_COLLECTION, event_id)
        if rec is None:
            return None
        if self.now() - float(rec.get("processed_at_epoch", 0)) > self.ttl_seconds:
            return None  # replay window closed: treat as a fresh event
        return rec

    def already_processed(self, event_id: str) -> bool:
        return self._lookup(event_id) is not None

    def handle(self, env: Envelope, handler: Callable[[Envelope], Any]) -> dict:
        """Execute handler exactly once per (event id, payload).

        Returns a receipt: {"status": "processed"|"duplicate", ...}.
        Raises DedupConflict on same-id/different-payload redelivery.
        """
        h = payload_hash(env.data)
        rec = self._lookup(env.id)
        if rec is not None:
            if rec.get("payload_hash") != h:
                raise DedupConflict(
                    f"event {env.id} redelivered with a different payload")
            return {"status": "duplicate", "event_id": env.id,
                    "processed_at": rec.get("processed_at")}
        result = handler(env)  # crash here => unmarked => redelivery re-executes
        receipt = {
            "event_id": env.id, "type": env.type, "payload_hash": h,
            "processed_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
            "processed_at_epoch": self.now(),
        }
        self.store.put(DEDUP_COLLECTION, env.id, receipt)
        return {"status": "processed", "event_id": env.id, "result": result}

    def purge_expired(self) -> int:
        """Terminal-state-style purge: drop receipts past the TTL."""
        now = self.now()
        expired = [r["event_id"] for r in self.store.list(DEDUP_COLLECTION)
                   if now - float(r.get("processed_at_epoch", 0)) > self.ttl_seconds]
        for eid in expired:
            self.store.delete(DEDUP_COLLECTION, eid)
        return len(expired)


def dedup_handler(store: JsonStore, handler: Callable[[Envelope], Any],
                  *, ttl_seconds: int = DEFAULT_TTL_SECONDS) -> Callable[[Envelope], dict]:
    """Functional wrapper: bus.subscribe(topic, dedup_handler(store, h))."""
    consumer = DeduplicatingConsumer(store, ttl_seconds=ttl_seconds)
    return lambda env: consumer.handle(env, handler)
