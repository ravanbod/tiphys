-- +goose Up
-- Break-glass requests (break-glass mode "approval"): an admin approves one
-- and the break_glass row it creates starts then. A separate table, so the
-- previous release, which reads break_glass while both run, never sees a
-- pending request as an active break-glass.
CREATE TABLE break_glass_requests (
    id               BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id          BIGINT NOT NULL REFERENCES users (id),
    reason           TEXT NOT NULL,
    minutes          INTEGER NOT NULL CHECK (minutes > 0),
    status           TEXT NOT NULL DEFAULT 'pending'
                     CHECK (status IN ('pending', 'approved', 'rejected', 'cancelled', 'expired')),
    decided_by       TEXT,
    decided_at       TIMESTAMPTZ,
    decision_note    TEXT,
    approved_minutes INTEGER CHECK (approved_minutes > 0),
    break_glass_id   BIGINT REFERENCES break_glass (id),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT break_glass_requests_decision CHECK ((status = 'pending') = (decided_at IS NULL)),
    CONSTRAINT break_glass_requests_approved CHECK (
        (status = 'approved') = (break_glass_id IS NOT NULL AND approved_minutes IS NOT NULL)
    )
);
-- At most one pending request per user.
CREATE UNIQUE INDEX break_glass_requests_pending_user_idx ON break_glass_requests (user_id) WHERE status = 'pending';
CREATE INDEX break_glass_requests_user_idx ON break_glass_requests (user_id, id DESC);
CREATE INDEX break_glass_requests_pending_idx ON break_glass_requests (created_at) WHERE status = 'pending';

-- +goose Down
DROP TABLE break_glass_requests;
