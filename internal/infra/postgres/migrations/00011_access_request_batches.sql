-- +goose Up
-- A batch is several namespace requests a user files at once (same cluster,
-- actions, duration and reason); each stays an ordinary request row and the
-- admin decides the batch in one transaction. The previous release, which
-- keeps running during a rollout, ignores the column and shows the rows as
-- separate requests; one it decides alone makes the batch decision fail with
-- a conflict naming it.
ALTER TABLE access_requests ADD COLUMN batch_id UUID;
CREATE INDEX access_requests_batch_idx ON access_requests (batch_id) WHERE batch_id IS NOT NULL;

-- +goose Down
DROP INDEX access_requests_batch_idx;
ALTER TABLE access_requests DROP COLUMN batch_id;
