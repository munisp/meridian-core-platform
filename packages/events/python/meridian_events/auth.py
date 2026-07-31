"""HS256 JWT auth per SPEC 1.3 (dev secret MERIDIAN_DEV_JWT_SECRET).

Also provides FastAPI dependency helpers honouring AUTH_MODE=dev with the
X-Dev-Role: admin|operator|auditor header.
"""
from __future__ import annotations

import base64
import hashlib
import hmac
import json
import os
import time
from dataclasses import dataclass, field


class AuthError(Exception):
    pass


def _secret() -> bytes:
    return os.environ.get("MERIDIAN_DEV_JWT_SECRET", "meridian-dev-secret-change-me").encode()


def _b64e(b: bytes) -> str:
    return base64.urlsafe_b64encode(b).rstrip(b"=").decode()


def _b64d(s: str) -> bytes:
    return base64.urlsafe_b64decode(s + "=" * (-len(s) % 4))


@dataclass
class Claims:
    sub: str
    roles: list[str] = field(default_factory=list)
    tenant_id: str = ""
    exp: int = 0
    iat: int = 0

    def has_role(self, role: str) -> bool:
        return role in self.roles


def sign_hs256(claims: Claims, ttl: int = 3600) -> str:
    now = int(time.time())
    claims.iat = claims.iat or now
    claims.exp = claims.exp or now + ttl
    header = _b64e(json.dumps({"alg": "HS256", "typ": "JWT"}).encode())
    payload = _b64e(json.dumps({
        "sub": claims.sub, "roles": claims.roles, "tenant_id": claims.tenant_id,
        "exp": claims.exp, "iat": claims.iat,
    }).encode())
    body = f"{header}.{payload}"
    sig = _b64e(hmac.new(_secret(), body.encode(), hashlib.sha256).digest())
    return f"{body}.{sig}"


def verify_hs256(token: str) -> Claims:
    parts = token.split(".")
    if len(parts) != 3:
        raise AuthError("malformed token")
    body = f"{parts[0]}.{parts[1]}"
    expected = hmac.new(_secret(), body.encode(), hashlib.sha256).digest()
    try:
        got = _b64d(parts[2])
    except Exception as exc:
        raise AuthError("bad signature encoding") from exc
    if not hmac.compare_digest(expected, got):
        raise AuthError("signature mismatch")
    try:
        payload = json.loads(_b64d(parts[1]))
    except Exception as exc:
        raise AuthError("bad payload") from exc
    if payload.get("exp") and int(payload["exp"]) < int(time.time()):
        raise AuthError("token expired")
    return Claims(
        sub=payload.get("sub", ""), roles=list(payload.get("roles", [])),
        tenant_id=payload.get("tenant_id", ""), exp=int(payload.get("exp", 0)),
        iat=int(payload.get("iat", 0)),
    )


DEV_ROLES = {"admin", "operator", "auditor", "board"}

# --- Keycloak OIDC (AUTH_MODE=keycloak; HARDENING H2) ---
#
# RS256 verification against the realm JWKS via PyJWT[crypto] + PyJWKClient
# (PyJWKClient caches the key set and refetches on unknown kid), validating
# iss/exp/aud and mapping realm_access.roles -> Claims.roles.
_KEYCLOAK_JWKS_CLIENT = None


def _keycloak_jwks_client():
    global _KEYCLOAK_JWKS_CLIENT
    if _KEYCLOAK_JWKS_CLIENT is not None:
        return _KEYCLOAK_JWKS_CLIENT
    issuer = os.environ.get("KEYCLOAK_ISSUER", "").rstrip("/")
    jwks_url = os.environ.get("KEYCLOAK_JWKS_URL") or (
        f"{issuer}/protocol/openid-connect/certs" if issuer else "")
    if not jwks_url:
        raise AuthError("KEYCLOAK_ISSUER or KEYCLOAK_JWKS_URL required")
    try:
        from jwt import PyJWKClient  # PyJWT[crypto]
    except ImportError as exc:
        raise AuthError("PyJWT[crypto] required for AUTH_MODE=keycloak") from exc
    _KEYCLOAK_JWKS_CLIENT = PyJWKClient(jwks_url, cache_keys=True, lifespan=300)
    return _KEYCLOAK_JWKS_CLIENT


def verify_keycloak(token: str) -> Claims:
    """Verify an RS256 Keycloak JWT (iss/exp/aud + realm role mapping)."""
    import jwt  # PyJWT[crypto]

    issuer = os.environ.get("KEYCLOAK_ISSUER", "").rstrip("/") or None
    audience = os.environ.get("KEYCLOAK_AUDIENCE") or None
    try:
        signing_key = _keycloak_jwks_client().get_signing_key_from_jwt(token)
        payload = jwt.decode(
            token,
            signing_key.key,
            algorithms=["RS256"],
            audience=audience,
            issuer=issuer,
            options={
                "require": ["exp", "sub"],
                "verify_aud": audience is not None,
                "verify_iss": issuer is not None,
            },
        )
    except AuthError:
        raise
    except Exception as exc:  # jwt.PyJWTError and friends
        raise AuthError(str(exc)) from exc
    roles = list(payload.get("roles") or [])
    if not roles:
        roles += list((payload.get("realm_access") or {}).get("roles") or [])
        if audience:
            ra = (payload.get("resource_access") or {}).get(audience) or {}
            roles += list(ra.get("roles") or [])
    return Claims(
        sub=payload.get("sub", ""),
        roles=sorted(set(r for r in roles if r)),
        tenant_id=payload.get("tenant_id", ""),
        exp=int(payload.get("exp", 0)),
        iat=int(payload.get("iat", 0)),
    )


def _verify_bearer(token: str) -> Claims:
    if os.environ.get("AUTH_MODE", "dev") == "keycloak":
        return verify_keycloak(token)
    return verify_hs256(token)


def fastapi_dependency(required_roles: set[str] | None = None):
    """FastAPI dependency enforcing SPEC 1.3 auth.

    Usage: ``claims: Claims = Depends(fastapi_dependency({"admin"}))``
    """
    from fastapi import Header, HTTPException

    def dep(authorization: str | None = Header(default=None),
            x_dev_role: str | None = Header(default=None),
            x_tenant_id: str | None = Header(default=None)) -> Claims:
        claims: Claims | None = None
        if authorization and authorization.startswith("Bearer "):
            try:
                claims = _verify_bearer(authorization[len("Bearer "):])
            except AuthError as exc:
                raise HTTPException(401, f"invalid bearer token: {exc}") from exc
        elif os.environ.get("AUTH_MODE", "dev") == "dev" and x_dev_role in DEV_ROLES:
            claims = Claims(sub=f"dev-{x_dev_role}", roles=[x_dev_role],
                            tenant_id=x_tenant_id or "")
        if claims is None:
            raise HTTPException(401, "Bearer JWT or X-Dev-Role required")
        if required_roles and not any(claims.has_role(r) for r in required_roles):
            raise HTTPException(403, f"role in {sorted(required_roles)} required")
        return claims

    return dep
