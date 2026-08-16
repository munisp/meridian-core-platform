"""R4: refund-decision idempotency TTL — expired key treated as new,
purge removes terminal records only."""
import os
import sys
import tempfile
import time
from pathlib import Path

os.environ["DATA_DIR"] = tempfile.mkdtemp()
sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from app.main import (REFUND_IDEMPOTENCY_TTL_SECONDS, _iso, _store,  # noqa: E402
                      purge_expired_refund_decisions, refund_decision_expired)
from app.refund_execution import refund_id  # noqa: E402


def _decision(rid, lane="auto_approve", decided_at=None):
    return {"refund_id": rid, "tin_hash": "tin-h", "period": "2026-07",
            "tax_type": "vat", "amount_kobo": 100_000, "lane": lane,
            "decided_at": decided_at or _iso(time.time())}


def test_fresh_decision_not_expired():
    assert not refund_decision_expired(_decision("r1"))


def test_expired_decision_with_and_without_expires_at():
    old = _iso(time.time() - 2 * REFUND_IDEMPOTENCY_TTL_SECONDS)
    # legacy record: falls back to decided_at + TTL
    assert refund_decision_expired(_decision("r2", decided_at=old))
    # explicit expires_at in the past
    doc = _decision("r3")
    doc["expires_at"] = _iso(time.time() - 1)
    assert refund_decision_expired(doc)
    # explicit expires_at in the future wins over an old decided_at
    doc = _decision("r4", decided_at=old)
    doc["expires_at"] = _iso(time.time() + 3600)
    assert not refund_decision_expired(doc)


def test_expired_key_treated_as_new_in_fasttrack():
    # an expired stored decision must not suppress a fresh attempt:
    # refund_decision_expired is the exact gate refund_fasttrack applies.
    rid = refund_id("tin-exp", "2026-07", "vat")
    doc = _decision(rid)
    doc["expires_at"] = _iso(time.time() - 1)
    _store.put("refund_decisions", rid, doc)
    prior = _store.get("refund_decisions", rid)
    assert prior is not None and refund_decision_expired(prior)


def test_purge_terminal_only():
    old = _iso(time.time() - 2 * REFUND_IDEMPOTENCY_TTL_SECONDS)
    # expired + terminal (standard lane, no execution) -> purge
    d1 = _decision("rid-std", lane="standard", decided_at=old)
    # expired + terminal (execution posted) -> purge
    d2 = _decision("rid-posted", decided_at=old)
    _store.put("refund_executions", "rid-posted",
               {"refund_id": "rid-posted", "status": "posted"})
    # expired + in-flight (manual_review awaiting approval) -> keep
    d3 = _decision("rid-review", lane="manual_review", decided_at=old)
    # fresh -> keep
    d4 = _decision("rid-fresh", lane="standard")
    for d in (d1, d2, d3, d4):
        _store.put("refund_decisions", d["refund_id"], d)
    n = purge_expired_refund_decisions()
    assert n == 2, n
    remaining = {rid for rid, _ in _store.items("refund_decisions")}
    assert {"rid-review", "rid-fresh"} <= remaining and "rid-std" not in remaining and "rid-posted" not in remaining, remaining
