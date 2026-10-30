package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"tiphys/internal/application"
)

type webSessionRepo struct{ q dbtx }

var _ application.WebSessionRepository = webSessionRepo{}

func (r webSessionRepo) Create(ctx context.Context, s application.WebSession) error {
	now := time.Now()
	if s.CreatedAt.IsZero() {
		s.CreatedAt = now
	}
	if s.LastRefreshAt.IsZero() {
		s.LastRefreshAt = s.CreatedAt
	}
	_, err := r.q.Exec(ctx, `
		INSERT INTO web_sessions (id, user_id, csrf_token, access_token, refresh_token, id_token,
			created_at, last_refresh_at, expires_at, source_ip, user_agent)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		s.ID, s.UserID, s.CSRFToken, s.AccessToken, s.RefreshToken, s.IDToken,
		s.CreatedAt, s.LastRefreshAt, s.ExpiresAt, s.SourceIP, s.UserAgent)
	return mapErr(err, "create web session")
}

const webSessionCols = `id, user_id, csrf_token, access_token, refresh_token, id_token, created_at,
	last_refresh_at, expires_at, source_ip, user_agent`

func scanWebSession(row pgx.CollectableRow) (application.WebSession, error) {
	var s application.WebSession
	err := row.Scan(&s.ID, &s.UserID, &s.CSRFToken, &s.AccessToken, &s.RefreshToken, &s.IDToken,
		&s.CreatedAt, &s.LastRefreshAt, &s.ExpiresAt, &s.SourceIP, &s.UserAgent)
	return s, err
}

func (r webSessionRepo) Get(ctx context.Context, id string) (application.WebSession, error) {
	rows, err := r.q.Query(ctx, `SELECT `+webSessionCols+` FROM web_sessions WHERE id = $1`, id)
	if err != nil {
		return application.WebSession{}, mapErr(err, "get web session")
	}
	s, err := pgx.CollectExactlyOneRow(rows, scanWebSession)
	return s, mapErr(err, "get web session")
}

// ListRefreshDue returns unexpired sessions last refreshed before the cutoff,
// oldest first.
func (r webSessionRepo) ListRefreshDue(ctx context.Context, before time.Time) ([]application.WebSession, error) {
	rows, err := r.q.Query(ctx, `
		SELECT `+webSessionCols+` FROM web_sessions
		WHERE last_refresh_at < $1 AND expires_at > now()
		ORDER BY last_refresh_at`, before)
	if err != nil {
		return nil, mapErr(err, "list web sessions due for refresh")
	}
	ss, err := pgx.CollectRows(rows, scanWebSession)
	return ss, mapErr(err, "list web sessions due for refresh")
}

func (r webSessionRepo) UpdateTokens(ctx context.Context, id string, accessToken, refreshToken, idToken string, refreshedAt, expiresAt time.Time) error {
	tag, err := r.q.Exec(ctx, `
		UPDATE web_sessions SET access_token = $2, refresh_token = $3, id_token = $4,
			last_refresh_at = $5, expires_at = $6
		WHERE id = $1`, id, accessToken, refreshToken, idToken, refreshedAt, expiresAt)
	return affected(tag, err, "update web session tokens")
}

// Delete is idempotent: a missing session is not an error.
func (r webSessionRepo) Delete(ctx context.Context, id string) error {
	_, err := r.q.Exec(ctx, `DELETE FROM web_sessions WHERE id = $1`, id)
	return mapErr(err, "delete web session")
}

func (r webSessionRepo) DeleteByUser(ctx context.Context, userID int64) (int64, error) {
	tag, err := r.q.Exec(ctx, `DELETE FROM web_sessions WHERE user_id = $1`, userID)
	return tag.RowsAffected(), mapErr(err, "delete web sessions of user")
}

func (r webSessionRepo) DeleteExpired(ctx context.Context, before time.Time) (int64, error) {
	tag, err := r.q.Exec(ctx, `DELETE FROM web_sessions WHERE expires_at < $1`, before)
	return tag.RowsAffected(), mapErr(err, "delete expired web sessions")
}
