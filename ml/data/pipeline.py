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

EXTRACT_SQL = """
SELECT tx_id, entity_id AS entity, counterparty_id AS counterparty,
       occurred_at::date AS date, EXTRACT(hour FROM occurred_at)::int AS hour,
       channel, category, amount_kobo, vat_rate, label, fraud_type
FROM transactions
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
    """Iceberg-style partitioned parquet layout: <base>/transactions/date=YYYY-MM-DD/part.parquet.

    Writes to MinIO (s3fs-compatible path) when MINIO_* set, else local dir.
    Returns the base path used.
    """
    endpoint = os.environ.get("MINIO_ENDPOINT", "").strip()
    if endpoint and base_dir is None:
        bucket = os.environ.get("MINIO_BUCKET", "meridian-lakehouse")
        scheme = "https" if os.environ.get("MINIO_USE_SSL", "false").lower() == "true" else "http"
        base = f"s3://{bucket}/lakehouse"
        storage_options = {
            "key": os.environ.get("MINIO_ACCESS_KEY", ""),
            "secret": os.environ.get("MINIO_SECRET_KEY", ""),
            "client_kwargs": {"endpoint_url": f"{scheme}://{endpoint}"},
        }
        log.info("profile=prod component=ml-lakehouse target=minio bucket=%s", bucket)
    else:
        base = base_dir or str(Path(__file__).resolve().parents[1] / "data" / "lakehouse")
        storage_options = None
        log.info("profile=dev component=ml-lakehouse target=local path=%s", base)

    out = df.copy()
    out["date"] = pd.to_datetime(out["date"]).dt.strftime("%Y-%m-%d")
    out.to_parquet(base, engine="pyarrow", partition_cols=["date"],
                   index=False, storage_options=storage_options)
    return base


def read_lakehouse(base_dir: str | None = None) -> pd.DataFrame:
    """Read the lakehouse back via duckdb (works for local layout; prod via httpfs)."""
    import duckdb
    base = base_dir or str(Path(__file__).resolve().parents[1] / "data" / "lakehouse")
    return duckdb.sql(f"SELECT * FROM read_parquet('{base}/transactions/date=*/*.parquet', hive_partitioning=true)").df()
