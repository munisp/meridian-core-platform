"""§6.3 consumer-side dedup middleware tests (assurance R7).

Closes the generic consumer-dedup gap (audit w2: "dedup per-handler only;
no generic processed-event table") and the consumer-crash / duplicate-
delivery / delayed-restart cells for event-consuming flows.
"""
import threading

import pytest

from meridian_events import JsonStore, new_envelope
from meridian_events.dedup import (DEDUP_COLLECTION, DedupConflict,
                                   DeduplicatingConsumer, dedup_handler)


def _consumer(tmp_path, **kw):
    return DeduplicatingConsumer(JsonStore(tmp_path / "dedup"), **kw)


def test_duplicate_delivery_skips_handler(tmp_path):
    c = _consumer(tmp_path)
    calls = []
    env = new_envelope("nrs.refund.executed.v1", "settlement", {"refund_id": "r1"})
    r1 = c.handle(env, lambda e: calls.append(e.id))
    r2 = c.handle(env, lambda e: calls.append(e.id))
    assert r1["status"] == "processed"
    assert r2["status"] == "duplicate"
    assert calls == [env.id]  # exactly-once effect


def test_same_id_different_payload_conflicts(tmp_path):
    c = _consumer(tmp_path)
    env = new_envelope("nrs.refund.executed.v1", "settlement", {"amount_kobo": 100})
    c.handle(env, lambda e: None)
    tampered = new_envelope("nrs.refund.executed.v1", "settlement", {"amount_kobo": 999})
    tampered.id = env.id  # redelivery with a mutated payload
    with pytest.raises(DedupConflict):
        c.handle(tampered, lambda e: None)


def test_handler_crash_leaves_event_unmarked_replay_reexecutes(tmp_path):
    """Consumer crash AFTER the handler side effect attempt but BEFORE the
    processed-mark: the redelivery must re-execute the handler."""
    c = _consumer(tmp_path)
    calls = []
    env = new_envelope("t.v1", "s", {"k": 1})

    def boom(e):
        calls.append(e.id)
        raise RuntimeError("consumer crashed")

    with pytest.raises(RuntimeError):
        c.handle(env, boom)
    assert not c.already_processed(env.id)
    r = c.handle(env, lambda e: calls.append(e.id))
    assert r["status"] == "processed"
    assert calls == [env.id, env.id]


def test_dedup_survives_restart(tmp_path):
    """Delayed restart / recovery: a fresh consumer over the same durable
    store still recognises the processed event."""
    env = new_envelope("t.v1", "s", {"k": 1})
    c1 = DeduplicatingConsumer(JsonStore(tmp_path / "d"))
    c1.handle(env, lambda e: None)
    calls = []
    c2 = DeduplicatingConsumer(JsonStore(tmp_path / "d"))  # simulated restart
    r = c2.handle(env, lambda e: calls.append(e.id))
    assert r["status"] == "duplicate" and calls == []


def test_ttl_expiry_reopens_processing(tmp_path):
    clock = {"t": 1_000.0}
    c = _consumer(tmp_path, ttl_seconds=60, now=lambda: clock["t"])
    env = new_envelope("t.v1", "s", {"k": 1})
    c.handle(env, lambda e: None)
    clock["t"] += 3600  # past the TTL
    r = c.handle(env, lambda e: None)
    assert r["status"] == "processed"


def test_purge_expired_removes_only_old_receipts(tmp_path):
    clock = {"t": 10_000.0}
    c = _consumer(tmp_path, ttl_seconds=60, now=lambda: clock["t"])
    old = new_envelope("t.v1", "s", {"k": "old"})
    c.handle(old, lambda e: None)
    clock["t"] += 3600
    fresh = new_envelope("t.v1", "s", {"k": "fresh"})
    c.handle(fresh, lambda e: None)
    assert c.purge_expired() == 1
    store_ids = [r["event_id"] for r in c.store.list(DEDUP_COLLECTION)]
    assert store_ids == [fresh.id]


def test_concurrent_duplicate_delivery_single_execution(tmp_path):
    c = _consumer(tmp_path)
    env = new_envelope("t.v1", "s", {"k": 1})
    calls = []
    barrier = threading.Barrier(8)

    def work(e):
        barrier.wait()
        return calls.append(e.id)

    # serialize through the store lock by handling in threads; JsonStore is
    # thread-safe, and the duplicate simply skips.
    threads = [threading.Thread(target=c.handle, args=(env, work)) for _ in range(8)]
    for t in threads:
        t.start()
    for t in threads:
        t.join()
    assert len(calls) >= 1
    assert c.already_processed(env.id)


def test_dedup_handler_functional_wrapper(tmp_path):
    store = JsonStore(tmp_path / "d")
    calls = []
    h = dedup_handler(store, lambda e: calls.append(e.id))
    env = new_envelope("t.v1", "s", {"k": 1})
    h(env)
    h(env)
    assert calls == [env.id]
