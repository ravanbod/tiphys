package application

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"go.uber.org/zap"

	"tiphys/internal/domain"
)

const (
	// APITokenPrefix starts every token value, so leaked tokens are easy to
	// recognize (and scanners can look for them).
	APITokenPrefix = "tph_"
	// apiTokenShownPrefix is how much of the value is stored and shown.
	apiTokenShownPrefix = len(APITokenPrefix) + 8
	// apiTokenTouchInterval throttles the last-used update.
	apiTokenTouchInterval = time.Minute
	maxAPITokenNameLength = 64
)

// AuthenticateToken resolves an API token value to a Principal. The token's
// User is a name only ("token:<name>"), so it passes no role check.
func (s *AuthService) AuthenticateToken(ctx context.Context, value, ip, userAgent string) (Principal, error) {
	if !strings.HasPrefix(value, APITokenPrefix) {
		return Principal{}, ErrUnauthenticated
	}
	t, err := s.store.Repos().APITokens.GetByHash(ctx, SessionID(value))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Principal{}, ErrUnauthenticated
		}
		return Principal{}, err
	}
	now := s.clock.Now()
	if !t.Active(now) {
		return Principal{}, ErrUnauthenticated
	}
	if t.LastUsedAt == nil || now.Sub(*t.LastUsedAt) >= apiTokenTouchInterval {
		if err := s.store.Repos().APITokens.TouchLastUsed(ctx, t.ID, now); err != nil {
			s.logger.Warn("api token last-used update failed", zap.Int64("token_id", t.ID), zap.Error(err))
		}
	}
	return Principal{User: domain.User{Username: "token:" + t.Name}, Token: &t, SourceIP: ip, UserAgent: userAgent}, nil
}

// ---- API tokens (admin) ----

// APITokens lists every token, revoked ones included (admin and auditor).
func (s *AdminService) APITokens(ctx context.Context, p Principal) ([]domain.APIToken, error) {
	if err := RequireRole(p, domain.RoleAdmin, domain.RoleAuditor); err != nil {
		return nil, err
	}
	return s.store.Repos().APITokens.List(ctx)
}

// CreateAPIToken creates a token and returns it with its value, which is
// never shown again. Only an admin's session may create one, never a token.
func (s *AdminService) CreateAPIToken(ctx context.Context, p Principal, t domain.APIToken) (domain.APIToken, string, error) {
	if err := s.requireAdmin(p); err != nil {
		return t, "", err
	}
	t.Name = strings.TrimSpace(t.Name)
	t.Description = strings.TrimSpace(t.Description)
	if t.Name == "" || len(t.Name) > maxAPITokenNameLength {
		return t, "", Invalid("name", "a name of at most "+itoa(maxAPITokenNameLength)+" characters is required")
	}
	if len(t.Permissions) == 0 {
		return t, "", Invalid("permissions", "choose at least one permission")
	}
	for _, perm := range t.Permissions {
		if !perm.Valid() {
			return t, "", Invalid("permissions", "unknown permission "+string(perm))
		}
	}
	// Catalog order, without duplicates.
	perms := make([]domain.Permission, 0, len(t.Permissions))
	for _, perm := range domain.AllPermissions {
		if slices.Contains(t.Permissions, perm) {
			perms = append(perms, perm)
		}
	}
	t.Permissions = perms
	now := s.clock.Now()
	if t.ExpiresAt != nil && !t.ExpiresAt.After(now) {
		return t, "", Invalid("expiresAt", "must be in the future")
	}
	value := APITokenPrefix + randomToken(32)
	t.Prefix = value[:apiTokenShownPrefix]
	t.CreatedBy = p.User.Username
	c := &change{entity: "api_token", operation: "create"}
	err := s.mutate(ctx, p, c, func(r Repositories) error {
		var err error
		if t, err = r.APITokens.Create(ctx, t, SessionID(value)); err != nil {
			return err
		}
		c.id, c.after = t.ID, auditToken(t)
		return nil
	})
	if errors.Is(err, ErrConflict) {
		return t, "", Invalid("name", "a token with this name already exists; revoke it first")
	}
	if err != nil {
		return t, "", err
	}
	return t, value, nil
}

// RevokeAPIToken stops a token at once. A revoked token stays listed.
func (s *AdminService) RevokeAPIToken(ctx context.Context, p Principal, id int64) error {
	if err := s.requireAdmin(p); err != nil {
		return err
	}
	c := &change{entity: "api_token", operation: "revoke", id: id}
	return s.mutate(ctx, p, c, func(r Repositories) error {
		before, err := r.APITokens.Get(ctx, id)
		if err != nil {
			return err
		}
		if before.RevokedAt != nil {
			return fmt.Errorf("%w: the token is already revoked", ErrConflict)
		}
		c.before = auditToken(before)
		return r.APITokens.Revoke(ctx, id, s.clock.Now(), p.User.Username)
	})
}

// auditToken is what the audit log keeps of a token.
func auditToken(t domain.APIToken) map[string]any {
	return map[string]any{"name": t.Name, "description": t.Description, "prefix": t.Prefix,
		"permissions": t.Permissions, "expiresAt": t.ExpiresAt}
}
