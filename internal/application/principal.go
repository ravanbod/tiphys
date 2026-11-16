package application

import "tiphys/internal/domain"

// Principal is the authenticated user making a request, together with the
// request metadata that audit events carry.
type Principal struct {
	// User is, for an API token, only the name "token:<name>": no ID, no role.
	User domain.User
	// Token is set when the request authenticated with an API token.
	Token     *domain.APIToken
	SessionID string
	CSRFToken string
	SourceIP  string
	UserAgent string
}

func (p Principal) Username() string { return p.User.Username }

// IsAdmin reports whether the principal has the admin role.
func (p Principal) IsAdmin() bool { return p.User.Role == domain.RoleAdmin }

// CanReadConfig reports whether the principal may read configuration and
// audit data (admins and auditors).
func (p Principal) CanReadConfig() bool {
	return p.User.Role == domain.RoleAdmin || p.User.Role == domain.RoleAuditor
}

// KubeActor is how the principal appears toward Kubernetes.
func (p Principal) KubeActor(breakGlassID *int64) KubeActor {
	return KubeActor{Username: p.User.Username, BreakGlassID: breakGlassID}
}
