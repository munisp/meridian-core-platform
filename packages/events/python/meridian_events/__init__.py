"""Meridian core event helpers (Python) — mirrors packages/events Go module."""
from .envelope import Envelope, new_envelope, new_ulid, new_trace_id, dlq_topic
from .bus import Bus, InprocBus, bus_from_env, Handler
from .outbox import OutboxStore, FileOutbox, OutboxRelay
from .auth import Claims, sign_hs256, verify_hs256, AuthError
from .store import JsonStore, AppendLog
from .outbox import DuckDBOutbox
from .shim import is_canonical_envelope, upgrade_legacy, coerce_envelope

__all__ = [
    "Envelope", "new_envelope", "new_ulid", "new_trace_id", "dlq_topic",
    "Bus", "InprocBus", "bus_from_env", "Handler",
    "OutboxStore", "FileOutbox", "OutboxRelay", "DuckDBOutbox",
    "Claims", "sign_hs256", "verify_hs256", "AuthError",
    "JsonStore", "AppendLog",
    "is_canonical_envelope", "upgrade_legacy", "coerce_envelope",
]
