package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"tiphys/internal/application"
	"tiphys/internal/domain"
)

type apiTokenRepo struct{ q dbtx }

var _ application.APITokenRepository = apiTokenRepo{}

const apiTokenCols = `id, name, description, prefix, permissions, created_by, created_at, expires_at,
	last_used_at, revoked_at, revoked_by`

func scanAPIToken(row pgx.CollectableRow) (domain.APIToken, error) {
	var t domain.APIToken
	var perms []string
	err := row.Scan(&t.ID, &t.Name, &t.Description, &t.Prefix, &perms, &t.CreatedBy, &t.CreatedAt, &t.ExpiresAt,
		&t.LastUsedAt, &t.RevokedAt, &t.RevokedBy)
	t.Permissions = make([]domain.Permission, len(perms))
	for i, p := range perms {
		t.Permissions[i] = domain.Permission(p)
	}
	return t, err
}

func (r apiTokenRepo) Create(ctx context.Context, t domain.APIToken, hash string) (domain.APIToken, error) {
	perms := make([]string, len(t.Permissions))
	for i, p := range t.Permissions {
		perms[i] = string(p)
	}
	rows, err := r.q.Query(ctx, `
		INSERT INTO api_tokens (name, description, token_hash, prefix, permissions, created_by, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING `+apiTokenCols, t.Name, t.Description, hash, t.Prefix, perms, t.CreatedBy, t.ExpiresAt)
	if err != nil {
		return t, mapErr(err, "create api token")
	}
	out, err := pgx.CollectExactlyOneRow(rows, scanAPIToken)
	return out, mapErr(err, "create api token")
}

func (r apiTokenRepo) get(ctx context.Context, where string, arg any) (domain.APIToken, error) {
	rows, err := r.q.Query(ctx, `SELECT `+apiTokenCols+` FROM api_tokens WHERE `+where, arg)
	if err != nil {
		return domain.APIToken{}, mapErr(err, "get api token")
	}
	t, err := pgx.CollectExactlyOneRow(rows, scanAPIToken)
	return t, mapErr(err, "get api token")
}

func (r apiTokenRepo) Get(ctx context.Context, id int64) (domain.APIToken, error) {
	return r.get(ctx, `id = $1`, id)
}

func (r apiTokenRepo) GetByHash(ctx context.Context, hash string) (domain.APIToken, error) {
	return r.get(ctx, `token_hash = $1`, hash)
}

func (r apiTokenRepo) List(ctx context.Context) ([]domain.APIToken, error) {
	rows, err := r.q.Query(ctx, `SELECT `+apiTokenCols+` FROM api_tokens ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, mapErr(err, "list api tokens")
	}
	ts, err := pgx.CollectRows(rows, scanAPIToken)
	return ts, mapErr(err, "list api tokens")
}

// Revoke refuses (ErrNotFound) a token that is unknown or already revoked.
func (r apiTokenRepo) Revoke(ctx context.Context, id int64, at time.Time, by string) error {
	tag, err := r.q.Exec(ctx, `UPDATE api_tokens SET revoked_at = $2, revoked_by = $3 WHERE id = $1 AND revoked_at IS NULL`, id, at, by)
	return affected(tag, err, "revoke api token")
}

func (r apiTokenRepo) TouchLastUsed(ctx context.Context, id int64, at time.Time) error {
	_, err := r.q.Exec(ctx, `UPDATE api_tokens SET last_used_at = $2 WHERE id = $1`, id, at)
	return mapErr(err, "touch api token")
}
