-- +goose Up
-- The sync action (sync a whole Argo CD application). Only widens the allowed
-- actions, so the previous release keeps working while both run.
ALTER TABLE grants DROP CONSTRAINT grants_actions;
ALTER TABLE grants ADD CONSTRAINT grants_actions CHECK (
    cardinality(actions) > 0 AND actions <@ ARRAY['logs', 'exec', 'restart', 'run-job', 'sync']::text[]
);
ALTER TABLE time_windows DROP CONSTRAINT time_windows_actions;
ALTER TABLE time_windows ADD CONSTRAINT time_windows_actions CHECK (
    cardinality(actions) > 0 AND actions <@ ARRAY['logs', 'exec', 'restart', 'run-job', 'sync']::text[]
);
ALTER TABLE access_requests DROP CONSTRAINT access_requests_actions;
ALTER TABLE access_requests ADD CONSTRAINT access_requests_actions CHECK (
    cardinality(actions) > 0 AND actions <@ ARRAY['logs', 'exec', 'restart', 'run-job', 'sync']::text[]
);

-- +goose Down
-- Fails while any row still holds sync; remove it from those rows first.
ALTER TABLE grants DROP CONSTRAINT grants_actions;
ALTER TABLE grants ADD CONSTRAINT grants_actions CHECK (
    cardinality(actions) > 0 AND actions <@ ARRAY['logs', 'exec', 'restart', 'run-job']::text[]
);
ALTER TABLE time_windows DROP CONSTRAINT time_windows_actions;
ALTER TABLE time_windows ADD CONSTRAINT time_windows_actions CHECK (
    cardinality(actions) > 0 AND actions <@ ARRAY['logs', 'exec', 'restart', 'run-job']::text[]
);
ALTER TABLE access_requests DROP CONSTRAINT access_requests_actions;
ALTER TABLE access_requests ADD CONSTRAINT access_requests_actions CHECK (
    cardinality(actions) > 0 AND actions <@ ARRAY['logs', 'exec', 'restart', 'run-job']::text[]
);
