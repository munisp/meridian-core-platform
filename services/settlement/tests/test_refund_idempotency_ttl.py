"""R4 refund-decision idempotency TTL: replay window + terminal purge."""
from __future__ import annotations

import os
import time

os.environ.setdefault("AUTH_MODE", "dev")
os.environ.setdefault("DATA_DIR", "/tmp/settlement-test-r4-ttl")

import shutil  # noqa: E402

shutil.rmtree(os.environ["DATA_DIR"], ignore_errors=True)

from app.main import (_refund_decision_terminal,  # noqa: E402
                      purge_expired_refund_decisions,
                      refund_decision_expired, _store, _iso)


def _decision(rid: str, lane: str = "manual_review", decided_at: float | None = None,
              expires_at: float | None = None) -> dict:
    decided = time.time() - 100 if decided_at is None else decided_at
    doc = {"refund_id": rid, "lane": lane, "decided_at": _iso(decided)}
    if expires_at is not None:
        doc["expires_at"] = _iso(expires_at)
    _store.put("refund_decisions", rid, doc)
    return doc


def test_replay_window_open_then_closed():
    now = time.time()
    doc = _decision("rid-fresh", decided_at=now, expires_at=now + 3600)
    assert not refund_decision_expired(doc, now)
    assert refund_decision_expired(doc, now + 7200)


def test_legacy_record_falls_back_to_decided_at_plus_ttl():
    from app.main import REFUND_IDEMPOTENCY_TTL_SECONDS
    now = time.time()
    doc = _decision("rid-legacy", decided_at=now - REFUND_IDEMPOTENCY_TTL_SECONDS - 10)
    assert refund_decision_expired(doc, now)


def test_purge_only_terminal_expired():
    now = time.time()
    old = now - 3 * 24 * 3600
    # expired + terminal (rejected standard-lane decision) -> purge
    # (R4 S1a#4: standard lane is now executable via the approval queue,
    # so a PENDING standard decision is in-flight and must be retained)
    d1 = _decision("rid-std", lane="standard", decided_at=old)
    d1["status"] = "rejected"
    _store.put("refund_decisions", "rid-std", d1)
    # expired + posted execution -> purge
    d2 = _decision("rid-posted", lane="auto_approve", decided_at=old)
    d2["execution"] = {"status": "posted"}
    _store.put("refund_decisions", "rid-posted", d2)
    # expired + manual_review pending -> retained
    _decision("rid-pending", lane="manual_review", decided_at=old)
    # fresh manual_review -> retained
    _decision("rid-fresh", lane="manual_review")

    purged = purge_expired_refund_decisions(now)
    assert purged == 2
    assert _store.get("refund_decisions", "rid-std") is None
    assert _store.get("refund_decisions", "rid-posted") is None
    assert _store.get("refund_decisions", "rid-pending") is not None
    assert _store.get("refund_decisions", "rid-fresh") is not None


def test_terminal_helper():
    assert _refund_decision_terminal({"status": "rejected"})
    assert _refund_decision_terminal({"refund_id": "x", "execution": {"status": "posted"}})
    assert not _refund_decision_terminal({"refund_id": "x2", "lane": "manual_review"})
    # R4 S1a#4: a pending standard-lane decision is in-flight, not terminal
    assert not _refund_decision_terminal({"refund_id": "x3", "lane": "standard"})
