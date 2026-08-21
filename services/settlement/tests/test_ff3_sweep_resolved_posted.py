"""FF-3 regression: sweep_pending must not mark a refund "voided" when its
pending transfer resolved as POSTED but the post record is not resolvable
under post_transfer_id (the prod-profile defect: the core ledger HTTP API
ignored post_id and had no single-transfer GET, so get_transfer(post_id)
returned None while money had actually moved)."""
import os
import sys
import tempfile
import time
from pathlib import Path

os.environ["DATA_DIR"] = tempfile.mkdtemp()
sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from meridian_events.store import JsonStore as FileStore  # noqa: E402
from app.refund_execution import RefundExecutor  # noqa: E402


class ResolvedPostedLedger:
    """Mimics the pre-fix core HTTP ledger: the post landed, the pending is
    resolved-as-posted (pending=true, resolved=true), but the post is NOT
    resolvable under the caller's post_id."""

    def __init__(self):
        self.transfers = {}

    def get_transfer(self, transfer_id):
        return self.transfers.get(transfer_id)


def _mk_executor():
    store = FileStore(tempfile.mkdtemp())
    return RefundExecutor(store, ResolvedPostedLedger()), store


def _seed_pending_execution(store, ledger, rid, pend, resolved=True, pending=True):
    ledger.transfers[pend] = {"id": pend, "pending": pending, "resolved": resolved}
    store.put("refund_executions", rid, {
        "refund_id": rid, "tin_hash": "tin-ff3", "period": "2026-08", "tax_type": "vat",
        "amount_kobo": 42_000_000, "status": "pending",
        "pending_transfer_id": pend, "post_transfer_id": "post-" + rid,
        "created_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())})


def test_sweep_marks_resolved_posted_pending_as_posted_not_voided():
    exe, store = _mk_executor()
    _seed_pending_execution(store, exe.ledger, "r-posted", "pend-posted")
    res = exe.sweep_pending()
    assert res["voided"] == 0, "a posted refund must never be swept to voided"
    assert res["resumed"] == 1
    doc = store.get("refund_executions", "r-posted")
    assert doc["status"] == "posted", doc


def test_sweep_still_voids_genuinely_voided_pending():
    exe, store = _mk_executor()
    # voided pendings flip pending=false (ledger void semantics)
    _seed_pending_execution(store, exe.ledger, "r-voided", "pend-voided",
                            resolved=True, pending=False)
    res = exe.sweep_pending()
    assert res["voided"] == 1
    assert store.get("refund_executions", "r-voided")["status"] == "voided"


def test_sweep_still_voids_missing_pending():
    exe, store = _mk_executor()
    _seed_pending_execution(store, exe.ledger, "r-missing", "pend-missing")
    exe.ledger.transfers.clear()  # pending never reached the ledger
    res = exe.sweep_pending()
    assert res["voided"] == 1
    assert store.get("refund_executions", "r-missing")["status"] == "voided"
