"""reg-watch — gate & gazette monitor (SPEC 2).

Gates (G1 CTCs, G2 Rivers case, G8 presumptive reg, carf.transmit_enabled,
qdmtt_upgrade...) are armed/disarmed state with board-authorized flips
(role board/admin). Gazette watch tracks monitored regulatory sources.
"""
from __future__ import annotations

import os
import time
from pathlib import Path

from fastapi import Depends, FastAPI, HTTPException
from pydantic import BaseModel

from meridian_events.auth import Claims, fastapi_dependency
from meridian_events.store import open_store

SERVICE = "reg-watch"
VERSION = "0.1.0"

app = FastAPI(title="Meridian reg-watch", version=VERSION)

DATA_DIR = Path(os.environ.get("DATA_DIR", "./data"))
DATA_DIR.mkdir(parents=True, exist_ok=True)
_store = open_store(DATA_DIR)

# [seed] Gate registry: id, description, default state, what it blocks.
SEED_GATES = [
    {"id": "g1_ctcs_confirmed", "description": "G1: CTCs (gazetted copies) confirmed for 2024/2025 regs",
     "state": "armed", "blocks": "packs with subject_to_regazette=true leaving simulation"},
    {"id": "g2_rivers_case", "description": "G2: Rivers State litigation on VAT administration resolved",
     "state": "armed", "blocks": "rp-vat-attribution-mode switching to state"},
    {"id": "g8_presumptive_reg", "description": "G8: presumptive taxation regulation gazetted",
     "state": "armed", "blocks": "presumptive payment capture (post-regulation gate)"},
    {"id": "carf.transmit_enabled", "description": "CARF XML transmission to partner jurisdictions enabled",
     "state": "disarmed", "blocks": "CARF outbound transmission"},
    {"id": "carf.gate.changed", "description": "CARF gate state changed (notification latch)",
     "state": "disarmed", "blocks": "informational"},
    {"id": "qdmtt_upgrade", "description": "QDMTT upgrade: ETR computes QDMTT instead of IIR top-up path",
     "state": "disarmed", "blocks": "etr service computation path selection"},
    {"id": "ombud.rules_active", "description": "Ombud procedure rules (rp-procedure-ombud) activated",
     "state": "armed", "blocks": "ombud case intake"},
    {"id": "jrb.single_filing", "description": "JRB single-filing pilot enabled",
     "state": "disarmed", "blocks": "wf-jrb-single-filing"},
]

# [seed] gazette watch sources
SEED_GAZETTE = [
    {"source": "Federal Republic of Nigeria Official Gazette", "url": "https://gazette.africa/ng",
     "last_checked": None, "findings": []},
    {"source": "NRS circulars feed", "url": "https://nrs.gov.ng/circulars",
     "last_checked": None, "findings": []},
]


def _init() -> None:
    if _store.get("meta", "seeded"):
        return
    for g in SEED_GATES:
        _store.put("gates", g["id"], {**g, "flipped_at": None, "flipped_by": None, "flip_reason": None})
    for src in SEED_GAZETTE:
        _store.put("gazette", src["source"], src)
    _store.put("meta", "seeded", {"at": time.time()})


_init()


@app.get("/healthz")
def healthz() -> dict:
    return {"status": "ok", "service": SERVICE, "version": VERSION}


@app.get("/readyz")
def readyz() -> dict:
    return {"status": "ok", "service": SERVICE, "version": VERSION}


@app.get("/v1/gates")
def gates(claims: Claims = Depends(fastapi_dependency())) -> dict:
    return {"gates": _store.list("gates")}


class FlipRequest(BaseModel):
    state: str  # armed|disarmed
    reason: str
    authorization_ref: str  # board minute / approval reference


@app.post("/v1/gates/{gate_id}/flip")
def flip(gate_id: str, req: FlipRequest,
         claims: Claims = Depends(fastapi_dependency({"board", "admin"}))) -> dict:
    gate = _store.get("gates", gate_id)
    if gate is None:
        raise HTTPException(404, f"gate {gate_id}")
    if req.state not in ("armed", "disarmed"):
        raise HTTPException(422, "state must be armed|disarmed")
    if not req.authorization_ref:
        raise HTTPException(422, "authorization_ref (board minute) required")
    if gate["state"] == req.state:
        return {"gate": gate, "note": "no-op: already in requested state"}
    gate = {**gate, "state": req.state, "flipped_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
            "flipped_by": claims.sub, "flip_reason": req.reason,
            "authorization_ref": req.authorization_ref}
    _store.put("gates", gate_id, gate)
    # audit trail of flips
    flips = _store.get("audit", "flips") or []
    flips.append({"gate": gate_id, "to": req.state, "by": claims.sub,
                  "at": gate["flipped_at"], "ref": req.authorization_ref, "reason": req.reason})
    _store.put("audit", "flips", flips)
    return {"gate": gate}


@app.get("/v1/gates/{gate_id}")
def gate(gate_id: str, claims: Claims = Depends(fastapi_dependency())) -> dict:
    g = _store.get("gates", gate_id)
    if g is None:
        raise HTTPException(404, f"gate {gate_id}")
    return {"gate": g}


@app.get("/v1/gazette-watch")
def gazette_watch(claims: Claims = Depends(fastapi_dependency())) -> dict:
    sources = _store.list("gazette")
    return {"sources": sources, "note": "[simulated] dev fixture sources; live fetchers "
            "poll gazette/circular endpoints in deployed profile"}


class FindingRequest(BaseModel):
    source: str
    finding: str
    reference: str | None = None


@app.post("/v1/gazette-watch/findings")
def add_finding(req: FindingRequest,
                claims: Claims = Depends(fastapi_dependency({"board", "admin", "operator"}))) -> dict:
    src = _store.get("gazette", req.source)
    if src is None:
        raise HTTPException(404, f"source {req.source}")
    src = dict(src)
    src.setdefault("findings", []).append(
        {"finding": req.finding, "reference": req.reference,
         "at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()), "by": claims.sub})
    src["last_checked"] = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
    _store.put("gazette", req.source, src)
    return {"source": src}


def main() -> None:  # pragma: no cover
    import uvicorn

    uvicorn.run(app, host="0.0.0.0", port=int(os.environ.get("PORT", "8014")))


if __name__ == "__main__":  # pragma: no cover
    main()
