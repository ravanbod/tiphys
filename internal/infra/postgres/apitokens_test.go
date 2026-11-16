package postgres

import (
	"testing"
	"time"

	"tiphys/internal/application"
	"tiphys/internal/domain"
)

func TestAPITokens(t *testing.T) {
	f := newFixture(t)
	exp := t0.Add(time.Hour)
	tok, err := f.r.APITokens.Create(f.ctx, domain.APIToken{Name: "ci", Prefix: "tph_abcdefgh", CreatedBy: "admin", ExpiresAt: &exp,
		Permissions: []domain.Permission{domain.PermTimeWindowsRead, domain.PermTimeWindowsDelete}}, "hash-1")
	must(t, err)
	if tok.ID == 0 || !tok.Has(domain.PermTimeWindowsDelete) || tok.Has(domain.PermHolidaysRead) || !tok.ExpiresAt.Equal(exp) {
		t.Fatalf("created: %+v", tok)
	}
	// One active token per name (case-insensitive), and hashes are unique.
	_, err = f.r.APITokens.Create(f.ctx, domain.APIToken{Name: "CI", Prefix: "p", CreatedBy: "admin", Permissions: []domain.Permission{}}, "hash-2")
	isErr(t, err, application.ErrConflict)
	_, err = f.r.APITokens.Create(f.ctx, domain.APIToken{Name: "other", Prefix: "p", CreatedBy: "admin", Permissions: []domain.Permission{}}, "hash-1")
	isErr(t, err, application.ErrConflict)

	got, err := f.r.APITokens.GetByHash(f.ctx, "hash-1")
	must(t, err)
	if got.ID != tok.ID || got.LastUsedAt != nil {
		t.Fatalf("by hash: %+v", got)
	}
	_, err = f.r.APITokens.GetByHash(f.ctx, "nope")
	isErr(t, err, application.ErrNotFound)

	must(t, f.r.APITokens.TouchLastUsed(f.ctx, tok.ID, t0))
	must(t, f.r.APITokens.Revoke(f.ctx, tok.ID, t0.Add(time.Minute), "admin"))
	isErr(t, f.r.APITokens.Revoke(f.ctx, tok.ID, t0.Add(time.Minute), "admin"), application.ErrNotFound)
	got, err = f.r.APITokens.Get(f.ctx, tok.ID)
	must(t, err)
	if got.LastUsedAt == nil || !got.LastUsedAt.Equal(t0) || got.RevokedAt == nil || got.RevokedBy != "admin" || got.Active(t0) {
		t.Fatalf("after use and revoke: %+v", got)
	}
	// The name is free again once revoked.
	_, err = f.r.APITokens.Create(f.ctx, domain.APIToken{Name: "ci", Prefix: "p", CreatedBy: "admin", Permissions: []domain.Permission{}}, "hash-3")
	must(t, err)
	list, err := f.r.APITokens.List(f.ctx)
	must(t, err)
	if len(list) != 2 {
		t.Fatalf("list: %+v", list)
	}
}
