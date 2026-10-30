package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"tiphys/internal/application"
	"tiphys/internal/domain"
)

type holidayRepo struct{ q dbtx }

var _ application.HolidayRepository = holidayRepo{}

const holidayCols = `id, date, title, environments, created_at`

func scanHoliday(row pgx.CollectableRow) (domain.Holiday, error) {
	var h domain.Holiday
	err := row.Scan(&h.ID, &h.Date, &h.Title, &h.Environments, &h.CreatedAt)
	return h, err
}

// dateOnly keeps the calendar day the caller wrote, whatever the location of
// the time.Time, so it is not shifted by a timezone conversion.
func dateOnly(t time.Time) pgtype.Date {
	y, m, d := t.Date()
	return pgtype.Date{Time: time.Date(y, m, d, 0, 0, 0, 0, time.UTC), Valid: true}
}

func envs(e []string) []string {
	if e == nil {
		return []string{}
	}
	return e
}

func (r holidayRepo) Get(ctx context.Context, id int64) (domain.Holiday, error) {
	rows, err := r.q.Query(ctx, `SELECT `+holidayCols+` FROM holidays WHERE id = $1`, id)
	if err != nil {
		return domain.Holiday{}, mapErr(err, "get holiday")
	}
	h, err := pgx.CollectExactlyOneRow(rows, scanHoliday)
	return h, mapErr(err, "get holiday")
}

func (r holidayRepo) List(ctx context.Context) ([]domain.Holiday, error) {
	rows, err := r.q.Query(ctx, `SELECT `+holidayCols+` FROM holidays ORDER BY date, id`)
	if err != nil {
		return nil, mapErr(err, "list holidays")
	}
	hs, err := pgx.CollectRows(rows, scanHoliday)
	return hs, mapErr(err, "list holidays")
}

func (r holidayRepo) Create(ctx context.Context, h domain.Holiday) (domain.Holiday, error) {
	rows, err := r.q.Query(ctx, `
		INSERT INTO holidays (date, title, environments) VALUES ($1, $2, $3)
		RETURNING `+holidayCols, dateOnly(h.Date), h.Title, envs(h.Environments))
	if err != nil {
		return domain.Holiday{}, mapErr(err, "create holiday")
	}
	created, err := pgx.CollectExactlyOneRow(rows, scanHoliday)
	return created, mapErr(err, "create holiday")
}

func (r holidayRepo) Update(ctx context.Context, h domain.Holiday) error {
	tag, err := r.q.Exec(ctx, `
		UPDATE holidays SET date = $2, title = $3, environments = $4 WHERE id = $1`,
		h.ID, dateOnly(h.Date), h.Title, envs(h.Environments))
	return affected(tag, err, "update holiday")
}

func (r holidayRepo) Delete(ctx context.Context, id int64) error {
	tag, err := r.q.Exec(ctx, `DELETE FROM holidays WHERE id = $1`, id)
	return affected(tag, err, "delete holiday")
}
