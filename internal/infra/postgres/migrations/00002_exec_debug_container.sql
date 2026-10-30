-- +goose Up
-- Debug sessions: the ephemeral container the session added ('' for exec).
-- Additive with a default, so the previous release keeps working while both run.
ALTER TABLE exec_sessions ADD COLUMN debug_container TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE exec_sessions DROP COLUMN debug_container;
