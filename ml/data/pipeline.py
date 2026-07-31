"""Production extract pipeline: Postgres (DATABASE_URL) -> parquet -> feature frames.

H1 contract: if DATABASE_URL is unset/empty -> dev fallback to the synthetic
generator (log `profile=dev component=ml-pipeline`); if set -> real Postgres
extract via psycopg. Lakehouse writer: Iceberg-style parquet layout to MinIO
(MINIO_* set) else ./data/lakehouse. Startup never fails on missing prod vars.
"""
from __future__ import annotations

import logging
import os
from pathlib import Path

import pandas as pd

from . import synthetic

log = logging.getLogger("ml.pipeline")
logging.basicConfig(level=logging.INFO, format="%(message)s")

# The extract reads the ingestion VIEW `ml_transactions_v`, not a physical
# `transactions` table (audit I6: nothing ever wrote such a table). The view
# is defined over the lakehouse bronze transactions table
# (bronze_txs_local / the Iceberg table bronze.txs_events written by
# ml/pipelines/lakehouse_sink.py). DDL — documented in docs/ingestion.md:
#
#   CREATE VIEW ml_transactions_v AS
#   SELECT id                                   AS tx_id,
#          data->>'tin_hash'                    AS entity_id,
#          data->>'counterparty_hash'           AS counterparty_id,
#          (data->>'occurred_at')::timestamptz  AS occurred_at,
#          data->>'channel'                     AS channel,
#          data->>'category'                    AS category,
#          (data->>'amount_kobo')::bigint       AS amount_kobo,
#          (data->>'vat_rate')::double          AS vat_rate,
#          0                                     AS label,
#          ''                                    AS fraud_type
#   FROM bronze_txs;   -- outbox/bronze mirror of nrs.* payment topics
#
# Services (or the sink backfill) materialise `bronze_txs` from the bronze
# zone; until then the view can be defined over the JSONL outbox import.
EXTRACT_TABLE = os.environ.get("ML_EXTRACT_TABLE", "ml_transactions_v")
EXTRACT_SQL = f"""
SELECT tx_id, entity_id AS entity, counterparty_id AS counterparty,
       occurred_at::date AS date, EXTRACT(hour FROM occurred_at)::int AS hour,
       channel, category, amount_kobo, vat_rate, label, fraud_type
FROM {EXTRACT_TABLE}
WHERE occurred_at >= %(window_start)s AND occurred_at < %(window_end)s
ORDER BY tx_id
"""


def extract_transactions(window_start: str = "2024-01-01", window_end: str = "2024-05-01",
                         out_dir: str | None = None, **synth_kwargs) -> pd.DataFrame:
    """Return raw transaction frame from Postgres if DATABASE_URL set, else synthetic."""
    db_url = os.environ.get("DATABASE_URL", "").strip()
    if db_url:
        log.info("profile=prod component=ml-pipeline source=postgres")
        import psycopg  # psycopg[binary], optional prod dep
        with psycopg.connect(db_url) as conn:
            df = pd.read_sql(EXTRACT_SQL, conn, params={"window_start": window_start, "window_end": window_end})
        if "label" not in df:
            df["label"] = 0
        if "fraud_type" not in df:
            df["fraud_type"] = ""
    else:
        log.info("profile=dev component=ml-pipeline source=synthetic")
        data = synthetic.generate(**({} if not synth_kwargs else synth_kwargs))
        df = data.transactions
    if out_dir:
        write_lakehouse(df, out_dir)
    return df


def feature_frame(df: pd.DataFrame) -> tuple[pd.DataFrame, list[str]]:
    """Raw transactions -> model feature frame (X columns + label)."""
    feat = synthetic.add_features(df) if "log_amount" not in df.columns else df
    return feat, list(synthetic.FEATURES)


def write_lakehouse(df: pd.DataFrame, base_dir: str | None = None) -> str:
    """Write transaction frames to the unified lakehouse (audit I4).

    Layout converges on the shared zone/dataset/dt convention:
    <base>/silver/transactions/dt=YYYY-MM-DD/part-*.parquet plus a
    catalog.json manifest (tables, schema versions, snapshots) via
    ml.data.lakehouse.ParquetLakehouse. When ICEBERG_REST_URI is set the
    real Iceberg REST catalog on MinIO is used instead. Legacy MinIO
    (MINIO_ENDPOINT, no ICEBERG_REST_URI) keeps the old s3 partitioned
    write for compatibility.
    """
    endpoint = os.environ.get("MINIO_ENDPOINT", "").strip()
    if endpoint and base_dir is None and not os.environ.get("ICEBERG_REST_URI"):
        bucket = os.environ.get("MINIO_BUCKET", "meridian-lakehouse")
        scheme = "https" if os.environ.get("MINIO_USE_SSL", "false").lower() == "true" else "http"
        base = f"s3://{bucket}/lakehouse"
        storage_options = {
            "key": os.environ.get("MINIO_ACCESS_KEY", ""),
            "secret": os.environ.get("MINIO_SECRET_KEY", ""),
            "client_kwargs": {"endpoint_url": f"{scheme}://{endpoint}"},
        }
        log.info("profile=prod component=ml-lakehouse target=minio bucket=%s (legacy layout)", bucket)
        out = df.copy()
        out["date"] = pd.to_datetime(out["date"]).dt.strftime("%Y-%m-%d")
        out.to_parquet(base, engine="pyarrow", partition_cols=["date"],
                       index=False, storage_options=storage_options)
        return base

    from .lakehouse import get_lakehouse
    base = base_dir or str(Path(__file__).resolve().parents[1] / "data" / "lakehouse")
    lh = get_lakehouse(base)
    log.info("component=ml-lakehouse impl=%s path=%s", type(lh).__name__, base)
    out = df.copy()
    out["date"] = pd.to_datetime(out["date"]).dt.strftime("%Y-%m-%d")
    for day, grp in out.groupby("date"):
        lh.write("silver", "transactions",
                 grp.drop(columns=[]).to_dict("records"), partition=str(day))
    return base


def read_lakehouse(base_dir: str | None = None) -> pd.DataFrame:
    """Read the lakehouse back via duckdb (works for local layout; prod via httpfs)."""
    import duckdb
    base = base_dir or str(Path(__file__).resolve().parents[1] / "data" / "lakehouse")
    new_glob = f"{base}/silver/transactions/dt=*/*.parquet"
    if Path(f"{base}/silver").exists():
        return duckdb.sql(f"SELECT * FROM read_parquet('{new_glob}', hive_partitioning=true)").df()
    return duckdb.sql(f"SELECT * FROM read_parquet('{base}/transactions/date=*/*.parquet', hive_partitioning=true)").df()
