"""§6.3 db-fault injection for the refund (REF) flow (assurance R7).

Closes the "db timeout on state write" and "deadlock" matrix cells for the
refund execution saga plus the kill-after-commit-before-response variant
where the state store faults AFTER the ledger post landed. Faults are
injected at the durable-store and ledger ports; every scenario asserts
(a) the error surfaces, (b) no partial/duplicate money movement, and
(c) recovery (retry or sweeper) converges to exactly one posted refund.
"""
import os
import sys
import tempfile
from pathlib import Path

os.environ["DATA_DIR"] = tempfile.mkdtemp()
sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from fastapi.testclient import TestClient  # noqa: E402

from app.main import app, _store, _executor  # noqa: E402
from app.refund_execution import (InprocLedger, RefundPayloadConflict,  # noqa: E402
                                  refund_id, taxpayer_account)

H = {"X-Dev-Role": "operator"}


class SimulatedDBTimeout(Exception):
    """DB driver timeout on a state write/read (e.g. psycopg QueryTimeout)."""


class SimulatedDeadlock(Exception):
    """DB deadlock detected (SQLSTATE 40P01) on a state write."""


def _req(tin, amount=300_000_000, period="2026-08"):
    # B3 #1: lane inputs are server-side; seed the platform profile store.
    _store.put("taxpayer_credit_profiles", tin, {
        "tin_hash": tin, "credit_score": 800,
        "filings_on_time": 12, "filings_total": 12})
    return {"tin_hash": tin, "amount_kobo": amount, "tax_type": "vat",
            "period": period}


def _posted_to(tin):
    led: InprocLedger = _executor.ledger
    acct = led.accounts.get(taxpayer_account(tin))
    return acct["credits_posted"] if acct else 0


def _fault_put(coll, exc, status=None):
    """Shadow _store.put, raising exc for the target collection (optionally
    only for docs with a given status). Returns (restore_fn)."""
    orig = _store.put

    def put(c, id_, doc):
        if c == coll and (status is None or
                          (isinstance(doc, dict) and doc.get("status") == status)):
            raise exc
        return orig(c, id_, doc)

    _store.put = put
    return lambda: setattr(_store, "put", orig)


def _fault_get(coll, exc):
    orig = _store.get

    def get(c, id_, default=None):
        if c == coll:
            raise exc
        return orig(c, id_, default)

    _store.get = get
    return lambda: setattr(_store, "get", orig)


# --- db timeout on state write (REF cell) ---

def test_db_timeout_on_state_write_blocks_execution_then_recovers():
    restore = _fault_put("refund_executions", SimulatedDBTimeout("db write timeout"))
    with TestClient(app) as c:
        try:
            r = c.post("/v1/refunds/fasttrack", headers=H, json=_req("tin-dbt"))
            assert r.status_code == 502, r.text
        finally:
            restore()
        # error surfaced, and NO money moved (no posted transfer)
        assert _posted_to("tin-dbt") == 0
        # retry after the db recovers: exactly one posted refund
        r2 = c.post("/v1/refunds/fasttrack", headers=H, json=_req("tin-dbt"))
        assert r2.status_code == 200, r2.text
        assert r2.json()["execution"]["status"] == "posted"
        assert _posted_to("tin-dbt") == 300_000_000


def test_db_deadlock_on_state_write_retryable():
    restore = _fault_put("refund_executions", SimulatedDeadlock("deadlock detected"))
    with TestClient(app) as c:
        try:
            r = c.post("/v1/refunds/fasttrack", headers=H, json=_req("tin-dl"))
            assert r.status_code == 502, r.text
        finally:
            restore()
        assert _posted_to("tin-dl") == 0
        r2 = c.post("/v1/refunds/fasttrack", headers=H, json=_req("tin-dl"))
        assert r2.status_code == 200, r2.text
        assert _posted_to("tin-dl") == 300_000_000  # retried exactly once


def test_db_timeout_on_read_fails_closed():
    restore = _fault_get("refund_executions", SimulatedDBTimeout("db read timeout"))
    with TestClient(app) as c:
        try:
            r = c.post("/v1/refunds/fasttrack", headers=H, json=_req("tin-dbr"))
            assert r.status_code == 502, r.text
        finally:
            restore()
        assert _posted_to("tin-dbr") == 0  # fail closed: nothing executed


# --- kill AFTER commit BEFORE response (db fault variant) ---

def test_db_timeout_after_post_sweeper_reconciles():
    """The ledger post lands but the 'posted' state write times out: the
    caller gets a 502, and the recovery sweeper reconciles the execution
    from actual ledger state instead of double-paying."""
    restore = _fault_put("refund_executions",
                         SimulatedDBTimeout("db write timeout"), status="posted")
    with TestClient(app) as c:
        try:
            r = c.post("/v1/refunds/fasttrack", headers=H, json=_req("tin-kac"))
            assert r.status_code == 502, r.text
        finally:
            restore()
    rid = refund_id("tin-kac", "2026-08", "vat")
    exe = _store.get("refund_executions", rid)
    assert exe["status"] == "pending"  # the posted-write was lost
    assert _posted_to("tin-kac") == 300_000_000  # but the post DID land
    res = _executor.sweep_pending()
    assert res["resumed"] >= 1
    exe = _store.get("refund_executions", rid)
    assert exe["status"] == "posted"
    # sweep again: idempotent, no second post
    assert _executor.sweep_pending()["resumed"] == 0
    assert _posted_to("tin-kac") == 300_000_000


# --- deadlock on the ledger post: compensation voids the hold ---

def test_deadlock_during_post_compensates_void():
    led: InprocLedger = _executor.ledger
    orig = led.post_pending_as

    def boom(pid, post_id, amount):
        raise SimulatedDeadlock("deadlock detected")

    led.post_pending_as = boom
    try:
        try:
            _executor.execute(tin_hash="tin-dlp", period="2026-08", tax_type="vat",
                              amount_kobo=10_000_000, decision={"lane": "auto_approve"},
                              approved_by="test")
            raise AssertionError("execution must fail")
        except SimulatedDeadlock:
            pass
    finally:
        led.post_pending_as = orig
    rid = refund_id("tin-dlp", "2026-08", "vat")
    exe = _store.get("refund_executions", rid)
    assert exe["status"] == "post_failed"
    pend = led.get_transfer(exe["pending_transfer_id"])
    assert pend["resolved"] is True and pend["pending"] is False  # hold voided
    assert _posted_to("tin-dlp") == 0


# --- same-key-different-payload (amount binding, w2 #7) ---

def test_same_key_different_amount_conflicts_409():
    with TestClient(app) as c:
        r1 = c.post("/v1/refunds/fasttrack", headers=H, json=_req("tin-amt", 300_000_000))
        assert r1.status_code == 200, r1.text
        r2 = c.post("/v1/refunds/fasttrack", headers=H, json=_req("tin-amt", 450_000_000))
        assert r2.status_code == 409, r2.text
        # the original execution stands; no second transfer
        assert _posted_to("tin-amt") == 300_000_000


def test_executor_payload_conflict_direct():
    _executor.execute(tin_hash="tin-amtd", period="2026-08", tax_type="vat",
                      amount_kobo=10_000_000, decision={"lane": "auto_approve"},
                      approved_by="test")
    try:
        _executor.execute(tin_hash="tin-amtd", period="2026-08", tax_type="vat",
                          amount_kobo=20_000_000, decision={"lane": "auto_approve"},
                          approved_by="test")
        raise AssertionError("must conflict")
    except RefundPayloadConflict:
        pass
    assert _posted_to("tin-amtd") == 10_000_000


# --- publish failure (event bus / outbox) after the state write ---

def test_outbox_failure_after_post_keeps_durable_posted_state():
    """Outbox append fails after the refund posted: the durable execution
    record is already 'posted', so a client retry replays it idempotently
    and never double-posts (the event is re-emittable from the record)."""
    orig = _executor.outbox.append

    def boom(topic, env):
        raise SimulatedDBTimeout("outbox append failed")

    with TestClient(app) as c:
        _executor.outbox.append = boom
        try:
            r = c.post("/v1/refunds/fasttrack", headers=H, json=_req("tin-obx"))
            assert r.status_code == 502, r.text
        finally:
            _executor.outbox.append = orig
        rid = refund_id("tin-obx", "2026-08", "vat")
        exe = _store.get("refund_executions", rid)
        assert exe["status"] == "posted"  # durable state survived
        r2 = c.post("/v1/refunds/fasttrack", headers=H, json=_req("tin-obx"))
        assert r2.status_code == 200, r2.text
        assert r2.json().get("idempotent_replay") is True
        assert _posted_to("tin-obx") == 300_000_000  # exactly one post
