"""settlement — PSSP 3-way reconciliation (SPEC 2).

POST /v1/recon/pssp/run compares platform vs PSSP vs treasury records by
reference: matched, missing-in-<side>, amount-mismatch breaks are persisted
and served by GET /v1/recon/breaks. Amounts are integer kobo.
"""
from __future__ import annotations

import os
import time
from pathlib import Path

from fastapi import Depends, FastAPI, HTTPException
from pydantic import BaseModel, Field

from meridian_events.auth import Claims, fastapi_dependency
from meridian_events.problem import install_problem_handlers
from meridian_events.store import open_store

SERVICE = "settlement"
VERSION = "0.1.0"

app = FastAPI(title="Meridian settlement", version=VERSION)
install_problem_handlers(app)

DATA_DIR = Path(os.environ.get("DATA_DIR", "./data"))
DATA_DIR.mkdir(parents=True, exist_ok=True)
_store = open_store(DATA_DIR)

# F8: transactional outbox — events are written with state changes and the
# relay publishes at-least-once. Consumer dedup keys are documented per
# event (see docs/funds-flow.md): revenue -> "revenue:{reference}",
# refunds -> "refund:{refund_id}", investigations -> "case:{reference}".
from meridian_events.bus import bus_from_env  # noqa: E402
from meridian_events.outbox import FileOutbox, OutboxRelay  # noqa: E402

_outbox = FileOutbox(DATA_DIR / "outbox")
_bus = bus_from_env()
_relay = OutboxRelay(_outbox, _bus, DATA_DIR / "outbox")


@app.on_event("startup")
def _start_relay() -> None:  # pragma: no cover
    _relay.start()


from .refund_execution import RefundExecutor, ledger_from_env, refund_id  # noqa: E402

_executor = RefundExecutor(_store, ledger_from_env(), _outbox)


class ReconRecord(BaseModel):
    reference: str
    amount_kobo: int
    date: str | None = None
    meta: dict = Field(default_factory=dict)


class ReconRunRequest(BaseModel):
    platform: list[ReconRecord] = Field(default_factory=list)
    pssp: list[ReconRecord] = Field(default_factory=list)
    treasury: list[ReconRecord] = Field(default_factory=list)
    run_id: str | None = None
    idempotency_key: str | None = None  # re-posting the same key replays the stored run


SIDES = ("platform", "pssp", "treasury")


def _index(records: list[ReconRecord]) -> tuple[dict[str, ReconRecord], list[dict]]:
    """Index by reference; duplicate references inside one side become
    explicit duplicate_reference breaks (audit: they were silently
    overwritten). The first occurrence is kept for matching."""
    out: dict[str, ReconRecord] = {}
    dups: list[dict] = []
    seen: dict[str, int] = {}
    for r in records:
        seen[r.reference] = seen.get(r.reference, 0) + 1
        if r.reference in out:
            prev = out[r.reference]
            if prev.amount_kobo != r.amount_kobo:
                dups.append({
                    "reference": r.reference, "kind": "duplicate_reference",
                    "occurrences": seen[r.reference],
                    "amounts_kobo": {"first": prev.amount_kobo, "duplicate": r.amount_kobo},
                    "detail": "duplicate reference within one side with differing amounts",
                })
            else:
                dups.append({
                    "reference": r.reference, "kind": "duplicate_reference",
                    "occurrences": seen[r.reference],
                    "amounts_kobo": {"amount": r.amount_kobo},
                    "detail": "duplicate reference within one side (identical amounts)",
                })
        else:
            out[r.reference] = r
    return out, dups


def _fee_accounted(present: dict[str, ReconRecord]) -> bool:
    """True when platform gross == pssp net + declared fee and treasury ==
    pssp net (F6: the fee-leg accounting path makes this the expected
    shape, so recon balances by construction)."""
    plat, pssp, tre = present["platform"], present["pssp"], present["treasury"]
    fee = int((pssp.meta or {}).get("fee_kobo") or 0)
    return fee > 0 and plat.amount_kobo == pssp.amount_kobo + fee and tre.amount_kobo == pssp.amount_kobo


def reconcile(platform: list[ReconRecord], pssp: list[ReconRecord],
              treasury: list[ReconRecord]) -> dict:
    indexed = {"platform": _index(platform), "pssp": _index(pssp), "treasury": _index(treasury)}
    sides = {s: idx for s, (idx, _d) in indexed.items()}
    dup_breaks = [
        {**b, "side": s}
        for s, (_idx, dups) in indexed.items()
        for b in dups
    ]
    refs = set().union(*(set(s) for s in sides.values()))
    matched: list[str] = []
    breaks: list[dict] = []
    for ref in sorted(refs):
        present = {s: sides[s].get(ref) for s in SIDES}
        missing = [s for s, r in present.items() if r is None]
        if missing:
            amounts = {s: r.amount_kobo for s, r in present.items() if r}
            breaks.append({
                "reference": ref, "kind": "missing",
                "missing_in": missing, "present_in": [s for s in SIDES if s not in missing],
                "amounts_kobo": amounts,
                "detail": f"reference present in {len(amounts)} of 3 sides",
            })
            continue
        amounts = {s: present[s].amount_kobo for s in SIDES}
        if len(set(amounts.values())) == 1:
            matched.append(ref)
        elif _fee_accounted(present):
            # F6: PSSP settles amount - fee; with a fee ledger account the
            # recon balances by construction: platform gross == pssp net +
            # fee and treasury == pssp net.
            matched.append(ref)
        else:
            breaks.append({
                "reference": ref, "kind": "amount_mismatch",
                "amounts_kobo": amounts,
                "max_delta_kobo": max(amounts.values()) - min(amounts.values()),
                "detail": "amounts differ across sides",
            })
    breaks = dup_breaks + breaks
    return {
        "totals": {s: len(sides[s]) for s in SIDES},
        "matched": len(matched),
        "matched_references": matched,
        "break_count": len(breaks),
        "breaks": breaks,
    }


@app.get("/healthz")
def healthz() -> dict:
    return {"status": "ok", "service": SERVICE, "version": VERSION}


@app.get("/readyz")
def readyz() -> dict:
    return {"status": "ok", "service": SERVICE, "version": VERSION}


@app.post("/v1/recon/pssp/run")
def run_recon(req: ReconRunRequest, claims: Claims = Depends(fastapi_dependency())) -> dict:
    if not (req.platform or req.pssp or req.treasury):
        raise HTTPException(400, "at least one side of records is required")
    if req.idempotency_key:
        prior = _store.get("idempotency", f"recon:{req.idempotency_key}")
        if prior is not None:
            return {"run": prior["run"], "breaks": prior["breaks"],
                    "idempotent_replay": True}
    result = reconcile(req.platform, req.pssp, req.treasury)
    run_id = req.run_id or f"run-{int(time.time() * 1000)}"
    run = {"run_id": run_id, "at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
           "by": claims.sub, **{k: v for k, v in result.items() if k != "breaks"}}
    _store.put("runs", run_id, run)
    existing = _store.list("breaks")
    stored_breaks = []
    for b in result["breaks"]:
        b = {**b, "run_id": run_id, "status": "open",
             "id": f"{run_id}:{b['reference']}:{b['kind']}"}
        _store.put("breaks", b["id"], b)
        existing.append(b)
        stored_breaks.append(b)
    if req.idempotency_key:
        _store.put("idempotency", f"recon:{req.idempotency_key}",
                   {"run": run, "breaks": stored_breaks})
    # I5 (REAL): emit nrs.revenue.settled.v1 dashboard documents for matched
    # references (revenue recognised on 3-way match).
    emitted = _emit_revenue_events(run_id, req, set(result["matched_references"]))
    return {"run": run, "breaks": result["breaks"], "revenue_events_emitted": emitted}


# ---------------------------------------------------------------------------
# I5: real-time revenue event stream (nrs.revenue.*)
# ---------------------------------------------------------------------------

def _emit_revenue_events(run_id: str, req: ReconRunRequest, matched: set[str]) -> int:
    """Persist OpenSearch-dashboard-ready revenue documents for matched
    settlement references. Docs are flat, timestamped, integer-kobo.

    F9: revenue is recognised ONCE per reference, globally — a re-submitted
    reference in a later run must not double-count revenue. The
    recognised_references collection is the dedup set (key = reference)."""
    by_ref = {r.reference: r for r in req.platform}
    n = 0
    for ref in sorted(matched):
        rec = by_ref.get(ref)
        if rec is None:
            continue
        if _store.get("recognised_references", ref) is not None:
            continue  # already recognised in a prior run: no double-count
        _store.put("recognised_references", ref,
                   {"reference": ref, "first_run_id": run_id,
                    "recognised_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())})
        doc = {
            "type": "nrs.revenue.settled.v1",
            "@timestamp": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
            "run_id": run_id,
            "reference": ref,
            "amount_kobo": rec.amount_kobo,
            "amount_naira": rec.amount_kobo // 100,
            "date": rec.date,
            "channel": (rec.meta or {}).get("channel"),
            "tax_type": (rec.meta or {}).get("tax_type"),
            "state": (rec.meta or {}).get("state"),
            "status": "settled",
        }
        _store.put("revenue_events", f"{run_id}:{ref}", doc)
        n += 1
    return n


@app.get("/v1/revenue/events")
def revenue_events(limit: int = 200,
                   claims: Claims = Depends(fastapi_dependency())) -> dict:
    docs = _store.list("revenue_events")
    return {"events": docs[-limit:], "count": len(docs)}


@app.get("/v1/revenue/aggregate")
def revenue_aggregate(group_by: str = "tax_type",
                      claims: Claims = Depends(fastapi_dependency())) -> dict:
    """Dashboard-ready aggregation of the revenue event stream (integer
    kobo sums per bucket)."""
    docs = _store.list("revenue_events")
    buckets: dict[str, dict] = {}
    for d in docs:
        key = str(d.get(group_by) or "unknown")
        b = buckets.setdefault(key, {"key": key, "count": 0, "total_kobo": 0})
        b["count"] += 1
        b["total_kobo"] += int(d.get("amount_kobo") or 0)
    return {"group_by": group_by,
            "buckets": sorted(buckets.values(), key=lambda b: -b["total_kobo"]),
            "total_events": len(docs)}


# ---------------------------------------------------------------------------
# I3: refund fast-track lane
# ---------------------------------------------------------------------------

class FastTrackRequest(BaseModel):
    tin_hash: str
    amount_kobo: int
    credit_score: int = Field(ge=0, le=1000)
    filings_on_time: int = Field(default=0, ge=0)
    filings_total: int = Field(default=0, ge=0)
    prior_breaks: int = Field(default=0, ge=0)
    tax_type: str | None = None
    period: str | None = None  # e.g. "2026-07"; defaults to the current month


@app.post("/v1/refunds/fasttrack")
def refund_fasttrack(req: FastTrackRequest,
                     claims: Claims = Depends(fastapi_dependency())) -> dict:
    """I3 + F2: decide the refund lane AND execute approved refunds.
    auto_approve (<= ₦5m) posts via the refund workflow; manual_review is
    queued for the manual-approve endpoint. Idempotent per
    (tin_hash, period, tax_type): a double-submit replays one execution.

    prior_breaks is read SERVER-SIDE from the breaks store — a caller can
    no longer self-certify a clean recon history (audit Flow 2c)."""
    from .refund import decide_refund_lane

    period = req.period or time.strftime("%Y-%m", time.gmtime())
    rid = refund_id(req.tin_hash, period, req.tax_type)
    prior = _store.get("refund_decisions", rid)
    if prior is not None:
        # funds-flow #3: a stored decision is only a safe replay if the
        # refund was actually POSTED. If the first call 502'd before/during
        # execution (or the post failed), re-enter the idempotent executor
        # instead of replaying a decision that never moved money.
        if prior.get("lane") == "auto_approve":
            exe = _store.get("refund_executions", rid) or prior.get("execution")
            if not exe or exe.get("status") != "posted":
                try:
                    exe = _executor.execute(tin_hash=prior["tin_hash"], period=prior["period"],
                                            tax_type=prior.get("tax_type"),
                                            amount_kobo=prior["amount_kobo"],
                                            decision=prior, approved_by="fasttrack:auto")
                except Exception as exc:  # noqa: BLE001
                    raise HTTPException(502, f"refund execution failed: {exc}") from exc
                prior["execution"] = exe
                _store.put("refund_decisions", rid, prior)
        return {**prior, "idempotent_replay": True}
    server_breaks = sum(1 for b in _store.list("breaks") if b.get("status") == "open")
    try:
        doc = decide_refund_lane(
            tin_hash=req.tin_hash, amount_kobo=req.amount_kobo,
            credit_score=req.credit_score, filings_on_time=req.filings_on_time,
            filings_total=req.filings_total, prior_breaks=server_breaks,
            tax_type=req.tax_type)
    except ValueError as exc:
        raise HTTPException(422, str(exc)) from exc
    doc["refund_id"] = rid
    doc["period"] = period
    doc["tin_hash"] = req.tin_hash
    doc["tax_type"] = req.tax_type
    _store.put("refund_decisions", rid, doc)
    if doc["lane"] == "auto_approve":
        try:
            exe = _executor.execute(tin_hash=req.tin_hash, period=period,
                                    tax_type=req.tax_type, amount_kobo=req.amount_kobo,
                                    decision=doc, approved_by="fasttrack:auto")
        except Exception as exc:  # noqa: BLE001
            raise HTTPException(502, f"refund execution failed: {exc}") from exc
        doc["execution"] = exe
    elif doc["lane"] == "manual_review":
        event = {"type": "nrs.refund.manual_review.v1", "dedup_key": f"manual_review:{rid}",
                 "decision": doc, "queued_at": doc["decided_at"]}
        _store.put("refund_manual_review", rid, event)
        from meridian_events.envelope import new_envelope
        _outbox.append("nrs.refund.manual_review.v1",
                       new_envelope("nrs.refund.manual_review.v1", SERVICE, event))
    return doc


@app.post("/v1/refunds/{rid}/approve")
def refund_manual_approve(rid: str,
                          claims: Claims = Depends(fastapi_dependency({"operator", "admin"}))) -> dict:
    """Manual-approve endpoint for refunds above the ₦5m auto cap: executes
    the SAME refund workflow after human approval (idempotent per refund)."""
    doc = _store.get("refund_decisions", rid)
    if doc is None:
        raise HTTPException(404, f"refund decision {rid}")
    if doc.get("lane") != "manual_review":
        raise HTTPException(409, f"refund {rid} is in lane {doc.get('lane')}; manual approve not applicable")
    try:
        exe = _executor.execute(tin_hash=doc["tin_hash"] if "tin_hash" in doc else doc.get("tin_hash", ""),
                                period=doc["period"], tax_type=doc.get("tax_type"),
                                amount_kobo=doc["amount_kobo"], decision=doc,
                                approved_by=claims.sub)
    except Exception as exc:  # noqa: BLE001
        raise HTTPException(502, f"refund execution failed: {exc}") from exc
    doc["execution"] = exe
    doc["approved_by"] = claims.sub
    _store.put("refund_decisions", rid, doc)
    return {"decision": doc, "execution": exe}


@app.post("/v1/refunds/sweep")
def refund_sweep(claims: Claims = Depends(fastapi_dependency({"operator", "admin"}))) -> dict:
    """Recovery worker hook: resume/void refund executions interrupted by a
    crash between the pending transfer and the post."""
    return _executor.sweep_pending()


@app.get("/v1/recon/breaks")
def list_breaks(status: str | None = None,
                claims: Claims = Depends(fastapi_dependency())) -> dict:
    breaks = _store.list("breaks")
    if status:
        breaks = [b for b in breaks if b.get("status") == status]
    return {"breaks": breaks, "count": len(breaks)}


class ResolveRequest(BaseModel):
    resolution: str
    note: str | None = None


@app.post("/v1/recon/breaks/{break_id}/resolve")
def resolve_break(break_id: str, req: ResolveRequest,
                  claims: Claims = Depends(fastapi_dependency({"operator", "admin"}))) -> dict:
    b = _store.get("breaks", break_id)
    if b is None:
        raise HTTPException(404, f"break {break_id}")
    b = {**b, "status": "resolved", "resolution": req.resolution, "note": req.note,
         "resolved_by": claims.sub,
         "resolved_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())}
    _store.put("breaks", break_id, b)
    return {"break": b}


# ---------------------------------------------------------------------------
# F9: independent PULL-mode recon + auto-heal investigation cases
# ---------------------------------------------------------------------------

class PSSPSettlementAdapter:
    """Pulls the PSSP settlement report INDEPENDENTLY of the caller.

    Dev default: the sim adapter reads settlement records published into the
    `pssp_settlement_report` collection (by the PSSP webhook/settlement
    poller) and tags every record honestly (adapter="sim"). Prod selects an
    HTTP adapter via PSSP_REPORT_URL — a caller-fed report is never trusted
    as the PSSP side in pull mode.
    """

    def __init__(self) -> None:
        self.mode = "http" if os.environ.get("PSSP_REPORT_URL") else "sim"

    def settlement_report(self) -> list[ReconRecord]:
        if self.mode == "http":  # pragma: no cover - requires a live PSSP
            import urllib.request
            url = os.environ["PSSP_REPORT_URL"].rstrip("/") + "/v1/settlements"
            req = urllib.request.Request(url, headers={"X-Dev-Role": "operator"})
            import json as _json
            with urllib.request.urlopen(req, timeout=15) as resp:  # noqa: S310
                rows = _json.loads(resp.read() or b"[]")
            return [ReconRecord(**r) for r in rows]
        out = []
        for rec in _store.list("pssp_settlement_report"):
            meta = {**(rec.get("meta") or {}), "adapter": "sim", "honest": True}
            out.append(ReconRecord(reference=rec["reference"],
                                   amount_kobo=int(rec["amount_kobo"]),
                                   date=rec.get("date"), meta=meta))
        return out


_pssp_adapter = PSSPSettlementAdapter()

AUTO_HEAL_TOLERANCE_DAYS = int(os.environ.get("RECON_AUTO_HEAL_TOLERANCE_DAYS", "7"))


class PullRunRequest(BaseModel):
    run_id: str | None = None
    idempotency_key: str | None = None


@app.post("/v1/recon/pssp/pull-run")
def run_recon_pull(req: PullRunRequest, claims: Claims = Depends(fastapi_dependency())) -> dict:
    """F9: independent pull-mode recon. The service fetches the PSSP side
    itself via the adapter (sim adapter is honest-tagged) instead of
    trusting caller-fed records; platform/treasury sides come from the
    durably ingested collections. Auto-heal class: ledger-captured but
    PSSP-missing within the tolerance window -> an investigation case is
    auto-created (deduped per reference)."""
    if req.idempotency_key:
        prior = _store.get("idempotency", f"recon-pull:{req.idempotency_key}")
        if prior is not None:
            return {**prior, "idempotent_replay": True}
    platform = [ReconRecord(**r) for r in _store.list("platform_records")]
    treasury = [ReconRecord(**r) for r in _store.list("treasury_records")]
    pssp = _pssp_adapter.settlement_report()
    result = reconcile(platform, pssp, treasury)
    run_id = req.run_id or f"pull-{int(time.time() * 1000)}"
    run = {"run_id": run_id, "mode": "pull", "adapter": _pssp_adapter.mode,
           "at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
           "by": claims.sub, **{k: v for k, v in result.items() if k != "breaks"}}
    _store.put("runs", run_id, run)
    healed = _auto_heal(run_id, platform, result["breaks"])
    stored = []
    for b in result["breaks"]:
        b = {**b, "run_id": run_id, "id": f"{run_id}:{b['reference']}:{b['kind']}"}
        if b["id"] in {h["break_id"] for h in healed}:
            b["status"] = "investigating"
            b["auto_heal"] = "investigation_case_created"
        else:
            b["status"] = "open"
        _store.put("breaks", b["id"], b)
        stored.append(b)
    emitted = _emit_revenue_events(run_id, ReconRunRequest(platform=platform),
                                   set(result["matched_references"]))
    out = {"run": run, "breaks": stored, "auto_healed": healed,
           "revenue_events_emitted": emitted}
    if req.idempotency_key:
        _store.put("idempotency", f"recon-pull:{req.idempotency_key}", out)
    return out


def _auto_heal(run_id: str, platform: list[ReconRecord], breaks: list[dict]) -> list[dict]:
    """Auto-heal class: platform/ledger shows captured but the PSSP side is
    missing AND the capture is within the tolerance window (recent enough
    that the PSSP report should already contain it) -> auto-create an
    investigation case (deduped per reference)."""
    by_ref = {r.reference: r for r in platform}
    healed: list[dict] = []
    for b in breaks:
        if b.get("kind") != "missing" or b.get("missing_in") != ["pssp"]:
            continue
        rec = by_ref.get(b["reference"])
        if rec is None or not _within_tolerance(rec.date):
            continue
        case_id = f"case:{b['reference']}"
        if _store.get("investigation_cases", case_id) is None:
            case = {"id": case_id, "dedup_key": case_id, "reference": b["reference"],
                    "class": "ledger_captured_pssp_missing",
                    "run_id": run_id, "status": "open",
                    "created_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())}
            _store.put("investigation_cases", case_id, case)
            from meridian_events.envelope import new_envelope
            _outbox.append("nrs.recon.investigation.v1",
                           new_envelope("nrs.recon.investigation.v1", SERVICE, case))
        healed.append({"break_id": f"{run_id}:{b['reference']}:{b['kind']}",
                       "case_id": case_id})
    return healed


def _within_tolerance(date_str: str | None) -> bool:
    if not date_str:
        return True  # undated platform records are always in scope
    try:
        d = time.strptime(date_str[:10], "%Y-%m-%d")
    except ValueError:
        return True
    age_days = (time.time() - time.mktime(d)) / 86400
    return age_days <= AUTO_HEAL_TOLERANCE_DAYS


class IngestRequest(BaseModel):
    side: str  # platform|treasury|pssp_settlement_report
    records: list[ReconRecord]


@app.post("/v1/recon/ingest")
def ingest_records(req: IngestRequest, claims: Claims = Depends(fastapi_dependency())) -> dict:
    """Durable ingest for the pull-mode recon sides (platform records,
    treasury statements, and the sim PSSP settlement report). Records are
    keyed by reference: re-ingesting a reference upserts, never duplicates."""
    colls = {"platform": "platform_records", "treasury": "treasury_records",
             "pssp": "pssp_settlement_report"}
    coll = colls.get(req.side)
    if coll is None:
        raise HTTPException(422, f"side must be one of {sorted(colls)}")
    for r in req.records:
        _store.put(coll, r.reference, r.model_dump())
    return {"side": req.side, "ingested": len(req.records)}


@app.get("/v1/recon/investigations")
def list_investigations(status: str | None = None,
                        claims: Claims = Depends(fastapi_dependency())) -> dict:
    cases = _store.list("investigation_cases")
    if status:
        cases = [c for c in cases if c.get("status") == status]
    return {"cases": cases, "count": len(cases)}


def main() -> None:  # pragma: no cover
    import uvicorn

    uvicorn.run(app, host="0.0.0.0", port=int(os.environ.get("PORT", "8013")))


if __name__ == "__main__":  # pragma: no cover
    main()
