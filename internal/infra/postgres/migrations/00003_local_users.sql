-- +goose Up
-- Local users: a bcrypt hash instead of a Keycloak identity (LOCAL_USERS_ENABLED).
-- Nullable, so the previous release keeps working while both run; the check
-- also stops it from binding a Keycloak sub to a local user.
ALTER TABLE users ADD COLUMN password_hash TEXT;
ALTER TABLE users ADD CONSTRAINT users_local_or_keycloak
    CHECK (keycloak_sub IS NULL OR password_hash IS NULL);

-- +goose Down
ALTER TABLE users DROP CONSTRAINT users_local_or_keycloak;
ALTER TABLE users DROP COLUMN password_hash;
