"""feature-store — offline/online feature service (SPEC 2).

Offline: DuckDB columnar store (parquet-capable). Online: low-latency KV.
Materialisation computes aggregations over source records into both stores.

Monetary features (kobo): features whose value_field ends in ``_kobo`` (or
with ``kobo: true``) are stored as BIGINT (int64) in the ``value_kobo``
column — never DOUBLE — so sums over large ledgers stay exact (float64 loses
integer precision beyond 2^53). PARQUET SCHEMA CHANGE: source parquet files
for kobo features must use INT64/BIGINT physical types; legacy DOUBLE kobo
columns are rejected on materialisation (re-export with int64).

TODO(point-in-time joins): training-set assembly must join features AS OF
the label timestamp (feature ts <= label ts), not latest-wins, or offline
metrics are optimistic (training/serving skew). The DuckDB history table
already retains per-ts rows to support this; the join builder is pending.
See ml/training/train.py for the corresponding guard note.
"""
from __future__ import annotations

import os
import threading
import time
from pathlib import Path

import duckdb
from fastapi import Depends, FastAPI, HTTPException
from pydantic import BaseModel, Field

from meridian_events.auth import Claims, fastapi_dependency
from meridian_events.problem import install_problem_handlers
from meridian_events.store import open_store

SERVICE = "feature-store"
VERSION = "0.1.0"

app = FastAPI(title="Meridian feature-store", version=VERSION)
install_problem_handlers(app)

DATA_DIR = Path(os.environ.get("DATA_DIR", "./data"))
DATA_DIR.mkdir(parents=True, exist_ok=True)

_lock = threading.RLock()
_db = duckdb.connect(str(DATA_DIR / "features.duckdb"))
with _lock:
    _db.execute("""
        CREATE TABLE IF NOT EXISTS feature_values (
            entity TEXT NOT NULL, name TEXT NOT NULL,
            value DOUBLE, value_kobo BIGINT, value_text TEXT, ts DOUBLE NOT NULL,
            PRIMARY KEY (entity, name, ts))
    """)
    # migrations for pre-existing databases
    try:
        _db.execute("ALTER TABLE feature_values ADD COLUMN value_kobo BIGINT")
    except Exception:  # noqa: BLE001 - column already present
        pass
_online = open_store(DATA_DIR / "online")

AGGS = {"last", "sum", "count", "avg", "min", "max"}


class FeatureDef(BaseModel):
    name: str = Field(..., pattern=r"^fv_[a-z0-9_]+$|^[a-z][a-z0-9_]*$")
    entity_key: str  # field in records identifying the entity (pseudonymised)
    value_field: str | None = None  # numeric field to aggregate (not needed for count/last)
    agg: str = "last"
    window_days: int | None = None  # trailing window filter on ts_field
    ts_field: str = "ts"
    kobo: bool = False  # monetary feature: stored as int64, never float64

    @property
    def is_kobo(self) -> bool:
        return self.kobo or bool(self.value_field and self.value_field.endswith("_kobo"))


class MaterialiseRequest(BaseModel):
    feature: FeatureDef
    source_records: list[dict] = Field(default_factory=list)
    source_parquet: str | None = None  # optional parquet file path (offline batch)


def _records_from_parquet(path: str) -> list[dict]:
    p = Path(path)
    if not p.exists():
        raise HTTPException(400, f"parquet source not found: {path}")
    with _lock:
        return _db.execute(f"SELECT * FROM read_parquet(?)", [str(p)]).fetchdf().to_dict("records")


def _materialise(fd: FeatureDef, records: list[dict]) -> dict:
    if fd.agg not in AGGS:
        raise HTTPException(422, f"agg must be one of {sorted(AGGS)}")
    now = time.time()
    cutoff = now - fd.window_days * 86400 if fd.window_days else None
    groups: dict[str, list[dict]] = {}
    for rec in records:
        ent = rec.get(fd.entity_key)
        if ent is None:
            continue
        ts = float(rec.get(fd.ts_field, now) or now)
        if cutoff and ts < cutoff:
            continue
        groups.setdefault(str(ent), []).append((ts, rec))
    is_kobo = fd.is_kobo
    written = 0
    for ent, rows in groups.items():
        value: float | int
        if fd.agg == "count":
            value = len(rows) if is_kobo else float(len(rows))
        else:
            vals = []
            for ts, rec in rows:
                v = rec.get(fd.value_field) if fd.value_field else None
                if v is None:
                    continue
                if is_kobo:
                    # int64 kobo: reject floats (precision loss) and bools
                    if isinstance(v, bool) or not isinstance(v, int):
                        raise HTTPException(
                            422, f"kobo feature {fd.name}: value for entity {ent} is "
                                 f"{type(v).__name__}, must be int (int64 kobo)")
                    vals.append((ts, v))
                elif isinstance(v, (int, float)):
                    vals.append((ts, float(v)))
            if not vals:
                continue
            nums = [v for _, v in vals]
            if fd.agg == "sum":
                value = sum(nums)
            elif fd.agg == "avg":
                # kobo avg: integer floor division keeps exact int64 semantics
                value = sum(nums) // len(nums) if is_kobo else sum(nums) / len(nums)
            elif fd.agg == "min":
                value = min(nums)
            elif fd.agg == "max":
                value = max(nums)
            else:  # last: latest by ts
                value = max(vals, key=lambda x: x[0])[1]
        with _lock:
            if is_kobo:
                _db.execute(
                    "INSERT INTO feature_values (entity, name, value_kobo, ts) VALUES (?,?,?,?)",
                    [ent, fd.name, int(value), now])
            else:
                _db.execute(
                    "INSERT INTO feature_values (entity, name, value, ts) VALUES (?,?,?,?)",
                    [ent, fd.name, float(value), now])
            col = "value_kobo" if is_kobo else "value"
            latest = _db.execute(
                f"SELECT {col}, ts FROM feature_values WHERE entity=? AND name=? "
                "ORDER BY ts DESC LIMIT 1", [ent, fd.name]).fetchone()
        if latest:
            payload = {"entity": ent, "name": fd.name, "ts": latest[1],
                       ("value_kobo" if is_kobo else "value"): latest[0]}
            _online.put("online", f"{ent}:{fd.name}", payload)
        written += 1
    return {"feature": fd.name, "entities_written": written, "agg": fd.agg,
            "kobo": is_kobo}


@app.get("/healthz")
def healthz() -> dict:
    return {"status": "ok", "service": SERVICE, "version": VERSION}


@app.get("/readyz")
def readyz() -> dict:
    try:
        with _lock:
            _db.execute("SELECT 1")
    except Exception as exc:  # noqa: BLE001
        raise HTTPException(503, str(exc)) from exc
    return {"status": "ok", "service": SERVICE, "version": VERSION}


@app.post("/v1/features/materialise")
def materialise(req: MaterialiseRequest, claims: Claims = Depends(fastapi_dependency())) -> dict:
    records = list(req.source_records)
    if req.source_parquet:
        records.extend(_records_from_parquet(req.source_parquet))
    if not records:
        raise HTTPException(400, "source_records or source_parquet required")
    return _materialise(req.feature, records)


@app.get("/v1/features/online/{entity}/{name}")
def online_get(entity: str, name: str, claims: Claims = Depends(fastapi_dependency())) -> dict:
    v = _online.get("online", f"{entity}:{name}")
    if v is None:
        raise HTTPException(404, f"no online value for {entity}/{name}")
    return v


class BatchRequest(BaseModel):
    entities: list[str]
    features: list[str]


@app.post("/v1/features/batch")
def batch(req: BatchRequest, claims: Claims = Depends(fastapi_dependency())) -> dict:
    out: dict[str, dict[str, float | None]] = {}
    with _lock:
        for ent in req.entities:
            row: dict[str, float | None] = {}
            for feat in req.features:
                r = _db.execute(
                    "SELECT COALESCE(value, value_kobo) FROM feature_values "
                    "WHERE entity=? AND name=? "
                    "ORDER BY ts DESC LIMIT 1", [ent, feat]).fetchone()
                row[feat] = r[0] if r else None
            out[ent] = row
    return {"features": out}


def main() -> None:  # pragma: no cover
    import uvicorn

    uvicorn.run(app, host="0.0.0.0", port=int(os.environ.get("PORT", "8012")))


if __name__ == "__main__":  # pragma: no cover
    main()
