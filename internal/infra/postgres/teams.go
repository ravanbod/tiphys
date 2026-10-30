package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"

	"tiphys/internal/application"
	"tiphys/internal/domain"
)

type teamRepo struct{ q dbtx }

var _ application.TeamRepository = teamRepo{}

const teamCols = `id, name, description, created_at, updated_at, updated_by`

func scanTeam(row pgx.CollectableRow) (domain.Team, error) {
	var t domain.Team
	err := row.Scan(&t.ID, &t.Name, &t.Description, &t.CreatedAt, &t.UpdatedAt, &t.UpdatedBy)
	return t, err
}

// fillMembers sets MemberIDs (ascending, never nil) on every team.
func (r teamRepo) fillMembers(ctx context.Context, teams []domain.Team) error {
	if len(teams) == 0 {
		return nil
	}
	rows, err := r.q.Query(ctx, `SELECT team_id, user_id FROM team_members ORDER BY team_id, user_id`)
	if err != nil {
		return mapErr(err, "list team members")
	}
	defer rows.Close()
	byTeam := map[int64][]int64{}
	for rows.Next() {
		var tid, uid int64
		if err := rows.Scan(&tid, &uid); err != nil {
			return mapErr(err, "list team members")
		}
		byTeam[tid] = append(byTeam[tid], uid)
	}
	if err := rows.Err(); err != nil {
		return mapErr(err, "list team members")
	}
	for i := range teams {
		teams[i].MemberIDs = byTeam[teams[i].ID]
		if teams[i].MemberIDs == nil {
			teams[i].MemberIDs = []int64{}
		}
	}
	return nil
}

func (r teamRepo) Get(ctx context.Context, id int64) (domain.Team, error) {
	rows, err := r.q.Query(ctx, `SELECT `+teamCols+` FROM teams WHERE id = $1`, id)
	if err != nil {
		return domain.Team{}, mapErr(err, "get team")
	}
	t, err := pgx.CollectExactlyOneRow(rows, scanTeam)
	if err != nil {
		return domain.Team{}, mapErr(err, "get team")
	}
	ts := []domain.Team{t}
	if err := r.fillMembers(ctx, ts); err != nil {
		return domain.Team{}, err
	}
	return ts[0], nil
}

func (r teamRepo) List(ctx context.Context) ([]domain.Team, error) {
	rows, err := r.q.Query(ctx, `SELECT `+teamCols+` FROM teams ORDER BY lower(name), id`)
	if err != nil {
		return nil, mapErr(err, "list teams")
	}
	ts, err := pgx.CollectRows(rows, scanTeam)
	if err != nil {
		return nil, mapErr(err, "list teams")
	}
	if err := r.fillMembers(ctx, ts); err != nil {
		return nil, err
	}
	return ts, nil
}

func (r teamRepo) Create(ctx context.Context, t domain.Team) (domain.Team, error) {
	rows, err := r.q.Query(ctx, `
		INSERT INTO teams (name, description, updated_by) VALUES ($1, $2, $3)
		RETURNING `+teamCols, t.Name, t.Description, t.UpdatedBy)
	if err != nil {
		return domain.Team{}, mapErr(err, "create team")
	}
	created, err := pgx.CollectExactlyOneRow(rows, scanTeam)
	if err != nil {
		return domain.Team{}, mapErr(err, "create team")
	}
	created.MemberIDs = []int64{}
	return created, nil
}

// Update also renames the scope value of the team's time windows, which keep
// the team name.
func (r teamRepo) Update(ctx context.Context, t domain.Team) error {
	return withTx(ctx, r.q, func(tx dbtx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE teams SET name = $2, description = $3, updated_at = now(), updated_by = $4
			WHERE id = $1`, t.ID, t.Name, t.Description, t.UpdatedBy)
		if err := affected(tag, err, "update team"); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE time_windows SET scope_value = $2 WHERE team_id = $1`, t.ID, t.Name)
		return mapErr(err, "update team windows")
	})
}

// Delete cascades to the team's members, grants and time windows.
func (r teamRepo) Delete(ctx context.Context, id int64) error {
	tag, err := r.q.Exec(ctx, `DELETE FROM teams WHERE id = $1`, id)
	return affected(tag, err, "delete team")
}

// SetMembers replaces the membership. Unknown user ids fail with ErrConflict.
func (r teamRepo) SetMembers(ctx context.Context, teamID int64, userIDs []int64) error {
	if userIDs == nil {
		userIDs = []int64{}
	}
	return withTx(ctx, r.q, func(tx dbtx) error {
		var id int64
		// Lock the team row so concurrent SetMembers calls serialize.
		if err := tx.QueryRow(ctx, `SELECT id FROM teams WHERE id = $1 FOR UPDATE`, teamID).Scan(&id); err != nil {
			return mapErr(err, "set team members")
		}
		if _, err := tx.Exec(ctx, `
			DELETE FROM team_members WHERE team_id = $1 AND NOT (user_id = ANY($2))`,
			teamID, userIDs); err != nil {
			return mapErr(err, "set team members")
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO team_members (team_id, user_id)
			SELECT $1, u FROM unnest($2::bigint[]) AS u
			ON CONFLICT DO NOTHING`, teamID, userIDs)
		return mapErr(err, "set team members")
	})
}

func (r teamRepo) TeamIDsOfUser(ctx context.Context, userID int64) ([]int64, error) {
	rows, err := r.q.Query(ctx, `SELECT team_id FROM team_members WHERE user_id = $1 ORDER BY team_id`, userID)
	if err != nil {
		return nil, mapErr(err, "team ids of user")
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	return ids, mapErr(err, "team ids of user")
}
