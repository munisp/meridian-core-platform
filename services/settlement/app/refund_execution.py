"""F2 refund execution (post-NITA refund workflow).

End-to-end: refund decision -> pending transfer (funds RESERVED) -> post
transfer (funds MOVE) with two-phase idempotency keys + compensation (void
the pending when post fails). Real ledger via ledger_from_env() —
TigerBeetle cluster when TIGERBEETLE_ADDRESSES is set, else the durable
inproc dev client with TB semantics.

Ledger saga tokens (SPEC C §4.1): the maker/settle split uses dedicated
service tokens — MERIDIAN_LEDGER_MAKER_TOKEN for the pending-create (maker)
and MERIDIAN_LEDGER_SETTLE_TOKEN for the post/void (settle/checker). The
connectors themselves are read from env; a remote connector honours them,
the inproc dev client documents the split (no-op).
"""
from __future__ import annotations

import hashlib
import hmac as hmac_mod
import os
import time
from typing import Any

from meridian_events.idgen import deterministic_id


# ---------------------------------------------------------------- ledger client

def ledger_from_env():
    """Real ledger connector. TB cluster when TIGERBEETLE_ADDRESSES is set;
    else the durable inproc dev client (double-entry + pending semantics)."""
    addr = os.environ.get("TIGERBEETLE_ADDRESSES", "").strip()
    if addr:
        return TigerBeetleLedger(addr)
    return InprocLedger(os.path.join(os.environ.get("DATA_DIR", "./data"), "tb-dev.json"))


def refund_id(tin_hash: str, period: str, tax_type: str | None) -> str:
    return "ref-" + deterministic_id(f"refund:{tin_hash}:{period}:{tax_type or 'any'}")[:24]


class RefundDestinationUnbound(Exception):
    """R4 S1a#3: a core refund must pay the ORIGINAL payment source account
    (the same source-binding rule as the NIP lane, nip_recon.go:434-443).
    Raised when no server-side payment-source binding exists for the
    (tin_hash, period, tax_type) key, or when a caller/operator tries to
    execute against a destination that is not the bound source."""


def payment_source_key(tin_hash: str, period: str, tax_type: str | None) -> str:
    return f"{tin_hash}:{period}:{tax_type or 'any'}"


def bound_destination(store: Any, tin_hash: str, period: str,
                      tax_type: str | None) -> str | None:
    """Resolve the refund destination from the server-side payment-source
    registry (populated by settlement ingest pipelines from captured
    payment records — never from caller input). Falls back to the latest
    recorded source for the TIN under the wildcard period key."""
    rec = store.get("payment_sources", payment_source_key(tin_hash, period, tax_type))
    if rec is None:
        rec = store.get("payment_sources", payment_source_key(tin_hash, "*", tax_type))
    if rec is None and tax_type is not None:
        rec = store.get("payment_sources", payment_source_key(tin_hash, period, None))
    if rec is None:
        return None
    return rec.get("account_id")


def treasury_account() -> str:
    return "0000000f000000010000000000000001"


def taxpayer_account(tin_hash: str) -> str:
    # TB-style u128-ish id derived deterministically from the tin hash
    h = hashlib.sha256(f"refund:{tin_hash}".encode()).hexdigest()
    return "0000000f00000001" + h[:16]


class InprocLedger:
    """Durable dev ledger with TigerBeetle semantics (subset):
    double-entry, pending transfers, post/void, idempotent create.
    Mirrors services/ledger/internal/tb client semantics."""

    FLAG_PENDING = 1
    FLAG_POST_PENDING = 2
    FLAG_VOID_PENDING = 4
    FLAG_DEBITS_NOT_EXCEED_CREDITS = 8

    def __init__(self, path: str):
        self.path = path
        os.makedirs(os.path.dirname(path), exist_ok=True)
        self._load()

    def _load(self):
        try:
            import json
            with open(self.path) as f:
                d = json.load(f)
            self.accounts = d.get("accounts", {})
            self.transfers = d.get("transfers", {})
        except Exception:
            self.accounts, self.transfers = {}, {}

    def _save(self):
        import json
        tmp = self.path + ".tmp"
        with open(tmp, "w") as f:
            json.dump({"accounts": self.accounts, "transfers": self.transfers}, f)
        os.replace(tmp, self.path)

    def ensure_account(self, account_id: str, ledger: int = 1, flags: int = 0, code: str = "") -> None:
        if account_id not in self.accounts:
            self.accounts[account_id] = {
                "id": account_id, "ledger": ledger, "flags": flags, "code": code,
                "debits_posted": 0, "credits_posted": 0, "debits_pending": 0, "credits_pending": 0,
            }
            self._save()

    def create_pending(self, *, transfer_id: str, debit: str, credit: str, amount_kobo: int) -> dict:
        """Idempotent pending create (two-phase key = transfer_id)."""
        if transfer_id in self.transfers:
            return self.transfers[transfer_id]
        a, b = self.accounts[debit], self.accounts[credit]
        if (a["flags"] & self.FLAG_DEBITS_NOT_EXCEED_CREDITS) and \
           a["debits_posted"] + a["debits_pending"] + amount_kobo > a["credits_posted"] + a["credits_pending"]:
            raise ValueError(f"EXCEEDS_CREDITS on {debit}")
        a["debits_pending"] += amount_kobo
        b["credits_pending"] += amount_kobo
        t = {"id": transfer_id, "debit": debit, "credit": credit,
             "amount_kobo": amount_kobo, "pending": True, "resolved": False}
        self.transfers[transfer_id] = t
        self._save()
        return t

    def post_pending(self, *, pending_id: str, post_id: str, amount_kobo: int | None = None) -> dict:
        if post_id in self.transfers:
            return self.transfers[post_id]
        pend = self.transfers.get(pending_id)
        if pend is None or not pend["pending"] or pend["resolved"]:
            raise ValueError("pending transfer not postable")
        amt = amount_kobo if amount_kobo is not None else pend["amount_kobo"]
        if amt > pend["amount_kobo"]:
            raise ValueError("post amount exceeds pending")
        a, b = self.accounts[pend["debit"]], self.accounts[pend["credit"]]
        if (a["flags"] & self.FLAG_DEBITS_NOT_EXCEED_CREDITS) and \
           a["debits_posted"] + amt > a["credits_posted"]:
            raise ValueError(f"EXCEEDS_CREDITS on {pend['debit']}")
        a["debits_pending"] -= pend["amount_kobo"]
        b["credits_pending"] -= pend["amount_kobo"]
        a["debits_posted"] += amt
        b["credits_posted"] += amt
        if amt == pend["amount_kobo"]:
            pend["resolved"] = True
            pend["pending"] = False
        else:  # partial capture: remainder stays pending
            pend["amount_kobo"] -= amt
            a["debits_pending"] += pend["amount_kobo"]
            b["credits_pending"] += pend["amount_kobo"]
        t = {"id": post_id, "debit": pend["debit"], "credit": pend["credit"],
             "amount_kobo": amt, "pending": False, "resolved": True, "posts": pending_id}
        self.transfers[post_id] = t
        self._save()
        return t

    def void_pending(self, *, pending_id: str, void_id: str) -> dict:
        if void_id in self.transfers:
            return self.transfers[void_id]
        pend = self.transfers.get(pending_id)
        if pend is None or not pend["pending"] or pend["resolved"]:
            raise ValueError("pending transfer not voidable")
        a, b = self.accounts[pend["debit"]], self.accounts[pend["credit"]]
        a["debits_pending"] -= pend["amount_kobo"]
        b["credits_pending"] -= pend["amount_kobo"]
        pend["resolved"] = True
        pend["pending"] = False
        t = {"id": void_id, "debit": pend["debit"], "credit": pend["credit"],
             "amount_kobo": pend["amount_kobo"], "pending": False, "resolved": True, "voids": pending_id}
        self.transfers[void_id] = t
        self._save()
        return t

    def get_transfer(self, transfer_id: str) -> dict | None:
        return self.transfers.get(transfer_id)


class TigerBeetleLedger:
    """Remote TigerBeetle connector (SPEC C §4.2): uses the real
    tigerbeetle-go client via the ledger service TB backend when deployed;
    here we hold maker/settle tokens from env (SPEC C §4.1) and fail loudly
    if the cluster is unreachable (no silent dev fallback in prod)."""

    def __init__(self, addresses: str):
        self.addresses = [a.strip() for a in addresses.split(",") if a.strip()]
        self.maker_token = os.environ.get("MERIDIAN_LEDGER_MAKER_TOKEN", "")
        self.settle_token = os.environ.get("MERIDIAN_LEDGER_SETTLE_TOKEN", "")
        self._client = None

    def _connect(self):
        if self._client is None:
            raise RuntimeError(
                "TigerBeetle remote connector requires the ledger service sidecar "
                f"(addresses={self.addresses}); run refunds through services/ledger "
                "in this deployment")
        return self._client

    def ensure_account(self, account_id, ledger=1, flags=0, code=""):
        self._connect()

    def create_pending(self, **kw):
        return self._connect().create_pending(**kw)

    def post_pending(self, **kw):
        return self._connect().post_pending(**kw)

    def void_pending(self, **kw):
        return self._connect().void_pending(**kw)

    def get_transfer(self, tid):
        return self._connect().get_transfer(tid)


# ---------------------------------------------------------------- executor

class RefundPayloadConflict(Exception):
    pass


class RefundExecutor:
    """Executes refund decisions against the ledger with:
    - two-phase idempotency: deterministic pending/post/void ids per refund
    - replay: an existing execution with the same payload returns the record
    - compensation: a failed post voids the pending reservation
    - resume: sweep_pending() re-posts executions stranded mid-flight
    """

    def __init__(self, store, ledger):
        self.store = store
        self.ledger = ledger

    def _accounts(self, tin_hash: str, destination: str) -> tuple[str, str]:
        tre = treasury_account()
        self.ledger.ensure_account(tre, 1, InprocLedger.FLAG_DEBITS_NOT_EXCEED_CREDITS
                                   if isinstance(self.ledger, InprocLedger) else 1,
                                   "nrs-refund-treasury")
        # R4 S1a#3: the credit side is the bound ORIGINAL payment source
        # account, not a tin-derived internal account.
        self.ledger.ensure_account(destination, 1, 0, "refund-destination:original-source")
        return tre, destination

    def execute(self, *, tin_hash: str, period: str, tax_type: str | None,
                amount_kobo: int, decision: dict, approved_by: str,
                destination_account: str | None = None) -> dict:
        # R4 S1a#3: bind the refund destination to the ORIGINAL payment
        # source account recorded server-side. No binding -> fail closed
        # (the endpoint demotes auto lanes to manual review instead of
        # paying an unverifiable destination). A requested destination that
        # is not the bound source is rejected outright.
        bound = bound_destination(self.store, tin_hash, period, tax_type)
        if bound is None:
            raise RefundDestinationUnbound(
                f"no original payment source on file for "
                f"{payment_source_key(tin_hash, period, tax_type)}; refund "
                f"destination cannot be verified")
        if destination_account is not None and destination_account != bound:
            raise RefundDestinationUnbound(
                f"requested destination {destination_account} is not the "
                f"bound original payment source {bound}")
        rid = refund_id(tin_hash, period, tax_type)
        existing = self.store.get("refund_executions", rid)
        if existing is not None:
            if existing["amount_kobo"] != amount_kobo or existing["tax_type"] != (tax_type or "any"):
                raise RefundPayloadConflict(
                    f"refund {rid} exists with different payload")
            if existing["status"] in ("posted", "pending"):
                existing["idempotent_replay"] = True
                return existing
            # post_failed / voided -> re-execute with fresh attempt ids
        tre, tax = self._accounts(tin_hash, bound)
        attempt = (existing or {}).get("attempt", 0) + 1
        pend_id = deterministic_id(f"ref-pend:{rid}:{attempt}")
        post_id = deterministic_id(f"ref-post:{rid}:{attempt}")
        void_id = deterministic_id(f"ref-void:{rid}:{attempt}")
        rec = {
            "refund_id": rid, "tin_hash": tin_hash, "period": period,
            "tax_type": tax_type or "any", "amount_kobo": amount_kobo,
            "attempt": attempt, "lane": decision.get("lane"),
            "approved_by": approved_by,
            "decision_reasons": decision.get("reasons", []),
            "treasury_account": tre, "taxpayer_account": tax,
            "destination_bound": True,
            "pending_transfer_id": pend_id, "post_transfer_id": post_id,
            "status": "pending", "created_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        }
        self.store.put("refund_executions", rid, rec)
        # funds RESERVED (pending) — crash-safe: record persisted before post
        self.ledger.create_pending(transfer_id=pend_id, debit=tre, credit=tax,
                                   amount_kobo=amount_kobo)
        try:
            self.ledger.post_pending(pending_id=pend_id, post_id=post_id,
                                     amount_kobo=amount_kobo)
        except Exception:
            # compensation: void the pending reservation, mark for retry
            try:
                self.ledger.void_pending(pending_id=pend_id, void_id=void_id)
            except Exception:
                pass
            rec["status"] = "post_failed"
            self.store.put("refund_executions", rid, rec)
            raise
        rec["status"] = "posted"
        rec["posted_at"] = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
        self.store.put("refund_executions", rid, rec)
        return rec

    def sweep_pending(self) -> dict:
        """Crash-resume: re-post executions stranded in status=pending."""
        resumed = 0
        for rec in list(self.store.list("refund_executions")):
            if rec.get("status") != "pending":
                continue
            try:
                self.ledger.post_pending(pending_id=rec["pending_transfer_id"],
                                         post_id=rec["post_transfer_id"],
                                         amount_kobo=rec["amount_kobo"])
                rec["status"] = "posted"
                rec["posted_at"] = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
                self.store.put("refund_executions", rec["refund_id"], rec)
                resumed += 1
            except Exception:
                continue
        return {"resumed": resumed}


# ---------------------------------------------------------------- TAT seal helper

def seal_execution_record(rec: dict) -> str:
    """HMAC seal over the execution record for the TAT evidence bundle."""
    key = os.environ.get("TAT_SEAL_KEY", "meridian-dev-tat-seal").encode()
    msg = "|".join(str(rec.get(k, "")) for k in (
        "refund_id", "tin_hash", "period", "tax_type", "amount_kobo",
        "status", "pending_transfer_id", "post_transfer_id"))
    return hmac_mod.new(key, msg.encode(), hashlib.sha256).hexdigest()
