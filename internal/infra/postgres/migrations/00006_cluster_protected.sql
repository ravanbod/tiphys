-- +goose Up
-- Protected clusters (the Secret's tiphys/protected annotation) are hidden
-- from access requests; grants on them still work. The previous release
-- ignores the column while both run.
ALTER TABLE clusters ADD COLUMN protected BOOLEAN NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE clusters DROP COLUMN protected;
