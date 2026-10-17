-- The four database roles the app uses (spec 0003). Run as a superuser:
-- Compose runs it on a fresh volume, `make db-roles` on an existing one, and
-- internal/db/dbtest before DB tests. Safe to run again.
--
-- noord_owner owns every table and only runs migrations. Each binary gets its
-- own role so the grants in migration 00002 decide what it may touch.
-- The password is the throwaway local dev value compose.yaml already uses.
-- In the cluster, CloudNativePG creates these roles from Secrets (feature #17).

DO $$
DECLARE
    role_name text;
BEGIN
    FOREACH role_name IN ARRAY ARRAY['noord_owner', 'noord_agent', 'noord_toolserver', 'noord_eval'] LOOP
        IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = role_name) THEN
            EXECUTE format('CREATE ROLE %I LOGIN PASSWORD %L', role_name, 'noord-dev');
        END IF;
    END LOOP;
END
$$;

-- Postgres 15 and later only let the database owner create in public.
GRANT CREATE ON SCHEMA public TO noord_owner;

-- A volume migrated before roles existed has goose's table owned by the
-- superuser; hand it to the owner role so `make migrate` can record versions.
DO $$
BEGIN
    IF to_regclass('public.goose_db_version') IS NOT NULL THEN
        ALTER TABLE public.goose_db_version OWNER TO noord_owner;
    END IF;
END
$$;
