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
    settlement references. Docs are flat, timestamped, integer-kobo."""
    by_ref = {r.reference: r for r in req.platform}
    n = 0
    for ref in sorted(matched):
        rec = by_ref.get(ref)
        if rec is None:
            continue
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


@app.post("/v1/refunds/fasttrack")
def refund_fasttrack(req: FastTrackRequest,
                     claims: Claims = Depends(fastapi_dependency())) -> dict:
    """I3 (REAL): decide the refund lane from credit score + compliance
    history with rule caps; manual-review lane emits a fallback event."""
    from .refund import decide_refund_lane

    try:
        doc = decide_refund_lane(
            tin_hash=req.tin_hash, amount_kobo=req.amount_kobo,
            credit_score=req.credit_score, filings_on_time=req.filings_on_time,
            filings_total=req.filings_total, prior_breaks=req.prior_breaks,
            tax_type=req.tax_type)
    except ValueError as exc:
        raise HTTPException(422, str(exc)) from exc
    _store.put("refund_decisions", f"{doc['decided_at']}:{req.tin_hash}", doc)
    if doc["lane"] == "manual_review":
        event = {"type": "nrs.refund.manual_review.v1", "decision": doc,
                 "queued_at": doc["decided_at"]}
        _store.put("refund_manual_review", f"{doc['decided_at']}:{req.tin_hash}", event)
    return doc


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


def main() -> None:  # pragma: no cover
    import uvicorn

    uvicorn.run(app, host="0.0.0.0", port=int(os.environ.get("PORT", "8013")))


if __name__ == "__main__":  # pragma: no cover
    main()
