-- +goose Up
-- Scheduled break-glass: a request may name when the break-glass should
-- start (NULL: when it is approved). A scheduled break-glass itself is an
-- ordinary break_glass row whose started_at is in the future; the previous
-- release only treats rows with started_at <= now as active, and reads
-- requests without this column (it expires them by created_at alone).
ALTER TABLE break_glass_requests ADD COLUMN starts_at TIMESTAMPTZ;
-- The policy reload reads every break-glass not yet expired, also future ones.
CREATE INDEX break_glass_expires_idx ON break_glass (expires_at);

-- +goose Down
DROP INDEX break_glass_expires_idx;
ALTER TABLE break_glass_requests DROP COLUMN starts_at;
