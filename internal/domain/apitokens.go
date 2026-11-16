package domain

import (
	"slices"
	"time"
)

// Permission is one thing an API token may do. Tokens are service accounts
// for configuration only: they never use grants, never reach a cluster and
// cannot manage tokens.
type Permission string

const (
	PermTimeWindowsRead   Permission = "time_windows:read"
	PermTimeWindowsCreate Permission = "time_windows:create"
	PermTimeWindowsUpdate Permission = "time_windows:update"
	PermTimeWindowsDelete Permission = "time_windows:delete"
	PermHolidaysRead      Permission = "holidays:read"
	PermHolidaysCreate    Permission = "holidays:create"
	PermHolidaysUpdate    Permission = "holidays:update"
	PermHolidaysDelete    Permission = "holidays:delete"
	PermSettingsRead      Permission = "settings:read"
	// Settings are written per section (PATCH /admin/settings).
	PermSettingsBreakGlassWrite    Permission = "settings.break_glass:write"
	PermSettingsExecWrite          Permission = "settings.exec:write"
	PermSettingsRetentionWrite     Permission = "settings.retention:write"
	PermSettingsNotificationsWrite Permission = "settings.notifications:write"
)

// AllPermissions is the catalog, in display order.
var AllPermissions = []Permission{
	PermTimeWindowsRead, PermTimeWindowsCreate, PermTimeWindowsUpdate, PermTimeWindowsDelete,
	PermHolidaysRead, PermHolidaysCreate, PermHolidaysUpdate, PermHolidaysDelete,
	PermSettingsRead, PermSettingsBreakGlassWrite, PermSettingsExecWrite, PermSettingsRetentionWrite, PermSettingsNotificationsWrite,
}

// Valid reports whether p is in the catalog.
func (p Permission) Valid() bool { return slices.Contains(AllPermissions, p) }

// APIToken is a service-account token. Only the sha256 of its value is
// stored; the value is shown once, when it is created.
type APIToken struct {
	ID          int64
	Name        string
	Description string
	// Prefix is the start of the value, so a token can be recognized.
	Prefix      string
	Permissions []Permission
	CreatedBy   string
	CreatedAt   time.Time
	ExpiresAt   *time.Time // nil: never
	LastUsedAt  *time.Time
	RevokedAt   *time.Time
	RevokedBy   string
}

// Has reports whether the token carries the permission.
func (t APIToken) Has(p Permission) bool { return slices.Contains(t.Permissions, p) }

// Active reports whether the token may be used at now.
func (t APIToken) Active(now time.Time) bool {
	return t.RevokedAt == nil && (t.ExpiresAt == nil || now.Before(*t.ExpiresAt))
}
