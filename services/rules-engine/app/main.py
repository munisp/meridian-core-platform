"""rules-engine — generic rp-* evaluation API (SPEC 2).

POST /v1/evaluate {pack_id, version, context} -> {decision, trace[]}
GET  /v1/packs -> loaded packs
"""
from __future__ import annotations

import os

from fastapi import Depends, FastAPI, HTTPException
from pydantic import BaseModel, Field

from meridian_events.auth import Claims, fastapi_dependency

from .evaluator import evaluate
from .packloader import PackLoader, PackNotFound

SERVICE = "rules-engine"
VERSION = "0.1.0"

app = FastAPI(title="Meridian rules-engine", version=VERSION)
loader = PackLoader()


class EvaluateRequest(BaseModel):
    pack_id: str = Field(..., pattern=r"^rp-[a-z0-9][a-z0-9-]*$")
    version: str | None = None  # semver or null/"latest"
    context: dict = Field(default_factory=dict)


class EvaluateResponse(BaseModel):
    pack: str
    pack_status: str | None
    subject_to_regazette: bool
    matched: bool
    decision: dict | None
    trace: list[dict]


@app.get("/healthz")
def healthz() -> dict:
    return {"status": "ok", "service": SERVICE, "version": VERSION}


@app.get("/readyz")
def readyz() -> dict:
    return {"status": "ok", "service": SERVICE, "version": VERSION}


@app.post("/v1/evaluate", response_model=EvaluateResponse)
def evaluate_endpoint(req: EvaluateRequest,
                      claims: Claims = Depends(fastapi_dependency())) -> dict:
    try:
        pack = loader.get(req.pack_id, req.version)
    except PackNotFound:
        raise HTTPException(404, f"pack {req.pack_id}@{req.version or 'latest'} not found") from None
    try:
        result = evaluate(pack, req.context)
    except Exception as exc:  # noqa: BLE001 - formula errors etc. -> 422 with detail
        raise HTTPException(422, f"evaluation failed: {exc}") from exc
    return result


@app.get("/v1/packs")
def list_packs(claims: Claims = Depends(fastapi_dependency())) -> dict:
    return {"packs": loader.list_loaded()}


def main() -> None:  # pragma: no cover
    import uvicorn

    uvicorn.run(app, host="0.0.0.0", port=int(os.environ.get("PORT", "8001")))


if __name__ == "__main__":  # pragma: no cover
    main()
