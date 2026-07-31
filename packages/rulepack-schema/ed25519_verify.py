"""Pure-Python Ed25519 (RFC 8032) sign/verify for rule-pack ceremony
signatures. Stdlib-only (no PyNaCl/cryptography dependency), constant-time
NOT guaranteed — acceptable for offline ceremony + load-time verification,
not for interactive secret-key operations in a hostile multi-tenant process.

Test vectors: RFC 8032 section 7.1 (see test_ed25519.py).
"""
from __future__ import annotations

import hashlib

_q = 2**255 - 19
_l = 2**252 + 27742317777372353535851937790883648493
_d = -121665 * pow(121666, _q - 2, _q) % _q
_I = pow(2, (_q - 1) // 4, _q)
_B = (
    15112221349535400772501151409588531511454012693041857206046113283949847762202,
    46316835694926478169428394003475163141307993866256225615783033603165251855960,
)


def _xrecover(y: int) -> int:
    xx = (y * y - 1) * pow(_d * y * y + 1, _q - 2, _q)
    x = pow(xx, (_q + 3) // 8, _q)
    if (x * x - xx) % _q != 0:
        x = (x * _I) % _q
    if x % 2 != 0:
        x = _q - x
    return x


def _edwards_add(P, Q):
    (x1, y1), (x2, y2) = P, Q
    denom = pow(1 + _d * x1 * x2 * y1 * y2, _q - 2, _q)
    x3 = (x1 * y2 + x2 * y1) * denom % _q
    denom2 = pow(1 - _d * x1 * x2 * y1 * y2, _q - 2, _q)
    y3 = (y1 * y2 + x1 * x2) * denom2 % _q
    return (x3, y3)


def _scalarmult(P, e: int):
    Q = (0, 1)
    while e > 0:
        if e & 1:
            Q = _edwards_add(Q, P)
        P = _edwards_add(P, P)
        e >>= 1
    return Q


def _encode_point(P) -> bytes:
    x, y = P
    bits = y | ((x & 1) << 255)
    return bits.to_bytes(32, "little")


def _decode_point(s: bytes):
    if len(s) != 32:
        raise ValueError("point must be 32 bytes")
    y = int.from_bytes(s, "little") & ((1 << 255) - 1)
    sign = s[31] >> 7
    if y >= _q:
        raise ValueError("invalid point encoding")
    x = _xrecover(y)
    if (x & 1) != sign:
        x = _q - x
    P = (x, y)
    if not _isoncurve(P):
        raise ValueError("point not on curve")
    return P


def _isoncurve(P) -> bool:
    x, y = P
    return (-x * x + y * y - 1 - _d * x * x * y * y) % _q == 0


def _hint(m: bytes) -> int:
    return int.from_bytes(hashlib.sha512(m).digest(), "little")


def _publickey(seed: bytes) -> bytes:
    if len(seed) != 32:
        raise ValueError("seed must be 32 bytes")
    h = hashlib.sha512(seed).digest()
    a = int.from_bytes(h[:32], "little")
    a &= (1 << 254) - 8
    a |= 1 << 254
    return _encode_point(_scalarmult(_B, a))


def _expand(seed: bytes) -> tuple[int, bytes]:
    h = hashlib.sha512(seed).digest()
    a = int.from_bytes(h[:32], "little")
    a &= (1 << 254) - 8
    a |= 1 << 254
    return a, h[32:]


def sign(seed: bytes, msg: bytes) -> bytes:
    """Detached Ed25519 signature (64 bytes) over msg with a 32-byte seed."""
    a, prefix = _expand(seed)
    A = _publickey(seed)
    r = _hint(prefix + msg) % _l
    R = _encode_point(_scalarmult(_B, r))
    k = _hint(R + A + msg) % _l
    S = (r + k * a) % _l
    return R + S.to_bytes(32, "little")


def verify(sig: bytes, msg: bytes, pubkey: bytes) -> bool:
    """Verify a detached Ed25519 signature. Constant-time-ish via int compare."""
    if len(sig) != 64 or len(pubkey) != 32:
        return False
    try:
        A = _decode_point(pubkey)
        R = _decode_point(sig[:32])
    except ValueError:
        return False
    S = int.from_bytes(sig[32:], "little")
    if S >= _l:
        return False
    k = _hint(sig[:32] + pubkey + msg) % _l
    # Check [S]B == R + [k]A
    return _encode_point(_scalarmult(_B, S)) == _encode_point(
        _edwards_add(R, _scalarmult(A, k))
    )
