package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"tiphys/internal/application"
	"tiphys/internal/domain"
)

type windowRepo struct{ q dbtx }

var _ application.WindowRepository = windowRepo{}

// For team windows scope_value is the team name; it is read from the join so
// a renamed team is always current.
const windowSelect = `
	SELECT w.id, w.name, w.scope_kind, COALESCE(t.name, w.scope_value, ''), COALESCE(w.team_id, 0),
		w.actions, w.days_of_week, w.start_time, w.end_time, w.timezone, w.effect, w.enabled,
		w.created_at, w.updated_at, w.updated_by
	FROM time_windows w LEFT JOIN teams t ON t.id = w.team_id`

func toPGTime(t domain.TimeOfDay) pgtype.Time {
	return pgtype.Time{Microseconds: int64(t) * int64(time.Minute/time.Microsecond), Valid: true}
}

func fromPGTime(t pgtype.Time) domain.TimeOfDay {
	return domain.TimeOfDay(t.Microseconds / int64(time.Minute/time.Microsecond))
}

func scanWindow(row pgx.CollectableRow) (domain.TimeWindow, error) {
	var w domain.TimeWindow
	var scope, effect string
	var actions []string
	var days []int16
	var start, end pgtype.Time
	err := row.Scan(&w.ID, &w.Name, &scope, &w.ScopeValue, &w.TeamID, &actions, &days, &start, &end,
		&w.Timezone, &effect, &w.Enabled, &w.CreatedAt, &w.UpdatedAt, &w.UpdatedBy)
	w.ScopeKind = domain.WindowScopeKind(scope)
	w.Effect = domain.WindowEffect(effect)
	w.Actions = actionsFrom(actions)
	w.Days = make([]time.Weekday, len(days))
	for i, d := range days {
		w.Days[i] = time.Weekday(d)
	}
	w.Start, w.End = fromPGTime(start), fromPGTime(end)
	return w, err
}

// windowCols resolves what differs by scope kind: scope_value and team_id.
// A team window names its team (by name, or by TeamID when the name is empty).
func windowCols(ctx context.Context, q dbtx, w domain.TimeWindow) (scopeValue, teamID any, err error) {
	if w.ScopeKind != domain.WindowTeam {
		return nullStr(w.ScopeValue), nil, nil
	}
	var id int64
	var name string
	if w.ScopeValue != "" {
		err = q.QueryRow(ctx, `SELECT id, name FROM teams WHERE name = $1`, w.ScopeValue).Scan(&id, &name)
	} else {
		err = q.QueryRow(ctx, `SELECT id, name FROM teams WHERE id = $1`, w.TeamID).Scan(&id, &name)
	}
	if err != nil {
		return nil, nil, mapErr(err, fmt.Sprintf("team window scope %q", w.ScopeValue))
	}
	return name, id, nil
}

func windowArgs(w domain.TimeWindow, scopeValue, teamID any) []any {
	days := make([]int16, len(w.Days))
	for i, d := range w.Days {
		days[i] = int16(d)
	}
	return []any{w.Name, string(w.ScopeKind), scopeValue, teamID, actionStrings(w.Actions), days,
		toPGTime(w.Start), toPGTime(w.End), w.Timezone, string(w.Effect), w.Enabled, w.UpdatedBy}
}

func (r windowRepo) Get(ctx context.Context, id int64) (domain.TimeWindow, error) {
	rows, err := r.q.Query(ctx, windowSelect+` WHERE w.id = $1`, id)
	if err != nil {
		return domain.TimeWindow{}, mapErr(err, "get window")
	}
	w, err := pgx.CollectExactlyOneRow(rows, scanWindow)
	return w, mapErr(err, "get window")
}

func (r windowRepo) List(ctx context.Context) ([]domain.TimeWindow, error) {
	rows, err := r.q.Query(ctx, windowSelect+` ORDER BY w.id`)
	if err != nil {
		return nil, mapErr(err, "list windows")
	}
	ws, err := pgx.CollectRows(rows, scanWindow)
	return ws, mapErr(err, "list windows")
}

func (r windowRepo) Create(ctx context.Context, w domain.TimeWindow) (domain.TimeWindow, error) {
	if w.Timezone == "" {
		w.Timezone = domain.DefaultTimezone
	}
	var id int64
	err := withTx(ctx, r.q, func(tx dbtx) error {
		sv, tid, err := windowCols(ctx, tx, w)
		if err != nil {
			return err
		}
		err = tx.QueryRow(ctx, `
			INSERT INTO time_windows (name, scope_kind, scope_value, team_id, actions, days_of_week,
				start_time, end_time, timezone, effect, enabled, updated_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
			RETURNING id`, windowArgs(w, sv, tid)...).Scan(&id)
		return mapErr(err, "create window")
	})
	if err != nil {
		return domain.TimeWindow{}, err
	}
	return r.Get(ctx, id)
}

func (r windowRepo) Update(ctx context.Context, w domain.TimeWindow) error {
	if w.Timezone == "" {
		w.Timezone = domain.DefaultTimezone
	}
	return withTx(ctx, r.q, func(tx dbtx) error {
		sv, tid, err := windowCols(ctx, tx, w)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
			UPDATE time_windows SET name = $2, scope_kind = $3, scope_value = $4, team_id = $5,
				actions = $6, days_of_week = $7, start_time = $8, end_time = $9, timezone = $10,
				effect = $11, enabled = $12, updated_by = $13, updated_at = now()
			WHERE id = $1`, append([]any{w.ID}, windowArgs(w, sv, tid)...)...)
		return affected(tag, err, "update window")
	})
}

func (r windowRepo) Delete(ctx context.Context, id int64) error {
	tag, err := r.q.Exec(ctx, `DELETE FROM time_windows WHERE id = $1`, id)
	return affected(tag, err, "delete window")
}
