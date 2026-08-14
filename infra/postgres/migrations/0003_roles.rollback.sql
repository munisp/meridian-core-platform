-- 0003_roles.rollback.sql — companion rollback for 0003_roles.sql.
-- Drops the per-service svc_* roles. Safe: roles own no objects (schemas,
-- tables and data remain owned by the original owner); reassign is a no-op
-- guard for any accidentally granted ownership. Run as a superuser.
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
        IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = r) THEN
            EXECUTE format('REASSIGN OWNED BY %I TO CURRENT_USER', r);
            EXECUTE format('DROP OWNED BY %I', r);
            EXECUTE format('DROP ROLE %I', r);
        END IF;
    END LOOP;
END $$;
-- Note: REVOKE ALL ON SCHEMA public FROM PUBLIC is intentionally NOT undone
-- here; restoring the pre-hardening PUBLIC grants would weaken the posture.
