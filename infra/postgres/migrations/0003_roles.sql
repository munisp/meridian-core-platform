-- 0003_roles.sql — per-service Postgres roles with least-privilege GRANTs
-- (assurance HIGH, w2 §6A #1: previously a single shared superuser served
-- every service, including audit stores, with zero GRANT/REVOKE).
--
-- Idempotent: safe to re-run. Roles are created WITHOUT a password here;
-- passwords are provisioned out-of-band from the environment/vault by the
-- deploy tooling, e.g.:
--   psql "$ADMIN_DATABASE_URL" -c \
--     "ALTER ROLE svc_audit_evidence PASSWORD :'pw'" -v pw="$AUDIT_EVIDENCE_DB_PASSWORD"
-- (see infra/.env.example — <SERVICE>_DB_USER / <SERVICE>_DB_PASSWORD).
-- Services resolve their per-service credentials via DB_USER/DB_PASSWORD;
-- if DB_USER is unset they fall back to the shared user with a loud startup
-- warning in non-prod and refuse to start with PROFILE=prod
-- (packages/events/store pg.go ResolveDatabaseURL).

-- ------------------------------------------------------------ role creation
DO $$
DECLARE
    r text;
    app_roles text[] := ARRAY[
        'svc_rp_registry', 'svc_tin_graph', 'svc_ledger', 'svc_audit_evidence',
        'svc_consent', 'svc_notification', 'svc_geo', 'svc_search_indexer',
        'svc_edge_policy', 'svc_admin_api', 'svc_onboarding', 'svc_einvoicing'
    ];
    ddl_roles text[] := ARRAY['svc_keycloak', 'svc_permify', 'svc_temporal'];
BEGIN
    FOREACH r IN ARRAY app_roles || ddl_roles LOOP
        IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = r) THEN
            EXECUTE format('CREATE ROLE %I LOGIN', r);
        END IF;
    END LOOP;
END $$;

-- -------------------------------------------- strip default PUBLIC exposure
REVOKE ALL ON SCHEMA public FROM PUBLIC;
DO $$
DECLARE
    s text;
BEGIN
    FOREACH s IN ARRAY ARRAY[
        'rp_registry', 'tin_graph', 'ledger', 'audit_evidence', 'consent',
        'notification', 'geo', 'search_indexer', 'edge_policy', 'admin_api',
        'onboarding', 'einvoicing', 'permify', 'keycloak'
    ] LOOP
        EXECUTE format('REVOKE ALL ON SCHEMA %I FROM PUBLIC', s);
    END LOOP;
END $$;

-- ------------------------------------- least privilege: one role, one schema
-- Macro pattern per service role <svc_x>: USAGE on schema x plus
-- SELECT/INSERT/UPDATE/DELETE on its tables (sequences for serial columns).
-- Audit schema is special-cased append-only below.
DO $$
DECLARE
    pair record;
BEGIN
    FOR pair IN
        SELECT * FROM (VALUES
            ('svc_rp_registry',   'rp_registry'),
            ('svc_tin_graph',     'tin_graph'),
            ('svc_ledger',        'ledger'),
            ('svc_consent',       'consent'),
            ('svc_notification',  'notification'),
            ('svc_geo',           'geo'),
            ('svc_search_indexer','search_indexer'),
            ('svc_edge_policy',   'edge_policy'),
            ('svc_admin_api',     'admin_api'),
            ('svc_onboarding',    'onboarding'),
            ('svc_einvoicing',    'einvoicing')
        ) AS v(role_name, schema_name)
    LOOP
        EXECUTE format('GRANT USAGE ON SCHEMA %I TO %I', pair.schema_name, pair.role_name);
        EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA %I TO %I', pair.schema_name, pair.role_name);
        EXECUTE format('GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA %I TO %I', pair.schema_name, pair.role_name);
        EXECUTE format('ALTER DEFAULT PRIVILEGES IN SCHEMA %I GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO %I', pair.schema_name, pair.role_name);
        EXECUTE format('ALTER DEFAULT PRIVILEGES IN SCHEMA %I GRANT USAGE, SELECT ON SEQUENCES TO %I', pair.schema_name, pair.role_name);
    END LOOP;
END $$;

-- ------------------------------------- middleware roles manage own schema DDL
-- Keycloak / Permify / Temporal auto-migrate their schemas on boot, so their
-- roles own (ALL on) their schema — but NOTHING outside it.
GRANT USAGE ON SCHEMA keycloak TO svc_keycloak;
GRANT ALL ON ALL TABLES IN SCHEMA keycloak TO svc_keycloak;
ALTER DEFAULT PRIVILEGES IN SCHEMA keycloak GRANT ALL ON TABLES TO svc_keycloak;

GRANT USAGE ON SCHEMA permify TO svc_permify;
GRANT ALL ON ALL TABLES IN SCHEMA permify TO svc_permify;
ALTER DEFAULT PRIVILEGES IN SCHEMA permify GRANT ALL ON TABLES TO svc_permify;

-- ------------------------------------------- audit_evidence: append-only WORM
-- Writable ONLY by the audit role, and even that role cannot UPDATE/DELETE:
-- audit evidence is an append-only, keyed-chain store (tampering requires a
-- superuser, never an app credential).
GRANT USAGE ON SCHEMA audit_evidence TO svc_audit_evidence;
GRANT SELECT, INSERT ON ALL TABLES IN SCHEMA audit_evidence TO svc_audit_evidence;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA audit_evidence TO svc_audit_evidence;
ALTER DEFAULT PRIVILEGES IN SCHEMA audit_evidence GRANT SELECT, INSERT ON TABLES TO svc_audit_evidence;
ALTER DEFAULT PRIVILEGES IN SCHEMA audit_evidence GRANT USAGE, SELECT ON SEQUENCES TO svc_audit_evidence;
-- Explicitly revoke write-back privileges from every app role (defence in
-- depth: no app role — including the audit role — may alter audit rows).
DO $$
DECLARE
    r text;
BEGIN
    FOREACH r IN ARRAY ARRAY[
        'svc_rp_registry', 'svc_tin_graph', 'svc_ledger', 'svc_audit_evidence',
        'svc_consent', 'svc_notification', 'svc_geo', 'svc_search_indexer',
        'svc_edge_policy', 'svc_admin_api', 'svc_onboarding', 'svc_einvoicing',
        'svc_keycloak', 'svc_permify', 'svc_temporal'
    ] LOOP
        EXECUTE format('REVOKE UPDATE, DELETE, TRUNCATE ON ALL TABLES IN SCHEMA audit_evidence FROM %I', r);
    END LOOP;
END $$;

-- Rollback: see 0003_roles.rollback.sql (drops the svc_* roles after
-- reassigning/dropping their privileges; schemas and data are untouched).
