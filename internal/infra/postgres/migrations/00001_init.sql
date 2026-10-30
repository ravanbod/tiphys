-- +goose Up
-- Tiphys schema. Privileges are not granted here: Migrate applies them after
-- the migrations (see grants.go) so that tables added later are covered too.

CREATE TABLE users (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    keycloak_sub  TEXT UNIQUE,
    username      TEXT NOT NULL CHECK (username <> ''),
    display_name  TEXT NOT NULL DEFAULT '',
    role          TEXT NOT NULL CHECK (role IN ('user', 'auditor', 'admin')),
    disabled      BOOLEAN NOT NULL DEFAULT false,
    last_login_at TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by    TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX users_username_lower_key ON users (lower(username));

CREATE TABLE teams (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE CHECK (name <> ''),
    description TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by  TEXT NOT NULL DEFAULT ''
);

CREATE TABLE team_members (
    team_id    BIGINT NOT NULL REFERENCES teams (id) ON DELETE CASCADE,
    user_id    BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, user_id)
);
CREATE INDEX team_members_user_idx ON team_members (user_id);

CREATE TABLE clusters (
    id                 BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name               TEXT NOT NULL UNIQUE CHECK (name <> ''),
    environment        TEXT NOT NULL DEFAULT '',
    kubeconfig_secret  TEXT NOT NULL DEFAULT '',
    kubernetes_version TEXT NOT NULL DEFAULT '',
    last_check_at      TIMESTAMPTZ,
    reachable          BOOLEAN NOT NULL DEFAULT false,
    privilege_check_ok BOOLEAN NOT NULL DEFAULT false,
    admission_mode     TEXT NOT NULL DEFAULT 'none' CHECK (admission_mode IN ('vap', 'kyverno', 'none')),
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by         TEXT NOT NULL DEFAULT ''
);

CREATE TABLE environment_overrides (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    kind         TEXT NOT NULL CHECK (kind IN ('argo_project', 'namespace')),
    cluster_id   BIGINT REFERENCES clusters (id) ON DELETE CASCADE,
    argo_project TEXT,
    namespace    TEXT,
    environment  TEXT NOT NULL CHECK (environment <> ''),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT environment_overrides_fields CHECK (
        (kind = 'argo_project' AND argo_project IS NOT NULL AND argo_project <> ''
            AND cluster_id IS NULL AND namespace IS NULL)
        OR
        (kind = 'namespace' AND cluster_id IS NOT NULL AND namespace IS NOT NULL AND namespace <> ''
            AND argo_project IS NULL)
    )
);
CREATE UNIQUE INDEX environment_overrides_namespace_key
    ON environment_overrides (cluster_id, namespace) WHERE kind = 'namespace';
CREATE UNIQUE INDEX environment_overrides_project_key
    ON environment_overrides (argo_project) WHERE kind = 'argo_project';

CREATE TABLE protected_namespaces (
    namespace  TEXT PRIMARY KEY CHECK (namespace <> ''),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- Safe defaults; admins can edit the list.
INSERT INTO protected_namespaces (namespace) VALUES ('kube-system'), ('kube-public'), ('kube-node-lease'), ('tiphys');

-- Deleting a team removes its grants; a cluster that still has grants cannot
-- be deleted (the admin removes the grants first, on purpose).
CREATE TABLE grants (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    holder_kind  TEXT NOT NULL CHECK (holder_kind IN ('team', 'user')),
    team_id      BIGINT REFERENCES teams (id) ON DELETE CASCADE,
    user_id      BIGINT REFERENCES users (id) ON DELETE CASCADE,
    scope_kind   TEXT NOT NULL CHECK (scope_kind IN ('argo_project', 'namespace', 'workloads')),
    argo_project TEXT,
    cluster_id   BIGINT REFERENCES clusters (id) ON DELETE RESTRICT,
    namespace    TEXT,
    actions      TEXT[] NOT NULL,
    expires_at   TIMESTAMPTZ,
    note         TEXT NOT NULL DEFAULT '',
    created_by   TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by   TEXT NOT NULL DEFAULT '',
    CONSTRAINT grants_holder CHECK (
        (holder_kind = 'team' AND team_id IS NOT NULL AND user_id IS NULL)
        OR
        (holder_kind = 'user' AND user_id IS NOT NULL AND team_id IS NULL)
    ),
    CONSTRAINT grants_scope CHECK (
        (scope_kind = 'argo_project' AND argo_project IS NOT NULL AND argo_project <> ''
            AND cluster_id IS NULL AND namespace IS NULL)
        OR
        (scope_kind IN ('namespace', 'workloads') AND argo_project IS NULL
            AND cluster_id IS NOT NULL AND namespace IS NOT NULL AND namespace <> '')
    ),
    CONSTRAINT grants_actions CHECK (
        cardinality(actions) > 0 AND actions <@ ARRAY['logs', 'exec', 'restart', 'run-job']::text[]
    )
);
CREATE INDEX grants_team_idx ON grants (team_id) WHERE team_id IS NOT NULL;
CREATE INDEX grants_user_idx ON grants (user_id) WHERE user_id IS NOT NULL;
CREATE INDEX grants_cluster_idx ON grants (cluster_id) WHERE cluster_id IS NOT NULL;

CREATE TABLE grant_workloads (
    grant_id BIGINT NOT NULL REFERENCES grants (id) ON DELETE CASCADE,
    kind     TEXT NOT NULL CHECK (kind IN ('Deployment', 'StatefulSet', 'DaemonSet', 'Rollout', 'CronJob')),
    name     TEXT NOT NULL CHECK (name <> ''),
    PRIMARY KEY (grant_id, kind, name)
);

-- scope_value holds the environment, cluster name, Argo project or team name.
-- For scope team, team_id is the resolved FK so that deleting the team
-- removes its windows.
CREATE TABLE time_windows (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name         TEXT NOT NULL CHECK (name <> ''),
    scope_kind   TEXT NOT NULL CHECK (scope_kind IN ('global', 'environment', 'cluster', 'argo_project', 'team')),
    scope_value  TEXT,
    team_id      BIGINT REFERENCES teams (id) ON DELETE CASCADE,
    actions      TEXT[] NOT NULL,
    days_of_week SMALLINT[] NOT NULL,
    start_time   TIME NOT NULL,
    end_time     TIME NOT NULL,
    timezone     TEXT NOT NULL DEFAULT 'Asia/Tehran',
    effect       TEXT NOT NULL CHECK (effect IN ('allow_inside', 'deny_inside')),
    enabled      BOOLEAN NOT NULL DEFAULT true,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by   TEXT NOT NULL DEFAULT '',
    CONSTRAINT time_windows_scope CHECK (
        (scope_kind = 'global' AND scope_value IS NULL AND team_id IS NULL)
        OR
        (scope_kind = 'team' AND scope_value IS NOT NULL AND scope_value <> '' AND team_id IS NOT NULL)
        OR
        (scope_kind IN ('environment', 'cluster', 'argo_project')
            AND scope_value IS NOT NULL AND scope_value <> '' AND team_id IS NULL)
    ),
    CONSTRAINT time_windows_actions CHECK (
        cardinality(actions) > 0 AND actions <@ ARRAY['logs', 'exec', 'restart', 'run-job']::text[]
    ),
    CONSTRAINT time_windows_days CHECK (
        cardinality(days_of_week) > 0 AND days_of_week <@ ARRAY[0, 1, 2, 3, 4, 5, 6]::smallint[]
    )
);
CREATE INDEX time_windows_team_idx ON time_windows (team_id) WHERE team_id IS NOT NULL;

CREATE TABLE holidays (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    date         DATE NOT NULL,
    title        TEXT NOT NULL DEFAULT '',
    environments TEXT[] NOT NULL DEFAULT '{}', -- empty = all environments
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX holidays_date_idx ON holidays (date);

CREATE TABLE break_glass (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id    BIGINT NOT NULL REFERENCES users (id),
    reason     TEXT NOT NULL,
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL,
    ended_at   TIMESTAMPTZ,
    ended_by   TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (expires_at > started_at)
);
CREATE INDEX break_glass_user_idx ON break_glass (user_id, started_at DESC);
CREATE INDEX break_glass_started_idx ON break_glass (started_at DESC);

CREATE TABLE access_requests (
    id                 BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id            BIGINT NOT NULL REFERENCES users (id),
    scope_kind         TEXT NOT NULL CHECK (scope_kind IN ('argo_project', 'namespace', 'workloads')),
    argo_project       TEXT,
    cluster_id         BIGINT REFERENCES clusters (id) ON DELETE CASCADE,
    namespace          TEXT,
    workloads          JSONB NOT NULL DEFAULT '[]',
    actions            TEXT[] NOT NULL,
    duration_hours     INTEGER CHECK (duration_hours > 0), -- NULL = permanent
    reason             TEXT NOT NULL DEFAULT '',
    status             TEXT NOT NULL DEFAULT 'pending'
                       CHECK (status IN ('pending', 'approved', 'rejected', 'expired')),
    decided_by         TEXT,
    decided_at         TIMESTAMPTZ,
    decision_note      TEXT,
    resulting_grant_id BIGINT REFERENCES grants (id) ON DELETE SET NULL,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT access_requests_scope CHECK (
        (scope_kind = 'argo_project' AND argo_project IS NOT NULL AND argo_project <> ''
            AND cluster_id IS NULL AND namespace IS NULL)
        OR
        (scope_kind IN ('namespace', 'workloads') AND argo_project IS NULL
            AND cluster_id IS NOT NULL AND namespace IS NOT NULL AND namespace <> '')
    ),
    CONSTRAINT access_requests_actions CHECK (
        cardinality(actions) > 0 AND actions <@ ARRAY['logs', 'exec', 'restart', 'run-job']::text[]
    ),
    CONSTRAINT access_requests_decision CHECK (
        (status = 'pending') = (decided_at IS NULL)
    ),
    CONSTRAINT access_requests_grant CHECK (resulting_grant_id IS NULL OR status = 'approved')
);
CREATE INDEX access_requests_user_idx ON access_requests (user_id, id DESC);
CREATE INDEX access_requests_pending_idx ON access_requests (created_at) WHERE status = 'pending';

-- cluster is the cluster name, not an FK: a session is history and must
-- outlive the cluster registration.
CREATE TABLE exec_sessions (
    id              UUID PRIMARY KEY,
    user_id         BIGINT NOT NULL REFERENCES users (id),
    username        TEXT NOT NULL,
    cluster         TEXT NOT NULL,
    namespace       TEXT NOT NULL,
    pod             TEXT NOT NULL,
    container       TEXT NOT NULL DEFAULT '',
    shell           TEXT NOT NULL DEFAULT '',
    workload_kind   TEXT NOT NULL DEFAULT '',
    workload_name   TEXT NOT NULL DEFAULT '',
    environment     TEXT NOT NULL DEFAULT '',
    break_glass_id  BIGINT REFERENCES break_glass (id),
    started_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    ended_at        TIMESTAMPTZ,
    end_reason      TEXT NOT NULL DEFAULT '',
    recording_key   TEXT NOT NULL DEFAULT '',
    recording_bytes BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX exec_sessions_started_idx ON exec_sessions (started_at DESC);
CREATE INDEX exec_sessions_user_idx ON exec_sessions (user_id, started_at DESC);
CREATE INDEX exec_sessions_active_idx ON exec_sessions (started_at) WHERE ended_at IS NULL;

-- Append-only for the application role (see grants.go). No foreign keys: the
-- trail must stay intact whatever happens to the rows it mentions.
CREATE TABLE audit_events (
    id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    user_id        BIGINT,
    username       TEXT NOT NULL DEFAULT '',
    event_type     TEXT NOT NULL,
    action         TEXT,
    cluster        TEXT,
    namespace      TEXT,
    workload_kind  TEXT,
    workload_name  TEXT,
    pod            TEXT,
    container      TEXT,
    environment    TEXT,
    result         TEXT NOT NULL CHECK (result IN ('allowed', 'denied', 'succeeded', 'failed')),
    reason         TEXT,
    break_glass_id BIGINT,
    session_id     TEXT,
    details        JSONB,
    source_ip      TEXT NOT NULL DEFAULT '',
    user_agent     TEXT NOT NULL DEFAULT ''
);
CREATE INDEX audit_events_at_idx ON audit_events (at DESC);
CREATE INDEX audit_events_user_idx ON audit_events (user_id, at DESC);
CREATE INDEX audit_events_username_idx ON audit_events (lower(username), at DESC);
CREATE INDEX audit_events_type_idx ON audit_events (event_type, at DESC);
CREATE INDEX audit_events_cluster_idx ON audit_events (cluster, at DESC);
CREATE INDEX audit_events_namespace_idx ON audit_events (namespace, at DESC);
CREATE INDEX audit_events_session_idx ON audit_events (session_id) WHERE session_id IS NOT NULL;
CREATE INDEX audit_events_break_glass_idx ON audit_events (break_glass_id) WHERE break_glass_id IS NOT NULL;

CREATE TABLE settings (
    key        TEXT PRIMARY KEY,
    value      JSONB NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by TEXT NOT NULL DEFAULT ''
);

-- id is the sha256 hex of the cookie value; tokens are stored exactly as the
-- caller gives them (already sealed).
CREATE TABLE web_sessions (
    id              TEXT PRIMARY KEY,
    user_id         BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    csrf_token      TEXT NOT NULL,
    access_token    TEXT NOT NULL DEFAULT '',
    refresh_token   TEXT NOT NULL DEFAULT '',
    id_token        TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_refresh_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at      TIMESTAMPTZ NOT NULL,
    source_ip       TEXT NOT NULL DEFAULT '',
    user_agent      TEXT NOT NULL DEFAULT ''
);
CREATE INDEX web_sessions_user_idx ON web_sessions (user_id);
CREATE INDEX web_sessions_expires_idx ON web_sessions (expires_at);
CREATE INDEX web_sessions_refresh_idx ON web_sessions (last_refresh_at);

-- +goose Down
DROP TABLE web_sessions, settings, audit_events, exec_sessions, access_requests, break_glass,
    holidays, time_windows, grant_workloads, grants, protected_namespaces,
    environment_overrides, clusters, team_members, teams, users;
