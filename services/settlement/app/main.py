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
from meridian_events.store import open_store

SERVICE = "settlement"
VERSION = "0.1.0"

app = FastAPI(title="Meridian settlement", version=VERSION)

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


SIDES = ("platform", "pssp", "treasury")


def _index(records: list[ReconRecord]) -> dict[str, ReconRecord]:
    out: dict[str, ReconRecord] = {}
    for r in records:
        if r.reference in out:
            # duplicate reference inside one side: keep latest but record break
            out[r.reference] = r
        else:
            out[r.reference] = r
    return out


def reconcile(platform: list[ReconRecord], pssp: list[ReconRecord],
              treasury: list[ReconRecord]) -> dict:
    sides = {"platform": _index(platform), "pssp": _index(pssp), "treasury": _index(treasury)}
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
    result = reconcile(req.platform, req.pssp, req.treasury)
    run_id = req.run_id or f"run-{int(time.time() * 1000)}"
    run = {"run_id": run_id, "at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
           "by": claims.sub, **{k: v for k, v in result.items() if k != "breaks"}}
    _store.put("runs", run_id, run)
    existing = _store.list("breaks")
    for b in result["breaks"]:
        b = {**b, "run_id": run_id, "status": "open",
             "id": f"{run_id}:{b['reference']}:{b['kind']}"}
        _store.put("breaks", b["id"], b)
        existing.append(b)
    return {"run": run, "breaks": result["breaks"]}


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
