"""F2: refund EXECUTION behind the fast-track decision (audit Flow 2).

The fast-track endpoint used to be a decision function only — "an approved
refund is a JSON document". This module executes approved refunds as a real
funds flow:

  decision -> pending TB transfer (refund treasury -> taxpayer) -> post
           -> compensation voids the pending when the post fails

Idempotency: one refund per (tin_hash, period, tax_type) — the refund id is
a deterministic hash and every ledger leg uses deterministic transfer ids,
so a double-submit replays the stored execution and never moves money
twice. A crash after the pending transfer is resumed by the sweeper
(sweep_pending), which posts or voids from actual ledger state.

Ledger port: LEDGER_URL selects the core ledger REST service (prod);
otherwise an in-process TB-semantics dev ledger (dev default, honestly
tagged in responses as profile=dev).
"""
from __future__ import annotations

import hashlib
import os
import time
import urllib.request
import json
from typing import Any, Protocol

REFUND_LEDGER = 400  # pssp_recon ledger hosts refund treasury/taxpayer accounts
NS_REFUND_TREASURY = 400_000_000_001
NS_REFUND_TAXPAYER_BASE = 400_000_100_000


def deterministic_id(seed: str) -> str:
    return hashlib.sha256(seed.encode()).hexdigest()[:32]


class RefundPayloadConflict(Exception):
    """The deterministic refund key (tin_hash, period, tax_type) was reused
    with a DIFFERENT amount. The refund id intentionally excludes the
    amount (w2 #7), so the executor itself must bind the payload: a
    mismatched replay is rejected (409-class), never silently served the
    original execution."""


def refund_id(tin_hash: str, period: str, tax_type: str | None) -> str:
    return "ref-" + deterministic_id(f"refund:{tin_hash}:{period}:{tax_type or 'any'}")[:24]


def _account_id(namespace: int, serial: int) -> str:
    return f"{namespace:016x}{serial:016x}"


def taxpayer_account(tin_hash: str) -> str:
    serial = int(hashlib.sha256(("taxpayer:" + tin_hash).encode()).hexdigest()[:12], 16)
    return _account_id(NS_REFUND_TAXPAYER_BASE, serial & 0x0000_FFFFFFFFFFFF)


def treasury_account() -> str:
    return _account_id(NS_REFUND_TREASURY, 1)


# ---------------------------------------------------------------------------
# Ledger ports
# ---------------------------------------------------------------------------

class LedgerPort(Protocol):
    def create_pending(self, t: dict) -> None: ...
    def post_pending_as(self, pending_id: str, post_id: str, amount: int) -> None: ...
    def void_pending(self, pending_id: str) -> None: ...
    def get_transfer(self, transfer_id: str) -> dict | None: ...
    def ensure_account(self, account_id: str, code: int, flags: int, user_data: str) -> None: ...


class InprocLedger:
    """Dev-default in-process ledger with TigerBeetle semantics: pending /
    post / void, dedup on client-supplied transfer ids (replay returns the
    existing transfer), DEBITS_MUST_NOT_EXCEED_CREDITS on the treasury."""

    FLAG_DEBITS_NOT_EXCEED_CREDITS = 1

    def __init__(self) -> None:
        self.accounts: dict[str, dict] = {}
        self.transfers: dict[str, dict] = {}

    def ensure_account(self, account_id: str, code: int, flags: int, user_data: str) -> None:
        self.accounts.setdefault(account_id, {
            "id": account_id, "code": code, "flags": flags, "user_data": user_data,
            "debits_posted": 0, "credits_posted": 0,
            "debits_pending": 0, "credits_pending": 0})

    def _check(self, acct: dict, d_post: int, c_post: int, d_pend: int = 0, c_pend: int = 0) -> None:
        if acct["flags"] & self.FLAG_DEBITS_NOT_EXCEED_CREDITS:
            if acct["debits_posted"] + acct["debits_pending"] + d_post + d_pend > \
                    acct["credits_posted"] + c_post:
                raise ValueError("ledger: debits_must_not_exceed_credits violated")

    def create_pending(self, t: dict) -> None:
        if t["id"] in self.transfers:
            return  # idempotent replay
        dr, cr = self.accounts[t["debit"]], self.accounts[t["credit"]]
        self._check(dr, 0, 0, d_pend=t["amount_kobo"])
        self._check(cr, 0, 0, c_pend=t["amount_kobo"])
        dr["debits_pending"] += t["amount_kobo"]
        cr["credits_pending"] += t["amount_kobo"]
        self.transfers[t["id"]] = {**t, "pending": True, "resolved": False}

    def post_pending_as(self, pending_id: str, post_id: str, amount: int) -> None:
        pt = self.transfers.get(pending_id)
        if pt is None:
            raise ValueError("ledger: pending transfer not found")
        if pt["resolved"]:
            post = self.transfers.get(post_id)
            if post and post["amount_kobo"] == amount:
                return  # idempotent replay
            raise ValueError("ledger: transfer is not pending")
        dr, cr = self.accounts[pt["debit"]], self.accounts[pt["credit"]]
        dr["debits_pending"] -= pt["amount_kobo"]
        cr["credits_pending"] -= pt["amount_kobo"]
        self._check(dr, amount, 0)
        self._check(cr, 0, amount)
        dr["debits_posted"] += amount
        cr["credits_posted"] += amount
        pt["resolved"] = True
        self.transfers[post_id] = {"id": post_id, "debit": pt["debit"], "credit": pt["credit"],
                                   "amount_kobo": amount, "code": 2, "pending": False, "resolved": True}

    def void_pending(self, pending_id: str) -> None:
        pt = self.transfers.get(pending_id)
        if pt is None or pt["resolved"]:
            return  # idempotent
        self.accounts[pt["debit"]]["debits_pending"] -= pt["amount_kobo"]
        self.accounts[pt["credit"]]["credits_pending"] -= pt["amount_kobo"]
        pt["resolved"] = True
        pt["pending"] = False

    def get_transfer(self, transfer_id: str) -> dict | None:
        return self.transfers.get(transfer_id)


class HTTPLedger:
    """Core ledger REST service client (prod profile; LEDGER_URL)."""

    def __init__(self, base: str) -> None:
        self.base = base.rstrip("/")

    def _call(self, method: str, path: str, body: dict | None = None) -> dict:
        req = urllib.request.Request(
            self.base + path, method=method,
            data=json.dumps(body).encode() if body is not None else None,
            headers={"Content-Type": "application/json", "X-Dev-Role": "operator"})
        with urllib.request.urlopen(req, timeout=10) as resp:  # noqa: S310
            return json.loads(resp.read() or b"{}")

    def ensure_account(self, account_id: str, code: int, flags: int, user_data: str) -> None:
        self._call("POST", "/v1/accounts", {
            "namespace": REFUND_LEDGER, "id": account_id, "code": code,
            "flags": flags, "user_data": user_data})

    def create_pending(self, t: dict) -> None:
        self._call("POST", "/v1/transfers/pending", {
            "id": t["id"], "debit_account_id": t["debit"], "credit_account_id": t["credit"],
            "amount_kobo": t["amount_kobo"], "ledger": REFUND_LEDGER, "code": t.get("code", 1),
            "timeout_seconds": t.get("timeout_seconds", 0)})

    def post_pending_as(self, pending_id: str, post_id: str, amount: int) -> None:
        self._call("POST", f"/v1/transfers/{pending_id}/post",
                   {"amount_kobo": amount, "post_id": post_id})

    def void_pending(self, pending_id: str) -> None:
        self._call("POST", f"/v1/transfers/{pending_id}/void")

    def get_transfer(self, transfer_id: str) -> dict | None:
        try:
            return self._call("GET", f"/v1/transfers/{transfer_id}")
        except Exception:  # noqa: BLE001
            return None


def ledger_from_env() -> LedgerPort:
    url = os.environ.get("LEDGER_URL", "")
    if url:
        return HTTPLedger(url)
    return InprocLedger()


# ---------------------------------------------------------------------------
# Refund execution workflow
# ---------------------------------------------------------------------------

EXEC_PENDING_TTL_SECONDS = int(os.environ.get("REFUND_PENDING_TTL_SECONDS", 1800))


class RefundExecutor:
    """Executes refund decisions as pending -> post ledger sagas with void
    compensation, idempotent per (tin_hash, period, tax_type)."""

    def __init__(self, store: Any, ledger: LedgerPort, outbox: Any | None = None) -> None:
        self.store = store
        self.ledger = ledger
        self.outbox = outbox

    def _accounts(self, tin_hash: str) -> tuple[str, str]:
        tre, tax = treasury_account(), taxpayer_account(tin_hash)
        self.ledger.ensure_account(tre, 1, InprocLedger.FLAG_DEBITS_NOT_EXCEED_CREDITS
                                   if isinstance(self.ledger, InprocLedger) else 1,
                                   "nrs-refund-treasury")
        self.ledger.ensure_account(tax, 1, 0, "refund-taxpayer")
        return tre, tax

    def ledger_transfer(self, debit: str, credit: str, amount_kobo: int, seed: str) -> str:
        """Immediate transfer with a deterministic id (InprocLedger dev
        path; prod funding happens upstream of the workflow)."""
        tid = deterministic_id(seed)
        led = self.ledger
        if tid in led.transfers:
            return tid
        dr, cr = led.accounts[debit], led.accounts[credit]
        led._check(dr, amount_kobo, 0)
        led._check(cr, 0, amount_kobo)
        dr["debits_posted"] += amount_kobo
        cr["credits_posted"] += amount_kobo
        led.transfers[tid] = {"id": tid, "debit": debit, "credit": credit,
                              "amount_kobo": amount_kobo, "code": 4, "pending": False}
        return tid

    def _emit(self, topic: str, payload: dict) -> None:
        """F8: outbox pattern — the event is written with the state change
        (same request); the relay publishes at-least-once. Consumer dedup
        key: payload['dedup_key']."""
        if self.outbox is None:
            return
        from meridian_events.envelope import new_envelope
        self.outbox.append(topic, new_envelope(topic, "settlement", payload))

    def execute(self, *, tin_hash: str, period: str, tax_type: str | None,
                amount_kobo: int, decision: dict, approved_by: str) -> dict:
        rid = refund_id(tin_hash, period, tax_type)
        existing = self.store.get("refund_executions", rid)
        if existing is not None and existing.get("status") in ("posted", "pending"):
            if existing.get("amount_kobo") != amount_kobo:
                raise RefundPayloadConflict(
                    f"refund {rid} already executed for "
                    f"{existing.get('amount_kobo')} kobo; refusing replay with "
                    f"{amount_kobo} kobo under the same (tin, period, tax_type) key")
            return {**existing, "idempotent_replay": True}
        # post_failed: the compensation voided the original pending, so a
        # retry must use a fresh deterministic attempt id (create_pending
        # dedups on the transfer id and a voided id cannot be re-posted).
        # "failed" (pending never created) retries under the original ids.
        attempt = 0
        if existing is not None and existing.get("status") == "post_failed":
            attempt = int(existing.get("attempt", 0)) + 1
        tre, tax = self._accounts(tin_hash)
        # fund the refund treasury from the budget-offset account for this
        # refund (idempotent per refund id; the treasury enforces
        # debits<=credits so unfunded refunds cannot execute)
        offset = _account_id(NS_REFUND_TREASURY, 2)
        self.ledger.ensure_account(offset, 1, 0, "nrs-refund-budget-offset")
        if isinstance(self.ledger, InprocLedger):
            self.ledger_transfer(offset, tre, amount_kobo, "ref-fund:" + rid)
        pend_id = deterministic_id("ref-pend:" + rid) if attempt == 0 \
            else deterministic_id(f"ref-pend:{rid}:{attempt}")
        post_id = deterministic_id("ref-post:" + rid) if attempt == 0 \
            else deterministic_id(f"ref-post:{rid}:{attempt}")
        exe = {
            "refund_id": rid, "tin_hash": tin_hash, "period": period, "tax_type": tax_type,
            "amount_kobo": amount_kobo, "lane": decision.get("lane"), "attempt": attempt,
            "treasury_account": tre, "taxpayer_account": tax,
            "pending_transfer_id": pend_id, "post_transfer_id": post_id,
            "status": "pending", "approved_by": approved_by,
            "created_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        }
        # NOTE: every store write below persists a COPY (dict(exe)) — the
        # executor keeps mutating exe in place, and a failed write must not
        # leave the stored document aliasing later in-memory state (R7:
        # found via db-fault injection on the 'posted' write).
        try:
            self.ledger.create_pending({
                "id": pend_id, "debit": tre, "credit": tax,
                "amount_kobo": amount_kobo, "code": 1,
                "timeout_seconds": EXEC_PENDING_TTL_SECONDS})
        except Exception as exc:  # noqa: BLE001
            exe["status"] = "failed"
            exe["fail_reason"] = f"create pending: {exc}"
            self.store.put("refund_executions", rid, dict(exe))
            raise
        # single durable write binding BOTH transfer ids before the post
        self.store.put("refund_executions", rid, dict(exe))
        return self._finish(rid, exe, amount_kobo)

    def _finish(self, rid: str, exe: dict, amount_kobo: int) -> dict:
        try:
            self.ledger.post_pending_as(exe["pending_transfer_id"], exe["post_transfer_id"], amount_kobo)
        except Exception as exc:  # noqa: BLE001
            # compensation: void the pending hold, never leave it dangling
            self.ledger.void_pending(exe["pending_transfer_id"])
            exe["status"] = "post_failed"
            exe["fail_reason"] = f"post pending: {exc}"
            self.store.put("refund_executions", rid, dict(exe))
            raise
        exe["status"] = "posted"
        exe["posted_at"] = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
        self.store.put("refund_executions", rid, dict(exe))
        self._emit("nrs.refund.executed.v1", {
            "dedup_key": "refund:" + rid, "refund_id": rid, "tin_hash": exe["tin_hash"],
            "amount_kobo": amount_kobo, "period": exe["period"], "tax_type": exe["tax_type"],
            "post_transfer_id": exe["post_transfer_id"]})
        return exe

    def sweep_pending(self) -> dict:
        """Recovery worker: resume or void executions interrupted by a crash
        between the pending transfer and the post (F2 test: crash after
        pending = sweep resumes)."""
        resumed = voided = 0
        for exe in self.store.list("refund_executions"):
            if exe.get("status") != "pending":
                continue
            post = self.ledger.get_transfer(exe["post_transfer_id"])
            pend = self.ledger.get_transfer(exe["pending_transfer_id"])
            if post is not None:
                # the post actually landed before the crash: mark posted
                exe["status"] = "posted"
                exe["posted_at"] = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
                self.store.put("refund_executions", exe["refund_id"], dict(exe))
                resumed += 1
            elif pend is not None and pend.get("pending") and not pend.get("resolved"):
                try:
                    self._finish(exe["refund_id"], exe, exe["amount_kobo"])
                    resumed += 1
                except Exception:  # noqa: BLE001
                    pass
            else:
                exe["status"] = "voided"
                exe["fail_reason"] = "sweeper: pending missing or already resolved"
                self.store.put("refund_executions", exe["refund_id"], dict(exe))
                voided += 1
        return {"resumed": resumed, "voided": voided}
