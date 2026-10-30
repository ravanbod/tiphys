package postgres

import (
	"context"
	"errors"
	"testing"

	"tiphys/internal/application"
	"tiphys/internal/domain"
)

func TestStorePing(t *testing.T) {
	f := newFixture(t)
	must(t, f.store.Ping(f.ctx))
}

func TestOpenBadDSN(t *testing.T) {
	if _, err := Open(context.Background(), Config{DSN: "::not a dsn"}); err == nil {
		t.Fatal("want error")
	}
}

func TestInTx(t *testing.T) {
	f := newFixture(t)

	err := f.store.InTx(f.ctx, func(r application.Repositories) error {
		u, err := r.Users.Create(f.ctx, domain.User{Username: "committed", Role: domain.RoleUser})
		if err != nil {
			return err
		}
		// Visible inside the transaction, not outside yet.
		if _, err := r.Users.Get(f.ctx, u.ID); err != nil {
			return err
		}
		if _, err := f.r.Users.Get(f.ctx, u.ID); !errors.Is(err, application.ErrNotFound) {
			t.Errorf("uncommitted row visible outside the transaction: %v", err)
		}
		return nil
	})
	must(t, err)
	if _, err := f.r.Users.GetByUsername(f.ctx, "committed"); err != nil {
		t.Fatalf("committed row missing: %v", err)
	}

	boom := errors.New("boom")
	err = f.store.InTx(f.ctx, func(r application.Repositories) error {
		if _, err := r.Users.Create(f.ctx, domain.User{Username: "rolledback", Role: domain.RoleUser}); err != nil {
			return err
		}
		if _, err := r.Audit.Insert(f.ctx, domain.AuditEvent{EventType: "x", Result: domain.ResultAllowed}); err != nil {
			return err
		}
		return boom
	})
	isErr(t, err, boom)
	_, err = f.r.Users.GetByUsername(f.ctx, "rolledback")
	isErr(t, err, application.ErrNotFound)
	es, _ := f.r.Audit.List(f.ctx, application.AuditFilter{})
	if len(es) != 0 {
		t.Fatalf("audit event survived rollback: %d", len(es))
	}

	// A statement error inside the tx (unique violation) maps to ErrConflict
	// and the surrounding InTx call returns it after rolling back.
	err = f.store.InTx(f.ctx, func(r application.Repositories) error {
		if _, err := r.Users.Create(f.ctx, domain.User{Username: "dup", Role: domain.RoleUser}); err != nil {
			return err
		}
		_, err := r.Users.Create(f.ctx, domain.User{Username: "DUP", Role: domain.RoleUser})
		return err
	})
	isErr(t, err, application.ErrConflict)
	_, err = f.r.Users.GetByUsername(f.ctx, "dup")
	isErr(t, err, application.ErrNotFound)

	// Multi-statement repository methods work inside a transaction (savepoint),
	// and a failure inside one does not poison the outer transaction.
	err = f.store.InTx(f.ctx, func(r application.Repositories) error {
		tm, err := r.Teams.Create(f.ctx, domain.Team{Name: "t"})
		if err != nil {
			return err
		}
		if err := r.Teams.SetMembers(f.ctx, tm.ID, []int64{9999}); !errors.Is(err, application.ErrConflict) {
			t.Errorf("want conflict, got %v", err)
		}
		return r.Teams.SetMembers(f.ctx, tm.ID, nil)
	})
	must(t, err)
}
