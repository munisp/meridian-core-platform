-- pg_partman partition setup (SPEC B section 3).
-- Monthly range partitions on created_at for filings, payments, audit_log.
-- Keep 24 hot partitions; older ones are archived to MinIO (pg_partman
-- retention + external archive job).
-- Idempotent: safe to re-run.
\set ON_ERROR_STOP on

CREATE EXTENSION IF NOT EXISTS pg_partman;

-- ---- filings -------------------------------------------------------------
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_tables WHERE tablename = 'filings') THEN
    CREATE TABLE filings (
      id          uuid        NOT NULL,
      tin         text        NOT NULL,
      period      text        NOT NULL,
      payload     jsonb       NOT NULL,
      created_at  timestamptz NOT NULL DEFAULT now(),
      PRIMARY KEY (id, created_at)
    ) PARTITION BY RANGE (created_at);
  END IF;
END $$;

SELECT partman.create_parent(
  p_parent_table    := 'public.filings',
  p_control         := 'created_at',
  p_type            := 'range',
  p_interval        := 'monthly',
  p_premake         := 3,
  p_start_partition := date_trunc('month', now())::text
);

-- ---- payments ------------------------------------------------------------
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_tables WHERE tablename = 'payments') THEN
    CREATE TABLE payments (
      id          uuid        NOT NULL,
      tin         text        NOT NULL,
      amount      numeric(19,4) NOT NULL,
      status      text        NOT NULL,
      created_at  timestamptz NOT NULL DEFAULT now(),
      PRIMARY KEY (id, created_at)
    ) PARTITION BY RANGE (created_at);
  END IF;
END $$;

SELECT partman.create_parent(
  p_parent_table    := 'public.payments',
  p_control         := 'created_at',
  p_type            := 'range',
  p_interval        := 'monthly',
  p_premake         := 3,
  p_start_partition := date_trunc('month', now())::text
);

-- ---- audit_log -----------------------------------------------------------
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_tables WHERE tablename = 'audit_log') THEN
    CREATE TABLE audit_log (
      id          uuid        NOT NULL,
      actor       text        NOT NULL,
      action      text        NOT NULL,
      resource    text        NOT NULL,
      detail      jsonb,
      created_at  timestamptz NOT NULL DEFAULT now(),
      PRIMARY KEY (id, created_at)
    ) PARTITION BY RANGE (created_at);
  END IF;
END $$;

SELECT partman.create_parent(
  p_parent_table    := 'public.audit_log',
  p_control         := 'created_at',
  p_type            := 'range',
  p_interval        := 'monthly',
  p_premake         := 3,
  p_start_partition := date_trunc('month', now())::text
);

-- ---- retention: 24 hot partitions (SPEC B) --------------------------------
UPDATE partman.part_config
SET retention = '24 months',
    retention_keep_table = true,   -- detach, keep for MinIO archive job
    infinite_time_partitions = true
WHERE parent_table IN ('public.filings', 'public.payments', 'public.audit_log');

-- Partman maintenance is run by the built-in bgw or a cron job:
-- SELECT partman.run_maintenance('public.filings'); etc.
