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

-- noord_grafana (spec 0016): the dashboard's view only role. Migration 00008
-- gives it the ops views and nothing else. It is capped here because only a
-- superuser can set these; the cluster sets the same in CloudNativePG.
DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'noord_grafana') THEN
        CREATE ROLE noord_grafana LOGIN NOINHERIT PASSWORD 'noord-dev';
    END IF;
END
$$;
ALTER ROLE noord_grafana CONNECTION LIMIT 5;
ALTER ROLE noord_grafana SET statement_timeout = '10s';

-- noord_owner owns the database it migrates, as in the cluster (CloudNativePG
-- makes it the initdb owner). Migration 00008 creates a schema and revokes
-- TEMPORARY from PUBLIC, which only the database owner may do.
DO $$
BEGIN
    EXECUTE format('ALTER DATABASE %I OWNER TO noord_owner', current_database());
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
