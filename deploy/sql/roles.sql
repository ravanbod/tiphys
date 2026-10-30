-- Bootstrap of the Tiphys database and its three roles. Run once, as a
-- PostgreSQL superuser (or a role with CREATEROLE and CREATEDB), before the
-- first Migrate:
--
--   psql "postgres://postgres@db.example/postgres" \
--     -v owner_password="$OWNER_PW" -v app_password="$APP_PW" -v retention_password="$RET_PW" \
--     -f deploy/sql/roles.sql
--
-- Required variables: owner_password, app_password, retention_password.
-- Optional (defaults in brackets): dbname [tiphys], owner_role [tiphys_owner],
-- app_role [tiphys_app], retention_role [tiphys_retention]. If you change the
-- app or retention role names, pass the same names to postgres.WithRoles.
--
-- Safe to re-run: roles and the database are created only when missing and the
-- passwords are reset. Table privileges are NOT set here; postgres.Migrate
-- grants them after each migration run (see docs/database.md).

\set ON_ERROR_STOP on

\if :{?dbname}
\else
  \set dbname tiphys
\endif
\if :{?owner_role}
\else
  \set owner_role tiphys_owner
\endif
\if :{?app_role}
\else
  \set app_role tiphys_app
\endif
\if :{?retention_role}
\else
  \set retention_role tiphys_retention
\endif

-- Roles (CREATE ROLE has no IF NOT EXISTS, hence \gexec).
SELECT format('CREATE ROLE %I LOGIN', :'owner_role')
 WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = :'owner_role') \gexec
SELECT format('CREATE ROLE %I LOGIN', :'app_role')
 WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = :'app_role') \gexec
SELECT format('CREATE ROLE %I LOGIN', :'retention_role')
 WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = :'retention_role') \gexec

-- The app and retention roles must never own objects or be able to create them.
SELECT format('ALTER ROLE %I NOSUPERUSER NOCREATEDB NOCREATEROLE PASSWORD %L', :'owner_role', :'owner_password') \gexec
SELECT format('ALTER ROLE %I NOSUPERUSER NOCREATEDB NOCREATEROLE PASSWORD %L', :'app_role', :'app_password') \gexec
SELECT format('ALTER ROLE %I NOSUPERUSER NOCREATEDB NOCREATEROLE PASSWORD %L', :'retention_role', :'retention_password') \gexec

SELECT format('CREATE DATABASE %I OWNER %I', :'dbname', :'owner_role')
 WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = :'dbname') \gexec

REVOKE ALL ON DATABASE :"dbname" FROM PUBLIC;
GRANT CONNECT ON DATABASE :"dbname" TO :"owner_role", :"app_role", :"retention_role";

\connect :"dbname"

-- Only the owner may create objects in the schema; the others just use it.
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
GRANT USAGE, CREATE ON SCHEMA public TO :"owner_role";
