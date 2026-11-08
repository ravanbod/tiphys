-- +goose Up
-- Access requests a team manager files for their team: approving one grants
-- the team, not the requester (user_id stays the manager who asked).
-- Deleting the team removes its requests, so one never turns into a request
-- of the manager's own.
--
-- The previous release, which keeps running during a rollout, reads these
-- rows as ordinary requests of the manager and would grant the manager on
-- approval. It never sets approved_for_team, so the CHECK makes such an
-- approval fail instead.
ALTER TABLE access_requests ADD COLUMN team_id BIGINT REFERENCES teams (id) ON DELETE CASCADE;
ALTER TABLE access_requests ADD COLUMN approved_for_team BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE access_requests ADD CONSTRAINT access_requests_team_approval CHECK (
    team_id IS NULL OR status <> 'approved' OR approved_for_team
);
CREATE INDEX access_requests_team_idx ON access_requests (team_id, id DESC) WHERE team_id IS NOT NULL;

-- +goose Down
DELETE FROM access_requests WHERE team_id IS NOT NULL;
ALTER TABLE access_requests DROP COLUMN approved_for_team;
ALTER TABLE access_requests DROP COLUMN team_id;
