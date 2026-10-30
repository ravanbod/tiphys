package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"

	"tiphys/internal/application"
	"tiphys/internal/domain"
)

type userRepo struct{ q dbtx }

var _ application.UserRepository = userRepo{}

const userCols = `id, COALESCE(keycloak_sub, ''), password_hash IS NOT NULL, username, display_name, role, disabled,
	last_login_at, created_at, updated_at, updated_by`

func scanUser(row pgx.CollectableRow) (domain.User, error) {
	var u domain.User
	var role string
	err := row.Scan(&u.ID, &u.KeycloakSub, &u.Local, &u.Username, &u.DisplayName, &role, &u.Disabled,
		&u.LastLoginAt, &u.CreatedAt, &u.UpdatedAt, &u.UpdatedBy)
	u.Role = domain.Role(role)
	return u, err
}

func (r userRepo) one(ctx context.Context, what, where string, arg any) (domain.User, error) {
	rows, err := r.q.Query(ctx, `SELECT `+userCols+` FROM users WHERE `+where, arg)
	if err != nil {
		return domain.User{}, mapErr(err, what)
	}
	u, err := pgx.CollectExactlyOneRow(rows, scanUser)
	return u, mapErr(err, what)
}

func (r userRepo) Get(ctx context.Context, id int64) (domain.User, error) {
	return r.one(ctx, "get user", "id = $1", id)
}

// GetByUsername compares case-insensitively, matching the unique index.
func (r userRepo) GetByUsername(ctx context.Context, username string) (domain.User, error) {
	return r.one(ctx, "get user by username", "lower(username) = lower($1)", username)
}

func (r userRepo) GetByKeycloakSub(ctx context.Context, sub string) (domain.User, error) {
	return r.one(ctx, "get user by keycloak sub", "keycloak_sub = $1", sub)
}

func (r userRepo) List(ctx context.Context) ([]domain.User, error) {
	rows, err := r.q.Query(ctx, `SELECT `+userCols+` FROM users ORDER BY lower(username), id`)
	if err != nil {
		return nil, mapErr(err, "list users")
	}
	us, err := pgx.CollectRows(rows, scanUser)
	return us, mapErr(err, "list users")
}

func (r userRepo) Create(ctx context.Context, u domain.User) (domain.User, error) {
	if u.Role == "" {
		u.Role = domain.RoleUser
	}
	rows, err := r.q.Query(ctx, `
		INSERT INTO users (keycloak_sub, username, display_name, role, disabled, last_login_at, updated_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING `+userCols,
		nullStr(u.KeycloakSub), u.Username, u.DisplayName, string(u.Role), u.Disabled, u.LastLoginAt, u.UpdatedBy)
	if err != nil {
		return domain.User{}, mapErr(err, "create user")
	}
	created, err := pgx.CollectExactlyOneRow(rows, scanUser)
	return created, mapErr(err, "create user")
}

func (r userRepo) Update(ctx context.Context, u domain.User) error {
	tag, err := r.q.Exec(ctx, `
		UPDATE users SET keycloak_sub = $2, username = $3, display_name = $4, role = $5,
			disabled = $6, last_login_at = $7, updated_at = now(), updated_by = $8
		WHERE id = $1`,
		u.ID, nullStr(u.KeycloakSub), u.Username, u.DisplayName, string(u.Role),
		u.Disabled, u.LastLoginAt, u.UpdatedBy)
	return affected(tag, err, "update user")
}

func (r userRepo) CountActiveAdmins(ctx context.Context) (int, error) {
	var n int
	err := r.q.QueryRow(ctx, `SELECT count(*) FROM users WHERE role = 'admin' AND NOT disabled`).Scan(&n)
	return n, mapErr(err, "count active admins")
}

// SetPasswordHash sets a local user's password hash; a user bound to Keycloak
// is not found.
func (r userRepo) SetPasswordHash(ctx context.Context, id int64, hash, updatedBy string) error {
	tag, err := r.q.Exec(ctx, `
		UPDATE users SET password_hash = $2, updated_at = now(), updated_by = $3
		WHERE id = $1 AND keycloak_sub IS NULL`, id, hash, updatedBy)
	return affected(tag, err, "set password hash")
}

func (r userRepo) PasswordHash(ctx context.Context, id int64) (string, error) {
	var hash string
	err := r.q.QueryRow(ctx, `SELECT COALESCE(password_hash, '') FROM users WHERE id = $1`, id).Scan(&hash)
	return hash, mapErr(err, "get password hash")
}
