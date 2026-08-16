-- 0004_migrator_role.rollback.sql — companion rollback for
-- 0004_migrator_role.sql. Drops the svc_migrator role. Schemas, tables and
-- data remain owned by their original owner; REASSIGN/DROP OWNED are guards
-- for any privileges or stray objects left behind. Run as a superuser.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_migrator') THEN
        EXECUTE 'REASSIGN OWNED BY svc_migrator TO CURRENT_USER';
        EXECUTE 'DROP OWNED BY svc_migrator';
        EXECUTE 'DROP ROLE svc_migrator';
    END IF;
END $$;
