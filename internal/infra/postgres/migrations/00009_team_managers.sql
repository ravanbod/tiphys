-- +goose Up
-- Team managers: users an admin chose to add and remove the members of a
-- team themselves. A table of its own, so the previous release, which never
-- reads it, keeps working while both run.
CREATE TABLE team_managers (
    team_id    BIGINT NOT NULL REFERENCES teams (id) ON DELETE CASCADE,
    user_id    BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, user_id)
);
CREATE INDEX team_managers_user_idx ON team_managers (user_id);

-- +goose Down
DROP TABLE team_managers;
