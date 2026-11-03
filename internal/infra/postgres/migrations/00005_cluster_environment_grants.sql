-- +goose Up
-- Grants on a whole cluster or environment. Only widens what grants may hold,
-- so the previous release keeps working while both run (it matches no target
-- with the new kinds). Access requests keep their scopes: these are admin-only.
ALTER TABLE grants ADD COLUMN environment TEXT;
ALTER TABLE grants DROP CONSTRAINT grants_scope_kind_check;
ALTER TABLE grants ADD CONSTRAINT grants_scope_kind_check CHECK (
    scope_kind IN ('argo_project', 'namespace', 'workloads', 'cluster', 'environment')
);
ALTER TABLE grants DROP CONSTRAINT grants_scope;
ALTER TABLE grants ADD CONSTRAINT grants_scope CHECK (
    (scope_kind = 'argo_project' AND argo_project IS NOT NULL AND argo_project <> ''
        AND cluster_id IS NULL AND namespace IS NULL AND environment IS NULL)
    OR
    (scope_kind IN ('namespace', 'workloads') AND argo_project IS NULL AND environment IS NULL
        AND cluster_id IS NOT NULL AND namespace IS NOT NULL AND namespace <> '')
    OR
    (scope_kind = 'cluster' AND argo_project IS NULL AND environment IS NULL
        AND cluster_id IS NOT NULL AND namespace IS NULL)
    OR
    (scope_kind = 'environment' AND environment IS NOT NULL AND environment <> ''
        AND argo_project IS NULL AND cluster_id IS NULL AND namespace IS NULL)
);

-- +goose Down
-- Fails while any cluster or environment grant exists; delete those first.
ALTER TABLE grants DROP CONSTRAINT grants_scope;
ALTER TABLE grants ADD CONSTRAINT grants_scope CHECK (
    (scope_kind = 'argo_project' AND argo_project IS NOT NULL AND argo_project <> ''
        AND cluster_id IS NULL AND namespace IS NULL)
    OR
    (scope_kind IN ('namespace', 'workloads') AND argo_project IS NULL
        AND cluster_id IS NOT NULL AND namespace IS NOT NULL AND namespace <> '')
);
ALTER TABLE grants DROP CONSTRAINT grants_scope_kind_check;
ALTER TABLE grants ADD CONSTRAINT grants_scope_kind_check CHECK (
    scope_kind IN ('argo_project', 'namespace', 'workloads')
);
ALTER TABLE grants DROP COLUMN environment;
