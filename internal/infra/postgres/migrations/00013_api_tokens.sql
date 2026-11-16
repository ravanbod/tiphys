-- +goose Up
-- API tokens: service accounts that change configuration (time windows,
-- holidays, settings) with the permissions an admin chose. Only the sha256 of
-- the token is stored. A table of its own, so the previous release, which
-- never reads it, keeps working while both run.
CREATE TABLE api_tokens (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name         TEXT NOT NULL CHECK (name <> ''),
    description  TEXT NOT NULL DEFAULT '',
    token_hash   TEXT NOT NULL UNIQUE,
    prefix       TEXT NOT NULL,
    permissions  TEXT[] NOT NULL,
    created_by   TEXT NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at   TIMESTAMPTZ,
    last_used_at TIMESTAMPTZ,
    revoked_at   TIMESTAMPTZ,
    revoked_by   TEXT NOT NULL DEFAULT ''
);
-- One name per token that is not revoked, so audit entries stay readable.
CREATE UNIQUE INDEX api_tokens_active_name_key ON api_tokens (lower(name)) WHERE revoked_at IS NULL;

-- +goose Down
DROP TABLE api_tokens;
