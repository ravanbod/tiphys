# Database

PostgreSQL 14 or later (tested on 16). The adapter is `internal/infra/postgres`; the schema lives in `internal/infra/postgres/migrations/` (embedded, applied with goose by `postgres.Migrate`).

## Roles

Three roles, so that the running portal cannot change its own audit trail or its schema.

| Role | Used by | Privileges |
| --- | --- | --- |
| `tiphys_owner` | `postgres.Migrate` (deploy time / init job) | owns the database and every table |
| `tiphys_app` | the portal (`postgres.Open`) | SELECT, INSERT, UPDATE, DELETE on all tables, **except** `audit_events`: SELECT and INSERT only. Nothing on `goose_db_version`. Cannot create objects. |
| `tiphys_retention` | the daily retention job (`postgres.NewAuditRetention`) | SELECT and DELETE on `audit_events`; nothing else |

The app and retention roles must never own tables and must not be members of the owner role; otherwise the restrictions mean nothing.

### Bootstrap

1. As a superuser (or a role with CREATEROLE and CREATEDB), run `deploy/sql/roles.sql` once. It creates the three roles with the given passwords, the database and the connect/schema rights. It is idempotent: re-running resets the passwords.

   ```sh
   psql "postgres://postgres@db.example/postgres" \
     -v owner_password="$OWNER_PW" -v app_password="$APP_PW" -v retention_password="$RET_PW" \
     -f deploy/sql/roles.sql
   ```

   Optional variables: `dbname` (default `tiphys`), `owner_role`, `app_role`, `retention_role`.

2. Run the migrations connected as the owner:

   ```go
   err := postgres.Migrate(ctx, "postgres://tiphys_owner:...@db.example/tiphys")
   // custom role names: postgres.Migrate(ctx, dsn, postgres.WithRoles(postgres.Roles{App: "x", Retention: "y"}))
   ```

   `Migrate` applies pending migrations (guarded by a PostgreSQL advisory lock, so replicas can race safely) and then runs the privilege grants (`privileges.go`). The grants run after **every** call, so tables added by future migrations are covered. A role that does not exist is skipped silently, which is what makes `Migrate` usable on a database where roles are managed elsewhere; the consequence is that a misspelled role name grants nothing, so check the role names if the portal reports `permission denied`.

3. Start the portal with the app role's DSN (`postgres.Open(ctx, postgres.Config{DSN: ...})`) and the retention job with the retention role's DSN (`postgres.NewAuditRetention(ctx, dsn)`).

Secrets (passwords, DSNs) come from mounted Kubernetes Secrets; never put them in the repository.

## Schema notes

- `users.username` is unique case-insensitively (`lower(username)` unique index); `GetByUsername` compares the same way. `keycloak_sub` is unique and NULL until first login. `password_hash` (bcrypt) is set only for local users; a CHECK keeps it and `keycloak_sub` from both being set, and the repositories expose it only through `SetPasswordHash`/`PasswordHash` (`domain.User` has just `Local`).
- `grants`, `access_requests`: CHECK constraints make exactly the fields of the holder/scope kind set; `actions <@ {logs, exec, restart, run-job, sync, rollback}` and non-empty (`time_windows` too; `sync` since migration 4, `rollback` since migration 8). That `sync` and `rollback` are not on a `workloads` scope is checked by the application. Grants (not access requests) may also have a `cluster` scope (`cluster_id` only) or an `environment` scope (the `environment` column only), since migration 5. Workloads of a `workloads` scope are rows in `grant_workloads` (in `access_requests` a JSONB list). That a `workloads` grant has at least one row is checked by the application, not the database.
- Deleting a team removes its memberships, grants and time windows. Deleting a user is not supported by the repositories. A cluster that still has grants cannot be deleted (`ErrConflict`); its environment overrides and access requests go with it.
- `time_windows`: `scope_value` holds the environment, cluster name, Argo project or team **name**. For scope `team` a nullable `team_id` FK (ON DELETE CASCADE) is also set, and the team repository keeps `scope_value` in step when a team is renamed.
- `environment_overrides`: one per `(cluster, namespace)` and one per `argo_project` (partial unique indexes); kind `namespace` requires a cluster.
- `protected_namespaces` is seeded with `kube-system`, `kube-public`, `kube-node-lease`, `tiphys` (the chart's default list; keep the two in sync); admins can edit the list.
- `exec_sessions.cluster` is the cluster **name** (no FK) so a session survives the cluster's removal. It also stores `username`, `workload_kind`, `workload_name` and `environment` as they were at the time.
- `exec_sessions.debug_container` (migration 00002) is the ephemeral container a debug session added, `''` for a plain exec session.
- `break_glass_requests` (migration 00007) holds break-glass requests (mode `approval`). It is a table of its own so the previous release, which reads `break_glass` while both run, never sees a pending request as active. A partial unique index allows one `pending` row per user; CHECKs tie `decided_at` to a decided status and `break_glass_id`/`approved_minutes` to `approved`. Approving creates the `break_glass` row in the same transaction. `created_at` comes from the application clock.
- `clusters.protected` (migration 00006) mirrors the cluster Secret's `tiphys/protected` annotation: the cluster is hidden from access requests, grants on it still work.
- `audit_events` has no foreign keys, so the trail is independent of the rows it mentions. `at` defaults to `now()`; `List` orders by `id` descending (keyset pagination with `BeforeID`), default limit 50, maximum 1000.
- `web_sessions`: `id` is the SHA-256 hex of the cookie value; token columns hold whatever the caller passes (already sealed).
- `settings`: one row per key (`exec.max_session_minutes`, `exec.idle_timeout_minutes`, `break_glass.mode`, `break_glass.enabled`, `break_glass.default_minutes`, `break_glass.max_minutes`, `break_glass.request_max_age_minutes`, `retention.audit_days`, `retention.recordings_days`, `rocket`), JSONB values. Missing keys read as `domain.DefaultSettings()`. `break_glass.enabled` is the mode as the release before migration 7 read it: written as `mode != off`, and read only when `break_glass.mode` is missing (`false` → `off`, otherwise `self`).

## Tests

The tests run against a real PostgreSQL and are skipped unless `TIPHYS_TEST_DATABASE_URL` holds a **superuser** DSN. `deploy/sql/test-db.sh` starts and removes a throwaway container (random free port on 127.0.0.1):

```sh
eval "$(deploy/sql/test-db.sh start)"      # exports TIPHYS_TEST_DATABASE_URL
go test ./internal/infra/postgres/...
deploy/sql/test-db.sh stop
```

`TestMain` migrates one template database; every test clones it, so tests are independent. The test run creates the roles `tiphys_test_app` and `tiphys_test_retention` (cluster-wide, left in place) and runs the repositories as the app role, so a missing grant fails the suite. `TestAuditPrivileges` proves that the app role cannot UPDATE, DELETE or TRUNCATE `audit_events` and that the retention role can DELETE from it.
