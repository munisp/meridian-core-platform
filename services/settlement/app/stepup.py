"""R4-9c: TOTP (RFC 6238) step-up for settlement money paths.

Mirrors packages/stepup (Go). Cross-service consistency contract:
  - secret: 20 random bytes, base32 (RFC 4648, no padding) — same format
    as the Go service; a secret enrolled on either side verifies on both
  - TOTP: HMAC-SHA1, 30s period, 6 digits, acceptance window ±1 step
  - recovery codes: "XXXX-XXXX" base32, stored as SHA-256 hashes,
    single-use
  - ticket: minted by admin-api (/v1/stepup/challenge); this service
    verifies X-Stepup-Code directly and does not consume tickets (the
    ledger is the ticket consumer; documented in the R4 PR).

At-rest protection: the TOTP secret is sealed in the settlement store with
an HMAC-SHA256 keystream envelope keyed from STEPUP_SEAL_KEY (the Go side
uses AES-256-GCM; both are passphrase-keyed envelopes — see the PR
residual: KMS envelope encryption via packages/keyx is the follow-up).
PROFILE=prod without STEPUP_SEAL_KEY refuses enrollment (fail-closed).

Policy (same as Go): an enrolled actor must present X-Stepup-Code (TOTP or
recovery) on gated money routes in every profile; an unenrolled actor is
denied with 403 step_up_required under PROFILE=prod and bypassed with a
loud log in dev.
"""
from __future__ import annotations

import base64
import hashlib
import hmac
import logging
import os
import secrets as _secrets
import struct
import time

from fastapi import HTTPException, Request

log = logging.getLogger("settlement.stepup")

PERIOD = 30
DIGITS = 6
WINDOW = 1
RECOVERY_COUNT = 10
_STORE_COLLECTION = "stepup_enrollments"

_DEV_SEAL_KEY = "meridian-stepup-dev-seal-change-me"


# --- TOTP core (RFC 6238) ---

def _decode_secret(secret: str) -> bytes:
    s = secret.strip().replace(" ", "").upper().rstrip("=")
    pad = "=" * (-len(s) % 8)
    raw = base64.b32decode(s + pad)
    if len(raw) < 10:
        raise ValueError("secret too short")
    return raw


def totp_code(secret: str, at: float | None = None) -> str:
    """6-digit TOTP for secret at time `at` (default now)."""
    raw = _decode_secret(secret)
    step = int((time.time() if at is None else at) // PERIOD)
    digest = hmac.new(raw, struct.pack(">Q", step), hashlib.sha1).digest()
    off = digest[-1] & 0x0F
    bin_code = struct.unpack(">I", digest[off:off + 4])[0] & 0x7FFFFFFF
    return str(bin_code % (10 ** DIGITS)).zfill(DIGITS)


def totp_validate(secret: str, code: str, at: float | None = None) -> bool:
    """Constant-time-ish check across the ±1 step window."""
    code = (code or "").strip()
    if len(code) != DIGITS or not code.isdigit():
        return False
    now = time.time() if at is None else at
    ok = False
    for w in range(-WINDOW, WINDOW + 1):
        if hmac.compare_digest(totp_code(secret, now + w * PERIOD), code):
            ok = True
    return ok


def generate_secret() -> str:
    return base64.b32encode(_secrets.token_bytes(20)).decode().rstrip("=")


def generate_recovery_codes() -> tuple[list[str], list[str]]:
    codes, hashes = [], []
    for _ in range(RECOVERY_COUNT):
        enc = base64.b32encode(_secrets.token_bytes(5)).decode().rstrip("=")
        code = f"{enc[:4]}-{enc[4:8]}"
        codes.append(code)
        hashes.append(hash_recovery_code(code))
    return codes, hashes


def hash_recovery_code(code: str) -> str:
    return hashlib.sha256((code or "").strip().upper().encode()).hexdigest()


# --- seal envelope (HMAC-SHA256 keystream + integrity tag) ---

def _seal_key() -> str:
    key = os.environ.get("STEPUP_SEAL_KEY", "")
    if not key:
        if os.environ.get("PROFILE", "dev") == "prod":
            raise RuntimeError("stepup: PROFILE=prod requires STEPUP_SEAL_KEY (fail-closed)")
        log.warning("STEPUP_SEAL_KEY unset; using well-known dev seal key (dev only)")
        key = _DEV_SEAL_KEY
    return key


def _seal(plaintext: str) -> str:
    key = hashlib.sha256(_seal_key().encode()).digest()
    nonce = _secrets.token_bytes(16)
    stream = b""
    counter = 0
    while len(stream) < len(plaintext.encode()):
        stream += hmac.new(key, nonce + struct.pack(">I", counter), hashlib.sha256).digest()
        counter += 1
    ct = bytes(a ^ b for a, b in zip(plaintext.encode(), stream))
    tag = hmac.new(key, b"seal" + nonce + ct, hashlib.sha256).digest()
    return base64.b64encode(nonce + tag + ct).decode()


def _open(sealed: str) -> str:
    key = hashlib.sha256(_seal_key().encode()).digest()
    raw = base64.b64decode(sealed)
    nonce, tag, ct = raw[:16], raw[16:48], raw[48:]
    want = hmac.new(key, b"seal" + nonce + ct, hashlib.sha256).digest()
    if not hmac.compare_digest(tag, want):
        raise ValueError("sealed secret integrity check failed")
    stream = b""
    counter = 0
    while len(stream) < len(ct):
        stream += hmac.new(key, nonce + struct.pack(">I", counter), hashlib.sha256).digest()
        counter += 1
    return bytes(a ^ b for a, b in zip(ct, stream)).decode()


# --- enrollment lifecycle (backed by the settlement store) ---

def enroll(store, actor: str) -> dict:
    secret = generate_secret()
    codes, hashes = generate_recovery_codes()
    store.put(_STORE_COLLECTION, actor, {
        "actor": actor,
        "sealed_secret": _seal(secret),
        "recovery_hashes": hashes,
        "enabled": False,
        "created_at": time.time(),
    })
    return {"actor": actor, "secret": secret,
            "otpauth_uri": f"otpauth://totp/MeridianSettlement:{actor}"
                           f"?secret={secret}&issuer=MeridianSettlement"
                           f"&algorithm=SHA1&digits={DIGITS}&period={PERIOD}",
            "recovery_codes": codes, "status": "pending_confirm"}


def confirm(store, actor: str, code: str) -> bool:
    rec = store.get(_STORE_COLLECTION, actor)
    if rec is None:
        raise KeyError(actor)
    if not totp_validate(_open(rec["sealed_secret"]), code):
        return False
    rec["enabled"] = True
    rec["confirmed_at"] = time.time()
    store.put(_STORE_COLLECTION, actor, rec)
    return True


def enrolled(store, actor: str) -> bool:
    rec = store.get(_STORE_COLLECTION, actor)
    return bool(rec and rec.get("enabled"))


def verify_code(store, actor: str, code: str) -> bool:
    """TOTP or single-use recovery code (burned on use)."""
    rec = store.get(_STORE_COLLECTION, actor)
    if not rec or not rec.get("enabled"):
        return False
    if totp_validate(_open(rec["sealed_secret"]), code):
        return True
    h = hash_recovery_code(code)
    hashes = list(rec.get("recovery_hashes") or [])
    if h in hashes:
        hashes.remove(h)
        rec["recovery_hashes"] = hashes
        store.put(_STORE_COLLECTION, actor, rec)
        return True
    return False


# --- middleware dependency ---

def require_stepup(request: Request, claims, store) -> None:
    """Gate a money route. Raises 403 step_up_required / 401 invalid."""
    code = request.headers.get("X-Stepup-Code", "")
    if code:
        if verify_code(store, claims.sub, code):
            return
        raise HTTPException(401, "step_up_invalid: invalid or expired TOTP/recovery code")
    if enrolled(store, claims.sub):
        raise HTTPException(403, "step_up_required: this money route requires X-Stepup-Code (TOTP)")
    if os.environ.get("PROFILE", "dev") == "prod":
        raise HTTPException(403, "step_up_required: admin has no enrolled TOTP "
                                 "(fail-closed in prod); enroll at /v1/stepup/enroll")
    log.warning("step-up BYPASSED (dev profile, unenrolled actor) actor=%s path=%s",
                claims.sub, request.url.path)
