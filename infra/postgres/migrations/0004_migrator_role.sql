-- 0004_migrator_role.sql — migration-role vs runtime-role split
-- (assurance gates finding: the svc_* runtime roles shipped by 0003 cannot
-- run services' boot-time auto-migrate — CREATE on schemas was revoked —
-- so DDL and runtime DML need separate credentials).
--
-- svc_migrator is the ONLY application role allowed to run DDL. Services
-- boot with DB_MIGRATE_USER=svc_migrator for the migrate step and their own
-- svc_* role for runtime traffic (see infra/postgres/README.md and
-- packages/events/store pg.go ResolveMigrateDatabaseURL).
--
-- Idempotent: safe to re-run. Run as a superuser (or schema owner).

-- ------------------------------------------------------------ role creation
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_migrator') THEN
        CREATE ROLE svc_migrator LOGIN;
    END IF;
END $$;
-- Password is provisioned out-of-band, same as the svc_* roles:
--   psql "$ADMIN_DATABASE_URL" -c \
--     "ALTER ROLE svc_migrator PASSWORD :'pw'" -v pw="$DB_MIGRATE_PASSWORD"

-- svc_migrator may CREATE/DROP/ALTER inside every application schema...
DO $$
DECLARE
    s text;
BEGIN
    FOREACH s IN ARRAY ARRAY[
        'rp_registry', 'tin_graph', 'ledger', 'audit_evidence', 'consent',
        'notification', 'geo', 'search_indexer', 'edge_policy', 'admin_api',
        'onboarding', 'einvoicing'
    ] LOOP
        EXECUTE format('GRANT USAGE, CREATE ON SCHEMA %I TO svc_migrator', s);
        EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA %I TO svc_migrator', s);
        EXECUTE format('GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA %I TO svc_migrator', s);
    END LOOP;
END $$;

-- ...but objects svc_migrator creates must be usable by the runtime roles:
-- default privileges keyed on svc_migrator as the creator.
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
            ('svc_search_indexer', 'search_indexer'),
            ('svc_edge_policy',   'edge_policy'),
            ('svc_admin_api',     'admin_api'),
            ('svc_onboarding',    'onboarding'),
            ('svc_einvoicing',    'einvoicing')
        ) AS v(role_name, schema_name)
    LOOP
        EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE svc_migrator IN SCHEMA %I GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO %I', pair.schema_name, pair.role_name);
        EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE svc_migrator IN SCHEMA %I GRANT USAGE, SELECT ON SEQUENCES TO %I', pair.schema_name, pair.role_name);
    END LOOP;
    -- audit_evidence stays append-only even for migrator-created tables.
    EXECUTE 'ALTER DEFAULT PRIVILEGES FOR ROLE svc_migrator IN SCHEMA audit_evidence GRANT SELECT, INSERT ON TABLES TO svc_audit_evidence';
    EXECUTE 'ALTER DEFAULT PRIVILEGES FOR ROLE svc_migrator IN SCHEMA audit_evidence GRANT USAGE, SELECT ON SEQUENCES TO svc_audit_evidence';
END $$;

-- audit_evidence WORM invariant holds for the migrator too: it may create
-- audit tables but never alter existing audit rows.
REVOKE UPDATE, DELETE, TRUNCATE ON ALL TABLES IN SCHEMA audit_evidence FROM svc_migrator;

-- Rollback: see 0004_migrator_role.rollback.sql.
