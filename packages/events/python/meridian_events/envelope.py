"""Event envelope per SPEC 1.1."""
from __future__ import annotations

import os
import time
from dataclasses import dataclass, field, asdict
from datetime import datetime, timezone
from typing import Any

_ALPHABET = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"


def new_ulid() -> str:
    """Canonical 26-char Crockford-base32 ULID (48-bit ms time + 80-bit random)."""
    ms = int(time.time() * 1000) & ((1 << 48) - 1)
    rand = int.from_bytes(os.urandom(10), "big")
    value = (ms << 80) | rand  # 128-bit
    # 130-bit stream: 2 leading zero bits + 128-bit value
    chars = []
    for i in range(26):
        shift = 125 - i * 5  # bits remaining below this group in 130-bit space
        # group i covers stream bits [5i, 5i+4]; stream = 0b00 ++ value
        group = (value >> (shift - 2)) & 0x1F if shift >= 2 else ((value << (2 - shift)) & 0x1F)
        chars.append(_ALPHABET[group])
    return "".join(chars)


def new_trace_id() -> str:
    return os.urandom(16).hex()


def dlq_topic(topic: str) -> str:
    return topic + ".dlq"


def _rfc3339_now() -> str:
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


@dataclass
class Envelope:
    type: str
    source: str
    data: Any
    id: str = field(default_factory=new_ulid)
    time: str = field(default_factory=_rfc3339_now)
    tenant_id: str = ""
    trace_id: str = field(default_factory=new_trace_id)
    rule_pack_version: str = ""

    def to_dict(self) -> dict:
        return asdict(self)

    @classmethod
    def from_dict(cls, d: dict) -> "Envelope":
        return cls(
            id=d["id"], type=d["type"], source=d["source"], time=d["time"],
            tenant_id=d.get("tenant_id", ""), trace_id=d.get("trace_id", ""),
            rule_pack_version=d.get("rule_pack_version", ""), data=d.get("data"),
        )


def new_envelope(type_: str, source: str, data: Any,
                 tenant_id: str = "", rule_pack_version: str = "") -> Envelope:
    return Envelope(type=type_, source=source, data=data,
                    tenant_id=tenant_id, rule_pack_version=rule_pack_version)
