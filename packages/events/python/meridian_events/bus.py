"""Event bus interface + inproc dev bus (SPEC 1.1: EVENT_BUS=inproc default)."""
from __future__ import annotations

import logging
import os
import threading
from typing import Callable, Protocol

from .envelope import Envelope, dlq_topic

log = logging.getLogger("meridian.bus")

Handler = Callable[[Envelope], None]


class Bus(Protocol):
    def publish(self, topic: str, env: Envelope) -> None: ...
    def subscribe(self, topic: str, handler: Handler) -> Callable[[], None]: ...
    def close(self) -> None: ...


class InprocBus:
    """Thread-safe in-process bus. Handler exceptions route to topic+'.dlq'."""

    def __init__(self) -> None:
        self._lock = threading.RLock()
        self._subs: dict[str, list[Handler]] = {}
        self._dlq: dict[str, list[Envelope]] = {}
        self._closed = False

    def publish(self, topic: str, env: Envelope) -> None:
        with self._lock:
            if self._closed:
                raise RuntimeError("bus closed")
            handlers = list(self._subs.get(topic, []))
        for h in handlers:
            try:
                h(env)
            except Exception as exc:  # noqa: BLE001 - DLQ routing per SPEC 1.2
                log.warning("handler error on %s (%s); routing to DLQ", topic, exc)
                with self._lock:
                    self._dlq.setdefault(dlq_topic(topic), []).append(env)

    def subscribe(self, topic: str, handler: Handler) -> Callable[[], None]:
        with self._lock:
            if self._closed:
                raise RuntimeError("bus closed")
            self._subs.setdefault(topic, []).append(handler)

        def cancel() -> None:
            with self._lock:
                if handler in self._subs.get(topic, []):
                    self._subs[topic].remove(handler)

        return cancel

    def dlq(self, topic: str) -> list[Envelope]:
        with self._lock:
            return list(self._dlq.get(dlq_topic(topic), []))

    def close(self) -> None:
        with self._lock:
            self._closed = True


def bus_from_env() -> Bus:
    """EVENT_BUS=inproc (default) -> InprocBus. EVENT_BUS=kafka uses Redpanda
    pandaproxy REST via KAFKA_PROXY_URL (default http://localhost:8082)."""
    mode = os.environ.get("EVENT_BUS", "inproc")
    if mode == "kafka":
        from .pandaproxy import PandaproxyBus

        proxy = os.environ.get("KAFKA_PROXY_URL", "http://localhost:8082")
        log.info("EVENT_BUS=kafka via pandaproxy %s", proxy)
        return PandaproxyBus(proxy)
    if mode not in ("", "inproc"):
        log.warning("unknown EVENT_BUS=%s; falling back to inproc", mode)
    return InprocBus()
