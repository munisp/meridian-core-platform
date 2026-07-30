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
                claims = verify_hs256(authorization[len("Bearer "):])
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
