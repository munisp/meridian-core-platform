"""Redpanda Pandaproxy REST bus (EVENT_BUS=kafka) — stdlib/httpx only."""
from __future__ import annotations

import base64
import json
import threading
import time
import urllib.request
from typing import Callable

from .envelope import Envelope, dlq_topic
from .bus import Handler


class PandaproxyBus:
    def __init__(self, base_url: str) -> None:
        self.base = base_url.rstrip("/")
        self._closed = False
        self._stops: list[threading.Event] = []

    def _req(self, method: str, path: str, body: dict | None = None,
             content_type: str = "application/vnd.kafka.v2+json",
             accept: str | None = None) -> tuple[int, bytes]:
        data = json.dumps(body).encode() if body is not None else None
        r = urllib.request.Request(self.base + path, data=data, method=method)
        if body is not None:
            r.add_header("Content-Type", content_type)
        if accept:
            r.add_header("Accept", accept)
        with urllib.request.urlopen(r, timeout=30) as resp:  # noqa: S310 (internal cluster URL)
            return resp.status, resp.read()

    def publish(self, topic: str, env: Envelope) -> None:
        raw = base64.b64encode(json.dumps(env.to_dict()).encode()).decode()
        self._req("POST", f"/topics/{topic}", {"records": [{"value": raw}]},
                  content_type="application/vnd.kafka.binary.v2+json")

    def subscribe(self, topic: str, handler: Handler) -> Callable[[], None]:
        name = f"meridian-{int(time.time() * 1000)}"
        _, body = self._req("POST", "/consumers/meridian", {
            "name": name, "format": "binary",
            "auto.offset.reset": "earliest", "auto.commit.enable": "true",
        })
        inst = json.loads(body)
        base_uri = inst["base_uri"]
        self._req("POST", f"{base_uri[len(self.base):]}/subscription", {"topics": [topic]})

        stop = threading.Event()
        self._stops.append(stop)

        def loop() -> None:
            while not stop.is_set():
                try:
                    _, rbody = self._req("GET", f"{base_uri[len(self.base):]}/records",
                                         accept="application/vnd.kafka.binary.v2+json")
                    recs = json.loads(rbody)
                except Exception:  # noqa: BLE001
                    stop.wait(2.0)
                    continue
                if not recs:
                    stop.wait(0.5)
                    continue
                for rec in recs:
                    try:
                        env = Envelope.from_dict(json.loads(base64.b64decode(rec["value"])))
                        handler(env)
                    except Exception:  # noqa: BLE001 - DLQ per SPEC 1.2
                        try:
                            self.publish(dlq_topic(rec.get("topic", topic)), env)  # type: ignore[possibly-undefined]
                        except Exception:
                            pass

        t = threading.Thread(target=loop, daemon=True)
        t.start()

        def cancel() -> None:
            stop.set()

        return cancel

    def close(self) -> None:
        self._closed = True
        for s in self._stops:
            s.set()
