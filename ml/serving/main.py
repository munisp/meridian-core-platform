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
from .registry_client import (
    RegistryClient,
    build_credit_from_state_dict,
    build_mlp_from_state_dict,
    load_state_dict_b64,
    unwrap_payload,
)

logger = logging.getLogger("ml.serving")
logging.basicConfig(level=os.environ.get("LOG_LEVEL", "INFO"))

MODEL_NAMES = ("fraud", "credit", "graph", "anomaly", "fusion")
FUSION_COMPONENTS = ("fraud", "credit", "graph")

# Serving endpoint name -> registry artifact name (manifest keys). The
# manifest registers training names (fraud_mlp, ...); fall back to the
# endpoint name itself so old single-name fixtures keep working.
MODEL_REGISTRY_NAMES = {
    "fraud": "fraud_mlp",
    "credit": "credit_score",
    "graph": "gnn_gcn",
    "anomaly": "fraud_autoencoder",
    "fusion": "fraudfusion",
}

AUTH_MODE = os.environ.get("AUTH_MODE", "dev")
PROFILE = os.environ.get("PROFILE", "dev")
DEV_SECRET = os.environ.get("MERIDIAN_DEV_JWT_SECRET", "meridian-dev-secret")


def _validate_auth_config() -> None:
    """A1-07: fail closed on misconfigured prod keycloak mode.

    In PROFILE=prod with AUTH_MODE=keycloak, BOTH KEYCLOAK_AUDIENCE and
    KEYCLOAK_ISSUER are mandatory: without audience/issuer pinning, any token
    signed by any key in the realm JWKS (minted for any client) is accepted
    (aud/iss confusion). Refuse to boot rather than serve unverified auth.
    """
    if AUTH_MODE == "keycloak" and PROFILE == "prod":
        missing = [v for v in ("KEYCLOAK_AUDIENCE", "KEYCLOAK_ISSUER") if not os.environ.get(v)]
        if missing:
            raise RuntimeError(
                "PROFILE=prod with AUTH_MODE=keycloak requires " + ", ".join(missing)
            )


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
    # AUTH_MODE=keycloak: fail closed. X-Dev-Role is NEVER honoured outside
    # AUTH_MODE=dev (audit: full auth bypass in prod mode). Bearer tokens are
    # verified against the Keycloak JWKS when ML_JWKS_URL (or
    # KEYCLOAK_JWKS_URL) is configured and PyJWT is available; otherwise every
    # request is rejected rather than falling back to a header trust.
    if AUTH_MODE == "keycloak":
        authz = request.headers.get("authorization", "")
        if authz.lower().startswith("bearer "):
            claims = _verify_keycloak(authz[7:].strip())
            if claims is not None:
                roles = claims.get("roles") or claims.get("realm_access", {}).get("roles") or []
                role = claims.get("role") or (roles[0] if roles else "service")
                return {"role": role, "sub": claims.get("sub", "keycloak")}
        from fastapi import HTTPException

        raise HTTPException(
            status_code=401,
            detail="AUTH_MODE=keycloak: a verifiable Bearer token is required "
                   "(X-Dev-Role is not honoured outside AUTH_MODE=dev)",
        )
    # Unknown mode: fail closed too.
    from fastapi import HTTPException

    raise HTTPException(status_code=401, detail=f"unsupported AUTH_MODE={AUTH_MODE!r}")


def _verify_keycloak(token: str) -> Optional[dict]:
    """Verify an RS256 Keycloak token via JWKS. Returns claims or None.
    Requires PyJWT + a configured JWKS URL; without them verification is
    impossible and we return None (fail closed)."""
    jwks_url = os.environ.get("ML_JWKS_URL") or os.environ.get("KEYCLOAK_JWKS_URL")
    if not jwks_url:
        logger.warning("component=ml-serving AUTH_MODE=keycloak but no JWKS URL configured; denying")
        return None
    try:
        import jwt
        from jwt import PyJWKClient
    except ImportError:
        logger.warning("component=ml-serving PyJWT unavailable in keycloak mode; denying")
        return None
    # A1-07: audience + issuer are pinned, never verify_aud=False. Without
    # KEYCLOAK_AUDIENCE there is nothing to bind the token to this service —
    # fail closed instead of accepting tokens minted for any realm client.
    audience = os.environ.get("KEYCLOAK_AUDIENCE")
    if not audience:
        logger.warning("component=ml-serving keycloak mode but KEYCLOAK_AUDIENCE unset; denying")
        return None
    issuer = os.environ.get("KEYCLOAK_ISSUER")
    try:
        client = _jwks_client(jwks_url)
        key = client.get_signing_key_from_jwt(token).key
        options: dict[str, Any] = {"require": ["exp", "aud"]}
        kwargs: dict[str, Any] = {"audience": audience}
        if issuer:
            kwargs["issuer"] = issuer
        return jwt.decode(token, key, algorithms=["RS256"], options=options, **kwargs)
    except Exception as exc:
        logger.info("component=ml-serving keycloak verify failed: %s", exc)
        return None


_JWKS_CLIENTS: dict[str, Any] = {}


def _jwks_client(url: str):
    if url not in _JWKS_CLIENTS:
        from jwt import PyJWKClient

        _JWKS_CLIENTS[url] = PyJWKClient(url)
    return _JWKS_CLIENTS[url]


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
    context: dict = Field(default_factory=dict,
                          description="Optional raw attributes for the fusion rules stream "
                                      "(is_night, vat_rate, ch_einvoice, days_since_prev_tx, amount_vs_p90)")


class BatchScoreRequest(BaseModel):
    records: list[ScoreRequest]


class BundleRequest(BaseModel):
    detections: list[dict] = Field(default_factory=list)
    rule_packs: list[str] = Field(default_factory=list)


class ChangePointRequest(BaseModel):
    entity: str
    metric: str = "filing_count"
    values: list[float] = Field(default_factory=list)
    detector: str = "cusum"  # cusum | bocpd_lite


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

    def _resolve(self, name: str, version: Optional[str] = None):
        """Resolve the manifest entry, trying the training-side artifact name
        (fraud -> fraud_mlp) before the endpoint name itself."""
        candidates = [MODEL_REGISTRY_NAMES.get(name, name)]
        if candidates[0] != name:
            candidates.append(name)
        for cand in candidates:
            mv = (
                self.registry.get_version(cand, version)
                if version
                else self.registry.active_version(cand)
            )
            if mv is not None:
                return mv
        return None

    def _load(self, name: str, version: Optional[str] = None):
        import torch  # noqa: F401  (raises ImportError -> caught by caller)

        mv = self._resolve(name, version)
        if mv is None:
            raise FileNotFoundError(f"no manifest entry for model={name} version={version or 'active'}")
        if mv.name == "fraudfusion":
            # Fusion artifact: learned stream weights + AE calibration, not an
            # MLP state_dict. Kept as raw payload for _fusion_score.
            payload = load_state_dict_b64(mv.weights_path)
            if not isinstance(payload, dict) or "weights" not in payload:
                raise ValueError("fraudfusion artifact missing learned weights")
            return None, mv.version, payload
        state_dict, meta = unwrap_payload(load_state_dict_b64(mv.weights_path))
        if mv.name == "credit_score" or any(k.startswith("stages.") for k in state_dict):
            net = build_credit_from_state_dict(state_dict)
        else:
            net = build_mlp_from_state_dict(state_dict)
        return net, mv.version, meta

    def get(self, name: str, version: Optional[str] = None):
        """Return (net, version, meta) or raise FileNotFoundError/RuntimeError.
        net is None for the fusion artifact (payload in meta)."""
        key = f"{name}@{version or 'active'}"
        with self._lock:
            if key in self._nets:
                net, ver, meta = self._nets[key]
                return net, ver, meta
            if key in self._failed:
                raise FileNotFoundError(self._failed[key])
            try:
                net, ver, meta = self._load(name, version)
                self._nets[key] = (net, ver, meta)
                logger.info("profile=loaded component=ml-serving model=%s version=%s", name, ver)
                return net, ver, meta
            except Exception as exc:  # hot-skip: remember failure, never crash
                reason = f"{type(exc).__name__}: {exc}"
                self._failed[key] = reason
                logger.warning("component=ml-serving model=%s hot-skip reason=%s", name, reason)
                raise FileNotFoundError(reason)

    def active_registry_version(self, name: str) -> Optional[str]:
        mv = self._resolve(name)
        return mv.version if mv else None

    def status(self) -> dict:
        out = {}
        for name in MODEL_NAMES:
            key = f"{name}@active"
            out[name] = {
                "active_version": self.active_registry_version(name),
                "loaded": key in self._nets,
                "error": self._failed.get(key),
            }
        return out


def create_app(registry_dir: Optional[str] = None) -> FastAPI:
    _validate_auth_config()  # A1-07: prod keycloak mode requires aud+iss config
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
                net, version, meta = store.get(model, version_override)
                score = _infer(net, req.features, meta)
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
                cnet, _, cmeta = store.get(model, ab.config.challenger_version)
                cscore = _infer(cnet, req.features, cmeta)
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

    def _apply_scaler(features: list[float], meta: dict) -> list[float]:
        """Standardise with the scaler stored at training time, when present.
        mu/sd are dicts keyed by feature name; their insertion order matches
        the training feature order (ml/data/synthetic FEATURES)."""
        scaler = (meta or {}).get("scaler") or {}
        mu, sd = scaler.get("mu"), scaler.get("sd")
        if not mu or not sd:
            return features
        mus = [float(v) for v in mu.values()]
        sds = [max(abs(float(v)), 1e-6) for v in sd.values()]
        if len(mus) != len(features):
            logger.warning("component=ml-serving scaler dim %d != features dim %d; unscored scaling skipped",
                           len(mus), len(features))
            return features
        return [(x - m) / s for x, m, s in zip(features, mus, sds)]

    def _infer(net, features: list[float], meta: Optional[dict] = None) -> float:
        import torch

        if not features:
            raise FileNotFoundError("empty feature vector")
        meta = meta or {}
        x_in = _apply_scaler(features, meta)
        with torch.no_grad():
            x = torch.tensor([x_in], dtype=torch.float32)
            out = net(x)
            if "ae_lo" in meta:  # autoencoder: score = calibrated reconstruction error
                err = float(((out - x) ** 2).mean())
                lo, hi = float(meta.get("ae_lo", 0.0)), float(meta.get("ae_hi", 1.0))
                return min(max((err - lo) / max(hi - lo, 1e-6), 0.0), 1.0)
            val = float(out.reshape(-1)[0])
        # squash to a probability-like score
        return 1.0 / (1.0 + pow(2.718281828459045, -val)) if abs(val) < 700 else (1.0 if val > 0 else 0.0)

    def _rules_score(req: ScoreRequest) -> float:
        """Deterministic rules stream for fusion (mirrors
        ml/models/fraudfusion.rule_scores on the attributes available at
        request time; absent attributes contribute 0)."""
        ctx = req.context or {}
        amt_naira = (req.amount_kobo or int(ctx.get("amount_kobo") or 0)) / 100.0
        s_struct = min(max((amt_naira - 8_000_000) / 2_000_000, 0.0), 1.0)
        s_night = float(ctx.get("is_night") or 0) * min(max(float(ctx.get("amount_vs_p90") or 0) * 4, 0.0), 1.0)
        vat = float(ctx.get("vat_rate") or 0) * 0.2
        s_vat = float(ctx.get("ch_einvoice") or 0) * min(max((0.075 - vat) / 0.075, 0.0), 1.0)
        s_dorm = min(max(float(ctx.get("days_since_prev_tx") or 0) * 2, 0.0), 1.0) * min(amt_naira / 5_000_000, 1.0)
        return max(s_struct, s_night, s_vat, s_dorm)

    def _fusion_score(req: ScoreRequest):
        """Late fusion using the LEARNED FraudFusion softmax weights over the
        (mlp, ae, gnn, rules) streams — replaces the old plain-mean bug
        (audit: flagship model's value was not served)."""
        try:
            _, fver, fused = store.get("fusion")
            weights = {k: float(v) for k, v in (fused.get("weights") or {}).items()}
        except FileNotFoundError:
            # No learned artifact (e.g. minimal dev fixture): equal weights
            # over model streams, rules stream still included.
            fver = "unlearned-fallback"
            weights = {"mlp": 1.0, "ae": 1.0, "gnn": 1.0, "rules": 1.0}
        if not weights:
            raise FileNotFoundError("fusion: artifact has no learned weights")

        def _stream(model: str) -> Optional[float]:
            try:
                net, _, meta = store.get(model)
                if net is None:
                    return None
                return _infer(net, req.features, meta)
            except FileNotFoundError:
                return None
            except Exception as exc:  # dim mismatch etc: stream unavailable
                logger.info("component=ml-serving fusion stream=%s unavailable: %s", model, exc)
                return None

        s_mlp = _stream("fraud")
        s_ae = _stream("anomaly")
        s_gnn = _stream("graph")
        s_rules = _rules_score(req)
        streams = {"mlp": s_mlp, "ae": s_ae, "gnn": s_gnn, "rules": s_rules}
        # Renormalise the learned weights over the streams that are actually
        # available; the rules stream is deterministic and always present.
        avail = {k: v for k, v in streams.items() if v is not None and weights.get(k) is not None}
        if not avail:
            raise FileNotFoundError("fusion: no component models loaded")
        wsum = sum(weights[k] for k in avail)
        score = sum(weights[k] / wsum * avail[k] for k in avail)
        version = f"fraudfusion={fver}(" + "+".join(sorted(avail)) + ")"
        return score, version

    @app.post("/v1/audit/bundle", response_model=None)
    async def audit_bundle(req: BundleRequest, _claims: dict = Depends(auth_dependency)):
        """I2 (REAL): bundle GNN ring detections into single audit cases with
        shared-evidence references."""
        try:
            from .audit_bundle import bundle_audit_cases
        except ImportError:  # top-level `serving` package
            from audit_bundle import bundle_audit_cases
        versions = {n: store.active_registry_version(n) for n in MODEL_NAMES}
        return bundle_audit_cases(req.detections, model_versions=versions,
                                  rule_packs=req.rule_packs)

    @app.post("/v1/monitoring/changepoint", response_model=None)
    async def changepoint(req: ChangePointRequest, _claims: dict = Depends(auth_dependency)):
        """I6 (REAL): CUSUM / BOCPD-lite behaviour change-point detection;
        emits nrs.ml.changepoint.v1 alerts."""
        if not req.values:
            return problem(422, "invalid request", "values must be a non-empty series")
        try:
            try:
                from ..monitoring.changepoint import detect_stream
            except ImportError:
                from monitoring.changepoint import detect_stream
            alerts = detect_stream(req.entity, req.metric, req.values, detector=req.detector)
        except ValueError as exc:
            return problem(422, "invalid request", str(exc))
        return {"entity": req.entity, "metric": req.metric, "detector": req.detector,
                "alert_count": len(alerts), "alerts": alerts}

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
