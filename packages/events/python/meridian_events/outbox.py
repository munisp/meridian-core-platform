"""Transactional outbox (SPEC 1.1): durable JSONL outbox + relay to bus."""
from __future__ import annotations

import json
import logging
import os
import threading
from pathlib import Path
from typing import Protocol

from .bus import Bus
from .envelope import Envelope

log = logging.getLogger("meridian.outbox")


class OutboxStore(Protocol):
    def append(self, topic: str, env: Envelope) -> None: ...
    def pending(self, after_seq: int, limit: int = 200) -> list[dict]: ...


class FileOutbox:
    """Durable JSONL outbox (dev default). Postgres-backed services implement
    OutboxStore with a real same-tx outbox table."""

    def __init__(self, dir_: str | os.PathLike) -> None:
        self.dir = Path(dir_)
        self.dir.mkdir(parents=True, exist_ok=True)
        self.path = self.dir / "outbox.jsonl"
        self._lock = threading.Lock()
        self._seq = 0
        if self.path.exists():
            with self.path.open() as f:
                for line in f:
                    line = line.strip()
                    if line:
                        try:
                            self._seq = max(self._seq, json.loads(line)["seq"])
                        except (json.JSONDecodeError, KeyError):
                            continue

    def append(self, topic: str, env: Envelope) -> None:
        with self._lock:
            self._seq += 1
            rec = {"seq": self._seq, "topic": topic, "envelope": env.to_dict()}
            with self.path.open("a") as f:
                f.write(json.dumps(rec) + "\n")
                f.flush()
                os.fsync(f.fileno())

    def pending(self, after_seq: int, limit: int = 200) -> list[dict]:
        out: list[dict] = []
        if not self.path.exists():
            return out
        with self._lock, self.path.open() as f:
            for line in f:
                line = line.strip()
                if not line:
                    continue
                try:
                    rec = json.loads(line)
                except json.JSONDecodeError:
                    continue
                if rec.get("seq", 0) > after_seq:
                    out.append(rec)
                    if len(out) >= limit:
                        break
        return out


class OutboxRelay:
    """Drains an OutboxStore onto a Bus with a seq checkpoint."""

    def __init__(self, store: OutboxStore, bus: Bus, checkpoint_dir: str | os.PathLike,
                 interval: float = 0.5) -> None:
        self.store = store
        self.bus = bus
        self.dir = Path(checkpoint_dir)
        self.dir.mkdir(parents=True, exist_ok=True)
        self.interval = interval
        self._stop = threading.Event()
        self._thread: threading.Thread | None = None

    def _checkpoint_path(self) -> Path:
        return self.dir / "outbox.relay.seq"

    def _load_checkpoint(self) -> int:
        try:
            return int(self._checkpoint_path().read_text().strip())
        except (OSError, ValueError):
            return 0

    def _save_checkpoint(self, seq: int) -> None:
        tmp = self._checkpoint_path().with_suffix(".tmp")
        tmp.write_text(str(seq))
        tmp.replace(self._checkpoint_path())

    def flush_once(self) -> int:
        seq = self._load_checkpoint()
        n = 0
        for rec in self.store.pending(seq):
            try:
                self.bus.publish(rec["topic"], Envelope.from_dict(rec["envelope"]))
            except Exception as exc:  # noqa: BLE001
                log.warning("relay publish seq=%s failed: %s", rec["seq"], exc)
                break
            seq = rec["seq"]
            n += 1
        if n:
            self._save_checkpoint(seq)
        return n

    def start(self) -> None:
        def loop() -> None:
            while not self._stop.is_set():
                self.flush_once()
                self._stop.wait(self.interval)

        self._thread = threading.Thread(target=loop, daemon=True)
        self._thread.start()

    def stop(self) -> None:
        self._stop.set()
        if self._thread:
            self._thread.join(timeout=5)
        self.flush_once()
