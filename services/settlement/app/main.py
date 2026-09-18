"""Settlement service (PSSP 3-way reconciliation, port 8013).

- Reconciles platform ledger vs PSSP settlement report vs Treasury credit
  advice; classifies breaks (missing/amount-mismatch/date-mismatch).
- Emits nrs.settlement.recon.run.v1 with outbox-first durability.
- I3 refund fast-track lane (innovation): server-side credit/compliance
  inputs ONLY (HARDENING B3 #1), deterministic refund id, decision event.
- F2 refund execution: auto_approve executes a REAL pending->post transfer
  on the ledger (refund_execution.RefundExecutor), idempotent per refund.
"""
from __future__ import annotations

import json
import os
import time
from contextlib import asynccontextmanager
from typing import Any

from fastapi import Depends, FastAPI, HTTPException, Query
from pydantic import BaseModel, Field

from meridian_events.auth import Claims, fastapi_dependency
from meridian_events.envelope import new_envelope
from meridian_events.outbox import Outbox
from meridian_events.store import Store

SERVICE = "settlement"
VERSION = "0.1.0"
PORT = os.environ.get("PORT", "8013")
DATA_DIR = os.environ.get("DATA_DIR", "./data")

_store: Store | None = None
_outbox: Outbox | None = None
_executor = None

# I3 refund fast-track caps (policy; amounts are integer kobo)
REFUND_AUTO_CAP_KOBO = 500_000_000         # ₦5m
REFUND_MANUAL_REVIEW_CAP_KOBO = 2_000_000_000  # ₦20m

# Refund-decision idempotency window (TTL): decisions are durable and the
# same (tin, period, tax_type) replays from the record; after the TTL the
# key may be re-decided (treated as new). Purge removes terminal records
# past expiry only.
REFUND_IDEMPOTENCY_TTL_SECONDS = int(os.environ.get("REFUND_IDEMPOTENCY_TTL_SECONDS", "86400"))

PageLimit = Query(default=50, ge=1, le=200)
PageOffset = Query(default=0, ge=0)


def _page(items: list[dict], limit: int, offset: int) -> tuple[list[dict], int]:
    return items[offset:offset + limit], len(items)


@asynccontextmanager
async def lifespan(app: FastAPI):
    global _store, _outbox, _executor
    _store = Store(DATA_DIR)
    _outbox = Outbox(_store)
    from .refund_execution import RefundExecutor, ledger_from_env
    _executor = RefundExecutor(_store, ledger_from_env())
    resumed = _executor.sweep_pending()
    if resumed["resumed"]:
        print(f"{SERVICE}: crash-resume reposted {resumed['resumed']} stranded refund(s)")
    yield


app = FastAPI(title="meridian-settlement", version=VERSION, lifespan=lifespan)


@app.get("/healthz")
def healthz() -> dict:
    return {"status": "ok", "service": SERVICE, "version": VERSION}


@app.get("/readyz")
def readyz() -> dict:
    return {"ready": True}


# ---------------------------------------------------------------- models

class ReconRecord(BaseModel):
    model_config = {"extra": "forbid"}
    reference: str
    amount_kobo: int = Field(ge=0)
    date: str | None = None
    meta: dict[str, Any] | None = None


class ReconRunRequest(BaseModel):
    model_config = {"extra": "forbid"}
    run_id: str | None = None
    platform: list[ReconRecord] = []
    pssp: list[ReconRecord] = []
    treasury: list[ReconRecord] = []


class FastTrackRequest(BaseModel):
    model_config = {"extra": "forbid"}
    tin_hash: str
    amount_kobo: int = Field(gt=0)
    tax_type: str | None = None
    period: str | None = None


# ---------------------------------------------------------------- recon core

def _classify_break(reference: str, kind: str, detail: str,
                    platform: ReconRecord | None, pssp: ReconRecord | None,
                    treasury: ReconRecord | None) -> dict:
    from meridian_events.idgen import deterministic_id
    return {
        "id": "brk-" + deterministic_id(f"{reference}:{kind}")[:16],
        "reference": reference, "kind": kind, "detail": detail,
        "status": "open",
        "platform_amount_kobo": platform.amount_kobo if platform else None,
        "pssp_amount_kobo": pssp.amount_kobo if pssp else None,
        "treasury_amount_kobo": treasury.amount_kobo if treasury else None,
        "opened_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
    }


def _run_recon(run_id: str, platform: list[ReconRecord], pssp: list[ReconRecord],
               treasury: list[ReconRecord]) -> dict:
    pf = {r.reference: r for r in platform}
    ps = {r.reference: r for r in pssp}
    tr = {r.reference: r for r in treasury}
    refs = sorted(set(pf) | set(ps) | set(tr))
    breaks: list[dict] = []
    matched = 0
    for ref in refs:
        p, s, t = pf.get(ref), ps.get(ref), tr.get(ref)
        if p is None:
            breaks.append(_classify_break(ref, "missing_on_platform",
                                          "settled by PSSP but not on platform ledger", p, s, t))
            continue
        if s is None:
            breaks.append(_classify_break(ref, "missing_on_pssp",
                                          "on platform ledger but not in PSSP report", p, s, t))
            continue
        if t is None:
            breaks.append(_classify_break(ref, "missing_on_treasury",
                                          "settled by PSSP but no treasury credit advice", p, s, t))
            continue
        if not (p.amount_kobo == s.amount_kobo == t.amount_kobo):
            breaks.append(_classify_break(ref, "amount_mismatch",
                                          "amounts differ across the three sides", p, s, t))
            continue
        if p.date and s.date and p.date != s.date:
            breaks.append(_classify_break(ref, "date_mismatch",
                                          "value dates differ platform vs PSSP", p, s, t))
            continue
        matched += 1
    run = {
        "run_id": run_id, "records": len(refs), "matched": matched,
        "break_count": len(breaks),
        "ran_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
    }
    return {"run": run, "breaks": breaks}


@app.post("/v1/recon/pssp/run")
def recon_run(req: ReconRunRequest,
              claims: Claims = Depends(fastapi_dependency({"operator", "admin"}))) -> dict:
    from meridian_events.idgen import new_id
    run_id = req.run_id or "run-" + new_id()[:16]
    result = _run_recon(run_id, req.platform, req.pssp, req.treasury)
    _store.put("recon_runs", run_id, result["run"])
    for b in result["breaks"]:
        _store.put("breaks", b["id"], b)
    env = new_envelope("nrs.settlement.recon.run.v1", SERVICE, {
        "run": result["run"], "break_ids": [b["id"] for b in result["breaks"]],
    })
    _outbox.append("nrs.settlement.recon.run.v1", env)
    return result


@app.get("/v1/recon/runs")
def recon_runs(limit: int = PageLimit, offset: int = PageOffset,
               claims: Claims = Depends(fastapi_dependency())) -> dict:
    runs = sorted(_store.list("recon_runs"), key=lambda r: r["ran_at"], reverse=True)
    page, total = _page(runs, limit, offset)
    return {"runs": page, "count": len(page), "total": total, "limit": limit, "offset": offset}


@app.get("/v1/recon/breaks")
def recon_breaks(status: str | None = None, limit: int = PageLimit, offset: int = PageOffset,
                 claims: Claims = Depends(fastapi_dependency())) -> dict:
    brks = _store.list("breaks")
    if status:
        brks = [b for b in brks if b["status"] == status]
    brks.sort(key=lambda b: b["opened_at"], reverse=True)
    page, total = _page(brks, limit, offset)
    return {"breaks": page, "count": len(page), "total": total, "limit": limit, "offset": offset}


class BreakResolveRequest(BaseModel):
    model_config = {"extra": "forbid"}
    resolution: str
    note: str | None = None


@app.post("/v1/recon/breaks/{break_id}/resolve")
def break_resolve(break_id: str, req: BreakResolveRequest,
                  claims: Claims = Depends(fastapi_dependency({"operator", "admin"}))) -> dict:
    b = _store.get("breaks", break_id)
    if b is None:
        raise HTTPException(404, f"break {break_id}")
    b["status"] = "resolved"
    b["resolution"] = req.resolution
    b["note"] = req.note
    b["resolved_by"] = claims.sub
    b["resolved_at"] = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
    _store.put("breaks", break_id, b)
    return {"break": b}


# ---------------------------------------------------------------- I3 refund fast-track

def _refund_decision_terminal(doc: dict) -> bool:
    exe = _store.get("refund_executions", doc.get("refund_id", "")) or doc.get("execution")
    if exe and exe.get("status") == "posted":
        return True
    return doc.get("status") == "rejected"


def _iso(ts: float) -> str:
    return time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(ts))


def refund_decision_expired(doc: dict, now: float | None = None) -> bool:
    now = time.time() if now is None else now
    exp = doc.get("expires_at")
    if exp:
        try:
            return time.mktime(time.strptime(exp, "%Y-%m-%dT%H:%M:%SZ")) <= now
        except ValueError:
            pass
    decided = doc.get("decided_at")
    if decided:
        try:
            return time.mktime(time.strptime(decided, "%Y-%m-%dT%H:%M:%SZ")) + REFUND_IDEMPOTENCY_TTL_SECONDS <= now
        except ValueError:
            pass
    return False


def purge_expired_refund_decisions() -> int:
    """Remove terminal decisions past the idempotency TTL. Returns count."""
    n = 0
    for rid, doc in list(_store.items("refund_decisions")):
        if refund_decision_expired(doc) and _refund_decision_terminal(doc):
            _store.delete("refund_decisions", rid)
            n += 1
    return n


def _check_tenant_tin_binding(claims: Claims, tin_hash: str) -> None:
    """R4 S1a#1: the caller-supplied tin_hash must resolve to the caller's
    tenant context. Ownership bindings live in the server-side tenant_tins
    registry (populated by onboarding/registration pipelines, or via
    POST /v1/tenants/bind-tin by an admin). If a binding exists and does
    not match the caller's tenant, the refund is refused (403). Absent a
    binding there is no ownership claim to violate, and the downstream
    credit-profile check still fails closed when no profile exists."""
    binding = _store.get("tenant_tins", tin_hash)
    if binding is None:
        return
    if not claims.tenant_id or claims.tenant_id != binding.get("tenant_id"):
        raise HTTPException(403, "tin_hash is not registered to the caller's tenant")


@app.post("/v1/refunds/fasttrack")
def refund_fasttrack(req: FastTrackRequest,
                     claims: Claims = Depends(fastapi_dependency({"operator", "admin"}))) -> dict:
    """I3 refund fast-track lane + F2 real execution for the auto lane.

    HARDENING B3 #1: credit score and compliance history are read from the
    platform's OWN stores only — caller-supplied trust fields were removed
    from the request schema (extra=forbid rejects them).
    """
    from .refund import decide_refund_lane

    _check_tenant_tin_binding(claims, req.tin_hash)
    period = req.period or time.strftime("%Y-%m", time.gmtime())
    from .refund_execution import refund_id as _rid
    rid = _rid(req.tin_hash, period, req.tax_type)
    existing = _store.get("refund_decisions", rid)
    if existing is not None and not refund_decision_expired(existing):
        # Idempotent replay: same deterministic refund id returns the stored
        # decision. If the refund never posted (decision stored but the
        # execution crashed / 502'd / was never attempted), re-execute the
        # SAME refund now — never a second transfer for a posted one.
        exe = _store.get("refund_executions", rid) or existing.get("execution")
        if existing["lane"] == "auto_approve" and not (exe and exe.get("status") == "posted"):
            try:
                exe = _executor.execute(tin_hash=req.tin_hash, period=period,
                                        tax_type=req.tax_type, amount_kobo=req.amount_kobo,
                                        decision=existing, approved_by="fasttrack:replay")
            except RefundPayloadConflict as exc:
                raise HTTPException(409, str(exc)) from exc
            except Exception as exc:  # noqa: BLE001
                raise HTTPException(502, f"refund execution failed: {exc}") from exc
            existing["execution"] = exe
        existing["idempotent_replay"] = True
        return existing

    profile = _store.get("taxpayer_credit_profiles", req.tin_hash)
    credit_score = profile.get("credit_score") if profile else None
    filings_on_time = profile.get("filings_on_time") if profile else None
    filings_total = profile.get("filings_total") if profile else None
    open_breaks = [b for b in _store.list("breaks")
                   if b.get("status") in ("open", "investigating")]
    doc = decide_refund_lane(
        tin_hash=req.tin_hash, amount_kobo=req.amount_kobo,
        credit_score=credit_score, filings_on_time=filings_on_time,
        filings_total=filings_total, prior_breaks=len(open_breaks),
        auto_cap_kobo=REFUND_AUTO_CAP_KOBO, review_cap_kobo=REFUND_MANUAL_REVIEW_CAP_KOBO,
        tax_type=req.tax_type)
    doc["refund_id"] = rid
    doc["tin_hash"] = req.tin_hash
    doc["period"] = period
    doc["tax_type"] = req.tax_type
    doc["decided_at"] = _iso(time.time())
    doc["expires_at"] = _iso(time.time() + REFUND_IDEMPOTENCY_TTL_SECONDS)
    doc["initiated_by"] = claims.sub  # maker identity for maker!=checker
    doc["status"] = "pending"
    if doc["lane"] == "auto_approve" and bound_destination(
            _store, req.tin_hash, period, req.tax_type) is None:
        # R4 S1a#3: never auto-pay an unverifiable destination — demote to
        # manual review where an operator can establish the source binding.
        doc["lane"] = "manual_review"
        doc["reasons"].append("no original payment source on file for this "
                              "(tin, period, tax_type); demoted to manual review")
    _store.put("refund_decisions", rid, doc)
    if doc["lane"] == "auto_approve":
        try:
            exe = _executor.execute(tin_hash=req.tin_hash, period=period,
                                    tax_type=req.tax_type, amount_kobo=req.amount_kobo,
                                    decision=doc, approved_by="fasttrack:auto")
        except RefundPayloadConflict as exc:
            raise HTTPException(409, str(exc)) from exc
        except RefundDestinationUnbound as exc:
            raise HTTPException(409, str(exc)) from exc
        except Exception as exc:  # noqa: BLE001
            raise HTTPException(502, f"refund execution failed: {exc}") from exc
        doc["execution"] = exe
        doc["status"] = "executed"
    elif doc["lane"] in ("manual_review", "standard"):
        event = {"type": "nrs.refund.manual_review.v1", "dedup_key": f"manual_review:{rid}",
                 "decision": doc, "queued_at": doc["decided_at"], "lane": doc["lane"]}
        _store.put("refund_manual_review", rid, event)
        from meridian_events.envelope import new_envelope
        _outbox.append("nrs.refund.manual_review.v1",
                       new_envelope("nrs.refund.manual_review.v1", SERVICE, event))
    return doc


# ---------------------------------------------------------------------------
# R4 S1a#4: standard-lane approval queue (was: no endpoint; approve 409'd
# any lane != manual_review, so standard refunds could never execute).
# ---------------------------------------------------------------------------

@app.get("/v1/refunds/queue")
def refund_queue(lane: str | None = None,
                 limit: int = PageLimit,
                 offset: int = PageOffset,
                 claims: Claims = Depends(fastapi_dependency({"operator", "admin"}))) -> dict:
    """List refund decisions awaiting a human decision (manual_review and
    standard lanes; rejected/executed decisions drop out of the queue)."""
    docs = [d for d in _store.list("refund_decisions")
            if d.get("lane") in ("manual_review", "standard")
            and d.get("status", "pending") == "pending"
            and not (d.get("execution") or {}).get("status") == "posted"]
    if lane:
        docs = [d for d in docs if d.get("lane") == lane]
    page, total = _page(docs, limit, offset)
    return {"refunds": page, "count": len(page), "total": total,
            "limit": limit, "offset": offset}


def _enforce_maker_checker(doc: dict, claims: Claims) -> None:
    """Maker != checker: the principal who initiated the refund decision
    cannot also approve it. The ledger saga separately splits maker/settle
    service tokens (MERIDIAN_LEDGER_MAKER_TOKEN / *_SETTLE_TOKEN)."""
    maker = doc.get("initiated_by")
    if maker and maker == claims.sub:
        raise HTTPException(403, "maker!=checker: the initiating principal cannot approve this refund")


@app.post("/v1/refunds/{rid}/approve")
def refund_manual_approve(rid: str,
                          claims: Claims = Depends(fastapi_dependency({"operator", "admin"}))) -> dict:
    """Approve endpoint for manual_review AND standard lanes: executes the
    SAME refund workflow after human approval (idempotent per refund)."""
    doc = _store.get("refund_decisions", rid)
    if doc is None:
        raise HTTPException(404, f"refund decision {rid}")
    if doc.get("status") == "rejected":
        raise HTTPException(409, f"refund {rid} was rejected and cannot be approved")
    if doc.get("lane") not in ("manual_review", "standard"):
        raise HTTPException(409, f"refund {rid} is in lane {doc.get('lane')}; approve not applicable")
    _check_tenant_tin_binding(claims, doc["tin_hash"])
    _enforce_maker_checker(doc, claims)
    try:
        exe = _executor.execute(tin_hash=doc["tin_hash"], period=doc["period"],
                                tax_type=doc.get("tax_type"),
                                amount_kobo=doc["amount_kobo"], decision=doc,
                                approved_by=claims.sub)
    except RefundDestinationUnbound as exc:
        raise HTTPException(409, str(exc)) from exc
    except RefundPayloadConflict as exc:
        raise HTTPException(409, str(exc)) from exc
    except Exception as exc:  # noqa: BLE001
        raise HTTPException(502, f"refund execution failed: {exc}") from exc
    doc["execution"] = exe
    doc["approved_by"] = claims.sub
    doc["status"] = "executed"
    _store.put("refund_decisions", rid, doc)
    return {"decision": doc, "execution": exe}


class RefundRejectRequest(BaseModel):
    reason: str | None = None


@app.post("/v1/refunds/{rid}/reject")
def refund_reject(rid: str, req: RefundRejectRequest,
                  claims: Claims = Depends(fastapi_dependency({"operator", "admin"}))) -> dict:
    """Reject a queued refund decision (manual_review or standard lane).
    Rejected decisions never execute and become purge-terminal."""
    doc = _store.get("refund_decisions", rid)
    if doc is None:
        raise HTTPException(404, f"refund decision {rid}")
    if (doc.get("execution") or {}).get("status") == "posted":
        raise HTTPException(409, f"refund {rid} already executed; cannot reject")
    _check_tenant_tin_binding(claims, doc["tin_hash"])
    _enforce_maker_checker(doc, claims)
    doc["status"] = "rejected"
    doc["rejected_by"] = claims.sub
    doc["reject_reason"] = req.reason
    doc["rejected_at"] = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
    _store.put("refund_decisions", rid, doc)
    return {"decision": doc}


class TenantTinBindingRequest(BaseModel):
    model_config = {"extra": "forbid"}
    tin_hash: str
    tenant_id: str


@app.post("/v1/tenants/bind-tin")
def bind_tenant_tin(req: TenantTinBindingRequest,
                    claims: Claims = Depends(fastapi_dependency({"admin"}))) -> dict:
    """Register the server-side tenant<->TIN ownership binding used to
    authorise refund initiation. Admin-only; rebinding is allowed only by
    an admin of the currently-bound tenant."""
    existing = _store.get("tenant_tins", req.tin_hash)
    if existing is not None and existing.get("tenant_id") != req.tenant_id:
        if not claims.tenant_id or claims.tenant_id != existing.get("tenant_id"):
            raise HTTPException(403, "tin_hash is bound to another tenant")
    doc = {"tin_hash": req.tin_hash, "tenant_id": req.tenant_id,
           "bound_by": claims.sub,
           "bound_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())}
    _store.put("tenant_tins", req.tin_hash, doc)
    return {"binding": doc}


# ---------------------------------------------------------------- F6/F9: pull-mode recon + auto-heal + revenue events

class IngestRequest(BaseModel):
    model_config = {"extra": "forbid"}
    side: str  # platform|pssp|treasury
    records: list[ReconRecord]


@app.post("/v1/recon/ingest")
def ingest_records(req: IngestRequest,
                   claims: Claims = Depends(fastapi_dependency({"operator", "admin"}))) -> dict:
    """F9: pull-mode ingest — adapters push side-tagged records into the
    store; pull-run reconciles whatever has arrived (no request-body runs)."""
    if req.side not in ("platform", "pssp", "treasury"):
        raise HTTPException(422, "side must be platform|pssp|treasury")
    coll = f"recon_inbox_{req.side}"
    for r in req.records:
        _store.put(coll, r.reference, r.model_dump())
        # R4 S1a#3: captured payment records carry the ORIGINAL source
        # account in meta (tin_hash + source_account); record the
        # server-side binding that refund execution pays back to.
        meta = r.meta or {}
        tin_hash, src = meta.get("tin_hash"), meta.get("source_account")
        if tin_hash and src:
            key = payment_source_key(tin_hash, meta.get("period", "*"),
                                     meta.get("tax_type"))
            _store.put("payment_sources", key, {
                "tin_hash": tin_hash, "period": meta.get("period", "*"),
                "tax_type": meta.get("tax_type"), "account_id": src,
                "source": f"recon-ingest:{req.side}", "reference": r.reference})
    return {"side": req.side, "ingested": len(req.records)}


@app.post("/v1/recon/pssp/pull-run")
def recon_pull_run(req: ReconRunRequest,
                   claims: Claims = Depends(fastapi_dependency({"operator", "admin"}))) -> dict:
    """F9: reconcile the pulled inbox; auto-heal ledger_captured_pssp_missing
    breaks into investigation cases; emit deduped revenue events for matches."""
    from meridian_events.idgen import new_id
    run_id = req.run_id or "run-" + new_id()[:16]
    platform = [ReconRecord(**d) for d in _store.list("recon_inbox_platform")]
    pssp = [ReconRecord(**d) for d in _store.list("recon_inbox_pssp")]
    treasury = [ReconRecord(**d) for d in _store.list("recon_inbox_treasury")]
    result = _run_recon(run_id, platform, pssp, treasury)
    result["run"]["mode"] = "pull"
    result["run"]["adapter"] = "sim"  # sim adapter pulls; real PSSP adapter behind interface
    _store.put("recon_runs", run_id, result["run"])

    auto_healed: list[dict] = []
    for b in result["breaks"]:
        _store.put("breaks", b["id"], b)
        if b["kind"] == "missing_on_pssp":
            # auto-heal: open an investigation case, mark break investigating
            case_id = f"case:{b['reference']}"
            if _store.get("investigation_cases", case_id) is None:
                case = {"case_id": case_id, "reference": b["reference"],
                        "class": "ledger_captured_pssp_missing",
                        "status": "open", "opened_at": b["opened_at"],
                        "evidence": {"break_id": b["id"]}}
                _store.put("investigation_cases", case_id, case)
            b["status"] = "investigating"
            _store.put("breaks", b["id"], b)
            auto_healed.append({"case_id": case_id, "reference": b["reference"]})

    # revenue events for 3-way matches, deduped by reference
    today = time.strftime("%Y-%m-%d", time.gmtime())
    for ref in sorted({r.reference for r in platform} & {r.reference for r in pssp}
                      & {r.reference for r in treasury}):
        if _store.get("revenue_events", ref) is None:
            amt = next(r.amount_kobo for r in platform if r.reference == ref)
            _store.put("revenue_events", ref, {
                "reference": ref, "amount_kobo": amt, "date": today,
                "source": "pssp_3way_match", "adapter": "sim"})
    result["auto_healed"] = auto_healed
    env = new_envelope("nrs.settlement.recon.run.v1", SERVICE, {
        "run": result["run"], "break_ids": [b["id"] for b in result["breaks"]],
    })
    _outbox.append("nrs.settlement.recon.run.v1", env)
    return result


@app.get("/v1/revenue/events")
def revenue_events(date: str | None = None, limit: int = PageLimit, offset: int = PageOffset,
                   claims: Claims = Depends(fastapi_dependency())) -> dict:
    evs = _store.list("revenue_events")
    if date:
        evs = [e for e in evs if e.get("date") == date]
    evs.sort(key=lambda e: e["reference"])
    page, total = _page(evs, limit, offset)
    return {"events": page, "count": len(page), "total": total, "limit": limit, "offset": offset}


@app.get("/v1/revenue/aggregate")
def revenue_aggregate(date_from: str | None = None, date_to: str | None = None,
                      claims: Claims = Depends(fastapi_dependency())) -> dict:
    evs = _store.list("revenue_events")
    if date_from:
        evs = [e for e in evs if e.get("date", "") >= date_from]
    if date_to:
        evs = [e for e in evs if e.get("date", "") <= date_to]
    return {"total_kobo": sum(e["amount_kobo"] for e in evs), "count": len(evs)}


# ---------------------------------------------------------------- admin

@app.get("/v1/outbox/pending")
def outbox_pending(claims: Claims = Depends(fastapi_dependency({"admin"}))) -> dict:
    return {"pending": _outbox.pending()}


@app.post("/v1/admin/purge-refund-decisions")
def admin_purge_refunds(claims: Claims = Depends(fastapi_dependency({"admin"}))) -> dict:
    return {"purged": purge_expired_refund_decisions()}
