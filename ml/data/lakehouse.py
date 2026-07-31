"""Unified lakehouse layer (audit I4) — ONE interface, two real backends.

Interface contract (shared with gov-enclave services/analytics/app/lakehouse.py
— both implementations honour the same method signatures and on-disk
conventions):

    write(zone, dataset, records, partition=None) -> receipt dict
    read(zone, dataset, where=None, columns=None, limit=10000) -> rows
    sql(query) -> rows
    datasets(zone=None) -> [{zone, dataset, partitions, files, rows}]
    dataset_path(zone, dataset) -> glob root

Backends selected by environment:

  ICEBERG_REST_URI set  -> IcebergLakehouse: REAL Apache Iceberg tables via
                           the REST catalog (infra compose ships iceberg-rest
                           on MinIO). pyiceberg is an optional dependency —
                           import-guarded, only required on this path.
  otherwise             -> ParquetLakehouse: dev fallback. Hive-partitioned
                           parquet (<root>/<zone>/<dataset>/dt=YYYY-MM-DD/*.parquet)
                           PLUS a local catalog.json manifest (tables, schema
                           versions, snapshots) so Trino/Spark can attach and
                           schema evolution is tracked explicitly instead of
                           being invisible (audit I4).

Zones: bronze (raw events), silver (conformed), gold (derived/pseudonymised).
Money is integer kobo (BIGINT) end to end.
"""
from __future__ import annotations

import abc
import datetime as dt
import json
import os
import threading
from typing import Any

ZONES = ("bronze", "silver", "gold")
CATALOG_FILE = "catalog.json"


def _today() -> str:
    return dt.datetime.now(dt.timezone.utc).strftime("%Y-%m-%d")


def _ulid() -> str:
    from meridian_events.envelope import new_ulid
    return new_ulid()


class Lakehouse(abc.ABC):
    """Zone/dataset/partition storage interface (the swap point)."""

    @abc.abstractmethod
    def write(self, zone: str, dataset: str, records: list[dict[str, Any]],
              partition: str | None = None) -> dict[str, Any]: ...

    @abc.abstractmethod
    def read(self, zone: str, dataset: str, where: str | None = None,
             columns: list[str] | None = None, limit: int = 10000) -> list[dict[str, Any]]: ...

    @abc.abstractmethod
    def sql(self, query: str) -> list[dict[str, Any]]: ...

    @abc.abstractmethod
    def datasets(self, zone: str | None = None) -> list[dict[str, Any]]: ...

    @abc.abstractmethod
    def dataset_path(self, zone: str, dataset: str) -> str: ...


class ParquetLakehouse(Lakehouse):
    """Dev backend: hive-partitioned parquet + catalog.json manifest.

    The manifest (<root>/catalog.json) records, per table:
      {zone, dataset, schema_version, columns, snapshots:
         [{id, ts, partition, files, rows}]}
    Every write appends one snapshot and bumps schema_version when the
    column set changes — enough for Trino/Spark attachment and for
    time-travel-by-partition in dev.
    """

    def __init__(self, root: str) -> None:
        self.root = os.path.abspath(root)
        os.makedirs(self.root, exist_ok=True)
        self._lock = threading.Lock()
        self._duck = None
        try:
            import duckdb
            self._duck = duckdb.connect(":memory:")
        except ImportError:
            pass  # read/sql then use pandas fallback
        self._catalog = self._load_catalog()

    # -- catalog manifest ---------------------------------------------------
    def _catalog_path(self) -> str:
        return os.path.join(self.root, CATALOG_FILE)

    def _load_catalog(self) -> dict:
        try:
            with open(self._catalog_path(), encoding="utf-8") as fh:
                return json.load(fh)
        except (OSError, json.JSONDecodeError):
            return {"format": "meridian-parquet-catalog/1", "tables": {}}

    def _save_catalog(self) -> None:
        tmp = self._catalog_path() + ".tmp"
        with open(tmp, "w", encoding="utf-8") as fh:
            json.dump(self._catalog, fh, indent=2, sort_keys=True)
        os.replace(tmp, self._catalog_path())

    def catalog(self) -> dict:
        with self._lock:
            return json.loads(json.dumps(self._catalog))

    def _record_snapshot(self, zone: str, dataset: str, columns: list[str],
                         snapshot: dict) -> None:
        key = f"{zone}.{dataset}"
        tables = self._catalog.setdefault("tables", {})
        tbl = tables.setdefault(key, {
            "zone": zone, "dataset": dataset, "schema_version": 0,
            "columns": columns, "snapshots": []})
        if tbl["columns"] != columns:
            tbl["schema_version"] += 1  # additive evolution bumps the version
            merged = list(dict.fromkeys([*tbl["columns"], *columns]))
            tbl["columns"] = merged
        tbl["snapshots"].append(snapshot)

    # -- layout helpers -------------------------------------------------------
    def _ds_dir(self, zone: str, dataset: str) -> str:
        if zone not in ZONES:
            raise ValueError(f"invalid zone {zone!r}; expected one of {ZONES}")
        safe = dataset.replace("/", "_").replace("..", "_")
        return os.path.join(self.root, zone, safe)

    def dataset_path(self, zone: str, dataset: str) -> str:
        return os.path.join(self._ds_dir(zone, dataset), "dt=*", "*.parquet")

    # -- Lakehouse API ---------------------------------------------------------
    def write(self, zone: str, dataset: str, records: list[dict[str, Any]],
              partition: str | None = None) -> dict[str, Any]:
        if not records:
            return {"dataset": dataset, "zone": zone, "partition": partition,
                    "rows": 0, "files": []}
        import pyarrow as pa
        import pyarrow.parquet as pq

        partition = partition or _today()
        part_dir = os.path.join(self._ds_dir(zone, dataset), f"dt={partition}")
        os.makedirs(part_dir, exist_ok=True)
        fname = f"part-{_ulid()}.parquet"
        fpath = os.path.join(part_dir, fname)

        cols: list[str] = []
        for r in records:
            for k in r:
                if k not in cols:
                    cols.append(k)

        def norm(v: Any) -> Any:
            if v is None or isinstance(v, (bool, int, float, str)):
                return v
            try:  # numpy / pandas scalars -> native python (keeps kobo int64)
                import numpy as np
                if isinstance(v, np.generic):
                    return v.item()
                import pandas as pd
                if v is pd.NaT or (isinstance(v, float) and pd.isna(v)):
                    return None
            except ImportError:
                pass
            if isinstance(v, (dt.date, dt.datetime)):
                return v.isoformat()
            return json.dumps(v, default=str)

        data = {c: [norm(r.get(c)) for r in records] for c in cols}
        table = pa.table(data)
        with self._lock:
            pq.write_table(table, fpath, compression="zstd")
            self._record_snapshot(zone, dataset, cols, {
                "id": _ulid(), "ts": dt.datetime.now(dt.timezone.utc).isoformat(),
                "partition": partition, "files": [fpath], "rows": len(records)})
            self._save_catalog()
        return {"dataset": dataset, "zone": zone, "partition": partition,
                "rows": len(records), "files": [fpath]}

    def read(self, zone: str, dataset: str, where: str | None = None,
             columns: list[str] | None = None, limit: int = 10000) -> list[dict[str, Any]]:
        glob = self.dataset_path(zone, dataset).replace("\\", "/")
        if self._duck is not None:
            if not _has_files(self._ds_dir(zone, dataset)):
                return []
            col_sql = ", ".join(f'"{c}"' for c in columns) if columns else "*"
            q = f"SELECT {col_sql} FROM read_parquet('{glob}', hive_partitioning=true, union_by_name=true)"
            if where:
                q += f" WHERE {where}"
            q += f" LIMIT {int(limit)}"
            return self.sql(q)
        import pandas as pd
        ds_dir = self._ds_dir(zone, dataset)
        if not _has_files(ds_dir):
            return []
        df = pd.read_parquet(ds_dir)
        if columns:
            df = df[[c for c in columns if c in df.columns]]
        return df.head(limit).to_dict("records")

    def sql(self, query: str) -> list[dict[str, Any]]:
        if self._duck is None:
            raise RuntimeError("sql() requires duckdb (pip install duckdb)")
        with self._lock:
            cur = self._duck.cursor()
            try:
                cur.execute(query)
                cols = [d[0] for d in cur.description]
                return [dict(zip(cols, row)) for row in cur.fetchall()]
            finally:
                cur.close()

    def datasets(self, zone: str | None = None) -> list[dict[str, Any]]:
        out: list[dict[str, Any]] = []
        zones = [zone] if zone else list(ZONES)
        for z in zones:
            zdir = os.path.join(self.root, z)
            if not os.path.isdir(zdir):
                continue
            for ds in sorted(os.listdir(zdir)):
                dpath = os.path.join(zdir, ds)
                if not os.path.isdir(dpath):
                    continue
                parts, files, rows = [], 0, None
                for entry in sorted(os.listdir(dpath)):
                    if entry.startswith("dt="):
                        parts.append(entry[3:])
                        files += sum(1 for f in os.listdir(os.path.join(dpath, entry))
                                     if f.endswith(".parquet"))
                if files and self._duck is not None:
                    glob = self.dataset_path(z, ds).replace("\\", "/")
                    rows = self.sql(
                        f"SELECT count(*) AS n FROM read_parquet('{glob}', "
                        "hive_partitioning=true, union_by_name=true)")[0]["n"]
                out.append({"zone": z, "dataset": ds, "partitions": parts,
                            "files": files, "rows": rows})
        return out


class IcebergLakehouse(Lakehouse):
    """Prod backend: REAL Apache Iceberg tables via the REST catalog on MinIO.

    pyiceberg is an optional dependency (import-guarded): this class only
    loads when ICEBERG_REST_URI is set. Tables are named `<zone>.<dataset>`;
    partitions are day(dt). Every write = one Iceberg snapshot (real
    time-travel, schema evolution handled by the catalog).
    """

    def __init__(self, rest_uri: str | None = None, warehouse: str | None = None) -> None:
        try:
            from pyiceberg.catalog import load_catalog  # noqa: F401
        except ImportError as exc:
            raise RuntimeError(
                "ICEBERG_REST_URI is set but pyiceberg is not installed; "
                "pip install 'pyiceberg[sql-sqlite,pyarrow]' or unset "
                "ICEBERG_REST_URI for the parquet dev fallback") from exc
        from pyiceberg.catalog import load_catalog
        self.rest_uri = rest_uri or os.environ["ICEBERG_REST_URI"]
        props = {
            "uri": self.rest_uri,
            "warehouse": warehouse or os.environ.get(
                "ICEBERG_WAREHOUSE", "s3://meridian-warehouse/"),
            "s3.endpoint": os.environ.get(
                "MINIO_ENDPOINT_URL", f"http://{os.environ.get('MINIO_ENDPOINT', 'localhost:9000')}"),
            "s3.access-key-id": os.environ.get("MINIO_ACCESS_KEY", "minio"),
            "s3.secret-access-key": os.environ.get("MINIO_SECRET_KEY", "minio123"),
        }
        self.catalog = load_catalog("rest", **props)

    def _ident(self, zone: str, dataset: str) -> tuple[str, str]:
        if zone not in ZONES:
            raise ValueError(f"invalid zone {zone!r}; expected one of {ZONES}")
        return zone, dataset.replace("/", "_").replace("..", "_")

    def write(self, zone: str, dataset: str, records: list[dict[str, Any]],
              partition: str | None = None) -> dict[str, Any]:
        if not records:
            return {"dataset": dataset, "zone": zone, "partition": partition,
                    "rows": 0, "files": []}
        import pyarrow as pa
        ns, name = self._ident(zone, dataset)
        partition = partition or _today()
        rows = [dict(r, dt=partition) for r in records]
        cols: list[str] = []
        for r in rows:
            for k in r:
                if k not in cols:
                    cols.append(k)
        data = {c: [r.get(c) if isinstance(r.get(c), (bool, int, float, str, type(None)))
                    else json.dumps(r.get(c), default=str) for r in rows] for c in cols}
        table = pa.table(data)
        try:
            self.catalog.create_namespace(ns)
        except Exception:  # noqa: BLE001 - already exists
            pass
        ident = f"{ns}.{name}"
        try:
            tbl = self.catalog.load_table(ident)
        except Exception:  # noqa: BLE001 - not found
            tbl = self.catalog.create_table(ident, schema=table.schema)
        tbl.append(table)  # one Iceberg snapshot per write
        return {"dataset": dataset, "zone": zone, "partition": partition,
                "rows": len(rows), "files": [], "iceberg_table": ident}

    def read(self, zone: str, dataset: str, where: str | None = None,
             columns: list[str] | None = None, limit: int = 10000) -> list[dict[str, Any]]:
        ns, name = self._ident(zone, dataset)
        tbl = self.catalog.load_table(f"{ns}.{name}")
        scan = tbl.scan(limit=limit)
        if columns:
            scan = scan.select(*columns)
        if where:
            scan = scan.row_filter(where)
        return scan.to_arrow().to_pylist()

    def sql(self, query: str) -> list[dict[str, Any]]:
        raise RuntimeError(
            "sql() over Iceberg tables is served by Trino attached to the "
            "REST catalog (ICEBERG_REST_URI); use Trino's endpoint")

    def datasets(self, zone: str | None = None) -> list[dict[str, Any]]:
        out = []
        for z in ([zone] if zone else list(ZONES)):
            try:
                tables = self.catalog.list_tables(z)
            except Exception:  # noqa: BLE001 - namespace absent
                continue
            for ns, name in tables:
                tbl = self.catalog.load_table(f"{ns}.{name}")
                out.append({"zone": z, "dataset": name,
                            "snapshots": len(tbl.snapshots()),
                            "rows": None, "files": None,
                            "partitions": None})
        return out

    def dataset_path(self, zone: str, dataset: str) -> str:
        ns, name = self._ident(zone, dataset)
        tbl = self.catalog.load_table(f"{ns}.{name}")
        return tbl.location()


def _has_files(ds_dir: str) -> bool:
    if not os.path.isdir(ds_dir):
        return False
    for entry in os.listdir(ds_dir):
        pdir = os.path.join(ds_dir, entry)
        if os.path.isdir(pdir) and any(f.endswith(".parquet") for f in os.listdir(pdir)):
            return True
    return False


def get_lakehouse(root: str) -> Lakehouse:
    """Backend selection: ICEBERG_REST_URI -> real Iceberg REST catalog
    (pyiceberg, import-guarded); otherwise the parquet+catalog dev fallback."""
    if os.environ.get("ICEBERG_REST_URI"):
        return IcebergLakehouse()
    return ParquetLakehouse(root)
