"""Consumer-side envelope shim for legacy non-enveloped publishers
(audit I3 / N6). Some producers (notably inclusion ussd-gateway, which
publishes raw maps to nrs.onb.ussd.v1) bypass the canonical envelope.
Until those producers are fixed upstream, consumers MUST upgrade legacy
raw maps at the edge so downstream (lakehouse sink, ML) only ever sees
canonical envelopes.

Upgraded events are tagged: data["upgraded_legacy"] = True and
source carries the "(legacy-shim)" suffix, so conformance gaps stay
measurable in bronze.
"""
from __future__ import annotations

import hashlib
import time
from datetime import datetime, timezone
from typing import Any

from .envelope import Envelope, new_trace_id, new_ulid

_ENVELOPE_KEYS = {"id", "type", "source", "time", "data"}
_SENSITIVE = ("msisdn", "nin", "phone")


def is_canonical_envelope(msg: Any) -> bool:
    """True when msg already looks like a canonical envelope."""
    if not isinstance(msg, dict):
        return False
    if not _ENVELOPE_KEYS.issubset(msg):
        return False
    return isinstance(msg.get("id"), str) and len(msg["id"]) == 26 \
        and isinstance(msg.get("type"), str) and msg["type"].startswith("nrs.")


def upgrade_legacy(topic: str, raw: dict, *, source_hint: str = "unknown") -> Envelope:
    """Wrap a legacy raw map into the canonical envelope.

    - id: deterministic ULID-shaped tag derived from content when the raw
      map carries an idempotency handle (client_ref/session_id) so redelivers
      dedup in bronze; otherwise a fresh ULID.
    - sensitive scalar identifiers are pseudonymised (sha256) in the copy.
    """
    data = dict(raw)
    for key in list(data):
        if key.lower() in _SENSITIVE and data[key] is not None:
            data[f"{key.lower()}_hash"] = hashlib.sha256(str(data[key]).encode()).hexdigest()
            del data[key]
    data["upgraded_legacy"] = True

    seed = raw.get("client_ref") or raw.get("session_id")
    if seed:
        digest = hashlib.sha256(f"{topic}:{seed}".encode()).digest()
        env_id = _ulid_from_bytes(digest[:16])
    else:
        env_id = new_ulid()

    return Envelope(
        id=env_id,
        type=topic,
        source=f"{source_hint}(legacy-shim)",
        time=raw.get("time") or datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
        tenant_id=str(raw.get("tenant_id", "")),
        trace_id=raw.get("trace_id") or new_trace_id(),
        rule_pack_version=str(raw.get("rule_pack_version", "")),
        data=data,
    )


def coerce_envelope(topic: str, msg: Any, *, source_hint: str = "unknown") -> Envelope:
    """Return msg as a canonical Envelope, upgrading legacy raw maps."""
    if is_canonical_envelope(msg):
        return Envelope.from_dict(msg)
    if isinstance(msg, dict):
        return upgrade_legacy(topic, msg, source_hint=source_hint)
    raise TypeError(f"cannot coerce {type(msg).__name__} to envelope for {topic}")


def _ulid_from_bytes(b: bytes) -> str:
    """Encode 16 bytes as a 26-char Crockford base32 ULID-shaped string with
    the current timestamp in the high 48 bits (keeps ULID sortability)."""
    alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
    ms = int(time.time() * 1000) & ((1 << 48) - 1)
    body = bytearray(b)
    body[0] = (ms >> 40) & 0xFF
    body[1] = (ms >> 32) & 0xFF
    body[2] = (ms >> 24) & 0xFF
    body[3] = (ms >> 16) & 0xFF
    body[4] = (ms >> 8) & 0xFF
    body[5] = ms & 0xFF
    value = int.from_bytes(body, "big")
    chars = []
    for i in range(26):
        shift = 125 - i * 5
        group = (value >> (shift - 2)) & 0x1F if shift >= 2 else ((value << (2 - shift)) & 0x1F)
        chars.append(alphabet[group])
    return "".join(chars)
