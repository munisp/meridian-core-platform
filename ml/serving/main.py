"""Meridian ML serving tier — FastAPI CPU inference service.

Endpoints:
  POST /v1/score/{fraud|credit|graph|anomaly|fusion}   (single or batch)
  GET  /healthz /readyz
  GET  /v1/ab/metrics
  GET  /v1/monitoring/{drift,performance,alerts}

Contract notes (plan-ml.md + HARDENING.md):
  - Loads ACTIVE versions from ml/registry/manifest.json (RegistryClient).
  - Hot-skip: missing/corrupt weights -> 503 RFC7807 problem+json, never crash.
  - AUTH_MODE=dev: HS256 JWT (MERIDIAN_DEV_JWT_SECRET) OR X-Dev-Role header,
    mirroring meridian_events conventions. keycloak mode reserved (H2).
  - Integer kobo in/out: monetary amounts are integers, never floats.
  - Never log raw NIN/TIN/MSISDN: entity identifiers are logged only as
    truncated SHA-256 hashes.
  - Startup NEVER fails because a prod var or a weight file is missing.
"""
from __future__ import annotations

import hashlib
import hmac
import json
import logging
import os
import threading
import time
from typing import Any, Optional

from fastapi import Depends, FastAPI, Request
from fastapi.responses import JSONResponse
from pydantic import BaseModel, Field

from .ab import ABRouter
from .registry_client import RegistryClient, build_mlp_from_state_dict, load_state_dict_b64

logger = logging.getLogger("ml.serving")
logging.basicConfig(level=os.environ.get("LOG_LEVEL", "INFO"))

MODEL_NAMES = ("fraud", "credit", "graph", "anomaly", "fusion")
FUSION_COMPONENTS = ("fraud", "credit", "graph")

AUTH_MODE = os.environ.get("AUTH_MODE", "dev")
DEV_SECRET = os.environ.get("MERIDIAN_DEV_JWT_SECRET", "meridian-dev-secret")


# ---------------------------------------------------------------- auth -----

def _hash_id(value: str) -> str:
    return hashlib.sha256(value.encode("utf-8")).hexdigest()[:16]


def _verify_hs256(token: str) -> Optional[dict]:
    """Minimal HS256 JWT verification (no hard dependency on PyJWT)."""
    try:
        header_b64, payload_b64, sig_b64 = token.split(".")
        import base64

        def _b64d(s: str) -> bytes:
            return base64.urlsafe_b64decode(s + "=" * (-len(s) % 4))

        signing_input = f"{header_b64}.{payload_b64}".encode()
        expected = hmac.new(DEV_SECRET.encode(), signing_input, hashlib.sha256).digest()
        if not hmac.compare_digest(expected, _b64d(sig_b64)):
            return None
        claims = json.loads(_b64d(payload_b64))
        if claims.get("exp") and float(claims["exp"]) < time.time():
            return None
        return claims
    except Exception:
        return None


async def auth_dependency(request: Request) -> dict:
    """Dev mode: HS256 bearer token or X-Dev-Role header. Never crashes;
    unauthenticated requests get role=anonymous with limited rights."""
    if AUTH_MODE == "dev":
        authz = request.headers.get("authorization", "")
        if authz.lower().startswith("bearer "):
            claims = _verify_hs256(authz[7:].strip())
            if claims is not None:
                return {"role": claims.get("role", "service"), "sub": claims.get("sub", "jwt")}
        role = request.headers.get("x-dev-role")
        if role:
            return {"role": role, "sub": "dev"}
        return {"role": "anonymous", "sub": "anon"}
    # keycloak mode: H2 middleware validates upstream; trust stamped caller.
    return {"role": request.headers.get("x-dev-role", "service"), "sub": "keycloak"}


# ------------------------------------------------------------ responses ----

def problem(status: int, title: str, detail: str = "", type_: str = "about:blank") -> JSONResponse:
    return JSONResponse(
        status_code=status,
        media_type="application/problem+json",
        content={"type": type_, "title": title, "status": status, "detail": detail},
    )


# -------------------------------------------------------------- schemas ----

class ScoreRequest(BaseModel):
    entity_id: Optional[str] = Field(default=None, description="Pseudonymised entity id (hashed upstream)")
    features: list[float] = Field(default_factory=list, description="Ordered feature vector")
    amount_kobo: Optional[int] = Field(default=None, description="Integer kobo; never a float")


class BatchScoreRequest(BaseModel):
    records: list[ScoreRequest]


class ScoreResponse(BaseModel):
    model: str
    version: str
    score: float
    arm: str = "champion"
    amount_kobo: Optional[int] = None
    latency_ms: float


# ------------------------------------------------------------ app state ----

class ModelStore:
    """Lazy-loading store of active model versions. Hot-skip semantics:
    a model whose weights are missing/unloadable stays unloaded and its
    endpoint returns 503 — the process never crashes."""

    def __init__(self, registry: RegistryClient):
        self.registry = registry
        self._lock = threading.Lock()
        self._nets: dict[str, Any] = {}
        self._versions: dict[str, str] = {}
        self._failed: dict[str, str] = {}  # model -> reason

    def _load(self, name: str, version: Optional[str] = None):
        import torch  # noqa: F401  (raises ImportError -> caught by caller)

        mv = (
            self.registry.get_version(name, version)
            if version
            else self.registry.active_version(name)
        )
        if mv is None:
            raise FileNotFoundError(f"no manifest entry for model={name} version={version or 'active'}")
        sd = load_state_dict_b64(mv.weights_path)
        net = build_mlp_from_state_dict(sd)
        return net, mv.version

    def get(self, name: str, version: Optional[str] = None):
        """Return (net, version) or raise FileNotFoundError/RuntimeError."""
        key = f"{name}@{version or 'active'}"
        with self._lock:
            if key in self._nets:
                return self._nets[key], self._versions[key]
            if key in self._failed:
                raise FileNotFoundError(self._failed[key])
            try:
                net, ver = self._load(name, version)
                self._nets[key] = net
                self._versions[key] = ver
                logger.info("profile=loaded component=ml-serving model=%s version=%s", name, ver)
                return net, ver
            except Exception as exc:  # hot-skip: remember failure, never crash
                reason = f"{type(exc).__name__}: {exc}"
                self._failed[key] = reason
                logger.warning("component=ml-serving model=%s hot-skip reason=%s", name, reason)
                raise FileNotFoundError(reason)

    def status(self) -> dict:
        out = {}
        for name in MODEL_NAMES:
            mv = self.registry.active_version(name)
            key = f"{name}@active"
            out[name] = {
                "active_version": mv.version if mv else None,
                "loaded": key in self._nets,
                "error": self._failed.get(key),
            }
        return out


def create_app(registry_dir: Optional[str] = None) -> FastAPI:
    registry = RegistryClient(registry_dir)
    store = ModelStore(registry)
    ab = ABRouter()

    # Monitoring is import-guarded: serving works without it.
    perf = drift_mon = None
    try:
        try:
            from ..monitoring.performance import PerformanceMonitor
            from ..monitoring.drift import DriftMonitor
        except ImportError:  # running as top-level `serving` package
            from monitoring.performance import PerformanceMonitor
            from monitoring.drift import DriftMonitor

        perf = PerformanceMonitor()
        drift_mon = DriftMonitor(registry)
    except Exception as exc:  # pragma: no cover
        logger.warning("component=ml-serving monitoring disabled reason=%s", exc)

    app = FastAPI(title="Meridian ML Serving", version="1.0.0")
    app.state.store = store
    app.state.ab = ab
    app.state.perf = perf
    app.state.drift = drift_mon

    profile = "prod" if os.environ.get("KAFKA_BROKERS") else "dev"
    logger.info("profile=%s component=ml-serving auth_mode=%s", profile, AUTH_MODE)

    @app.get("/healthz")
    async def healthz():
        return {"status": "ok", "component": "ml-serving"}

    @app.get("/readyz")
    async def readyz():
        # Ready even with zero models loaded: hot-skip is per-endpoint (503).
        return {"status": "ready", "models": store.status()}

    @app.get("/v1/ab/metrics")
    async def ab_metrics(_claims: dict = Depends(auth_dependency)):
        return ab.snapshot()

    @app.get("/v1/monitoring/performance")
    async def mon_perf(_claims: dict = Depends(auth_dependency)):
        if perf is None:
            return problem(503, "monitoring unavailable", "performance monitor not loaded")
        return perf.snapshot()

    @app.get("/v1/monitoring/drift")
    async def mon_drift(_claims: dict = Depends(auth_dependency)):
        if drift_mon is None:
            return problem(503, "monitoring unavailable", "drift monitor not loaded")
        return drift_mon.report()

    @app.get("/v1/monitoring/alerts")
    async def mon_alerts(_claims: dict = Depends(auth_dependency)):
        alerts = []
        if perf is not None:
            alerts.extend(perf.recent_alerts())
        if drift_mon is not None:
            alerts.extend(drift_mon.recent_alerts())
        return {"alerts": alerts[-200:]}

    def _score_one(model: str, req: ScoreRequest) -> ScoreResponse:
        t0 = time.perf_counter()
        entity_key = req.entity_id or "anonymous"
        arm = ab.serving_arm(entity_key)
        challenger_arm = ab.assign(entity_key)

        version_override = None
        if ab.config.enabled and model == ab.config.model:
            if arm == "challenger":
                version_override = ab.config.challenger_version
            elif ab.config.champion_version:
                version_override = ab.config.champion_version

        try:
            if model == "fusion":
                score, version = _fusion_score(req)
            else:
                net, version = store.get(model, version_override)
                score = _infer(net, req.features)
        except FileNotFoundError as exc:
            raise _HotSkip(str(exc))

        latency_ms = (time.perf_counter() - t0) * 1000.0

        # Shadow mode: challenger scored but not served.
        if (
            ab.config.enabled
            and ab.config.shadow
            and model == ab.config.model
            and challenger_arm == "challenger"
            and model != "fusion"
        ):
            try:
                cnet, _ = store.get(model, ab.config.challenger_version)
                cscore = _infer(cnet, req.features)
                ab.record("challenger", served=False, score=cscore, latency_ms=latency_ms)
            except FileNotFoundError:
                ab.record("challenger", served=False, score=None, latency_ms=0, error=True)

        ab.record(arm if model == ab.config.model else "champion",
                  served=True, score=score, latency_ms=latency_ms)
        if perf is not None:
            perf.record(model=model, score=score, latency_ms=latency_ms)
        if drift_mon is not None and req.features:
            drift_mon.observe(model, req.features)

        logger.info(
            "component=ml-serving model=%s version=%s arm=%s entity=%s latency_ms=%.2f",
            model, version, arm, _hash_id(entity_key), latency_ms,
        )
        return ScoreResponse(
            model=model, version=version, score=score, arm=arm,
            amount_kobo=req.amount_kobo, latency_ms=round(latency_ms, 3),
        )

    class _HotSkip(Exception):
        pass

    def _infer(net, features: list[float]) -> float:
        import torch

        if not features:
            raise FileNotFoundError("empty feature vector")
        with torch.no_grad():
            x = torch.tensor([features], dtype=torch.float32)
            out = net(x)
            val = float(out.reshape(-1)[0])
        # squash to a probability-like score
        return 1.0 / (1.0 + pow(2.718281828459045, -val)) if abs(val) < 700 else (1.0 if val > 0 else 0.0)

    def _fusion_score(req: ScoreRequest):
        scores, versions = [], []
        for comp in FUSION_COMPONENTS:
            try:
                net, ver = store.get(comp)
                scores.append(_infer(net, req.features))
                versions.append(f"{comp}={ver}")
            except FileNotFoundError:
                continue
        if not scores:
            raise FileNotFoundError("fusion: no component models loaded")
        return sum(scores) / len(scores), "+".join(versions)

    @app.post("/v1/score/{model}", response_model=None)
    async def score(model: str, request: Request, _claims: dict = Depends(auth_dependency)):
        if model not in MODEL_NAMES:
            return problem(404, "unknown model", f"model must be one of {MODEL_NAMES}")
        body = await request.json()
        try:
            if isinstance(body, dict) and "records" in body:
                batch = BatchScoreRequest(**body)
                results, errors = [], 0
                for rec in batch.records:
                    try:
                        results.append(_score_one(model, rec).model_dump())
                    except _HotSkip as exc:
                        errors += 1
                        results.append({"error": str(exc)})
                if errors == len(batch.records) and batch.records:
                    return problem(503, "model unavailable", f"model={model} has no loadable weights")
                return {"model": model, "count": len(results), "results": results}
            req = ScoreRequest(**body)
        except ValueError as exc:
            return problem(422, "invalid request", str(exc))
        try:
            return _score_one(model, req)
        except _HotSkip as exc:
            return problem(503, "model unavailable", f"model={model}: {exc}")

    return app


app = create_app()


def main() -> None:  # pragma: no cover
    import uvicorn

    uvicorn.run(app, host=os.environ.get("HOST", "0.0.0.0"),
                port=int(os.environ.get("PORT", "8090")))


if __name__ == "__main__":  # pragma: no cover
    main()
