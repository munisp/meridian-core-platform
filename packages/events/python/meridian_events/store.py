"""Embedded durable stores (SPEC 1.3 fallback when DATABASE_URL unset).

JsonStore: collection-oriented JSON document store (one file per collection,
atomic writes). AppendLog: append-only JSONL with sequence numbers.
A sqlite3-backed implementation is provided for services that want SQL.
"""
from __future__ import annotations

import json
import os
import sqlite3
import threading
from pathlib import Path
from typing import Any, Callable, Iterator


class JsonStore:
    def __init__(self, dir_: str | os.PathLike | None = None) -> None:
        self.dir = Path(dir_) if dir_ else None
        self._lock = threading.RLock()
        self._cols: dict[str, dict[str, Any]] = {}
        if self.dir:
            self.dir.mkdir(parents=True, exist_ok=True)
            for f in self.dir.glob("*.json"):
                try:
                    self._cols[f.stem] = json.loads(f.read_text() or "{}")
                except json.JSONDecodeError as exc:
                    raise ValueError(f"corrupt collection {f}") from exc

    def _persist(self, coll: str) -> None:
        if not self.dir:
            return
        tmp = self.dir / f"{coll}.json.tmp"
        tmp.write_text(json.dumps(self._cols.get(coll, {}), indent=2))
        tmp.replace(self.dir / f"{coll}.json")

    def put(self, coll: str, id_: str, doc: Any) -> None:
        with self._lock:
            self._cols.setdefault(coll, {})[id_] = doc
            self._persist(coll)

    def get(self, coll: str, id_: str, default: Any = None) -> Any:
        with self._lock:
            return self._cols.get(coll, {}).get(id_, default)

    def delete(self, coll: str, id_: str) -> bool:
        with self._lock:
            existed = id_ in self._cols.get(coll, {})
            if existed:
                del self._cols[coll][id_]
                self._persist(coll)
            return existed

    def list(self, coll: str) -> list[Any]:
        with self._lock:
            items = self._cols.get(coll, {})
            return [items[k] for k in sorted(items)]

    def items(self, coll: str) -> list[tuple[str, Any]]:
        with self._lock:
            items = self._cols.get(coll, {})
            return [(k, items[k]) for k in sorted(items)]

    def update(self, coll: str, id_: str, fn: Callable[[Any], Any]) -> Any:
        with self._lock:
            cur = self._cols.setdefault(coll, {}).get(id_)
            nxt = fn(cur)
            self._cols[coll][id_] = nxt
            self._persist(coll)
            return nxt


class AppendLog:
    """Append-only JSONL log with monotonic seq (fsync'd)."""

    def __init__(self, dir_: str | os.PathLike, name: str) -> None:
        d = Path(dir_)
        d.mkdir(parents=True, exist_ok=True)
        self.path = d / f"{name}.jsonl"
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

    def append(self, rec: dict) -> int:
        with self._lock:
            self._seq += 1
            rec = {**rec, "seq": self._seq}
            with self.path.open("a") as f:
                f.write(json.dumps(rec) + "\n")
                f.flush()
                os.fsync(f.fileno())
            return self._seq

    def read_all(self) -> list[dict]:
        out = []
        if not self.path.exists():
            return out
        with self._lock, self.path.open() as f:
            for line in f:
                line = line.strip()
                if line:
                    try:
                        out.append(json.loads(line))
                    except json.JSONDecodeError:
                        continue
        return out


class SQLiteKV:
    """sqlite3-backed KV store — the SPEC 1.3 SQLite fallback for services
    that want SQL semantics with zero external deps."""

    def __init__(self, path: str | os.PathLike) -> None:
        self._lock = threading.RLock()
        self.db = sqlite3.connect(str(path), check_same_thread=False)
        self.db.execute("PRAGMA journal_mode=WAL")
        self.db.execute(
            "CREATE TABLE IF NOT EXISTS kv (coll TEXT NOT NULL, id TEXT NOT NULL, "
            "doc TEXT NOT NULL, PRIMARY KEY (coll, id))")
        self.db.commit()

    def put(self, coll: str, id_: str, doc: Any) -> None:
        with self._lock:
            self.db.execute(
                "INSERT INTO kv (coll, id, doc) VALUES (?,?,?) "
                "ON CONFLICT (coll, id) DO UPDATE SET doc=excluded.doc",
                (coll, id_, json.dumps(doc)))
            self.db.commit()

    def get(self, coll: str, id_: str, default: Any = None) -> Any:
        with self._lock:
            row = self.db.execute(
                "SELECT doc FROM kv WHERE coll=? AND id=?", (coll, id_)).fetchone()
        return json.loads(row[0]) if row else default

    def delete(self, coll: str, id_: str) -> bool:
        with self._lock:
            cur = self.db.execute("DELETE FROM kv WHERE coll=? AND id=?", (coll, id_))
            self.db.commit()
            return cur.rowcount > 0

    def list(self, coll: str) -> list[Any]:
        with self._lock:
            rows = self.db.execute(
                "SELECT doc FROM kv WHERE coll=? ORDER BY id", (coll,)).fetchall()
        return [json.loads(r[0]) for r in rows]

    def query(self, sql: str, params: tuple = ()) -> Iterator[sqlite3.Row]:
        with self._lock:
            yield from self.db.execute(sql, params)

    def close(self) -> None:
        self.db.close()
