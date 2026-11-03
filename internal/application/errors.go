package application

import (
	"errors"
	"fmt"

	"tiphys/internal/domain"
)

var (
	// ErrUnauthenticated means there is no valid login session.
	ErrUnauthenticated = errors.New("unauthenticated")
	// ErrForbiddenRole means the user's role does not allow the operation.
	ErrForbiddenRole = errors.New("forbidden for this role")
	// ErrRecordingUnavailable means exec was refused because recording storage is down.
	ErrRecordingUnavailable = errors.New("session recording storage is unavailable")
	// ErrBreakGlassDisabled is returned when break-glass is turned off in the settings.
	ErrBreakGlassDisabled = errors.New("break-glass is turned off")
	// ErrBreakGlassApprovalRequired is returned by Start when break-glass
	// must be requested and approved instead.
	ErrBreakGlassApprovalRequired = errors.New("break-glass needs an admin's approval")
	// ErrUserDisabled is returned at login for disabled users.
	ErrUserDisabled = errors.New("user is disabled")
	// ErrInvalidCredentials is a failed local login: unknown user, Keycloak
	// user or wrong password, never saying which.
	ErrInvalidCredentials = errors.New("invalid username or password")
	// ErrLastAdmin is returned when a change would leave no active admin.
	ErrLastAdmin = fmt.Errorf("%w: at least one active admin must exist", ErrConflict)
)

// DeniedError is returned when an authorization decision denies an action.
type DeniedError struct {
	Decision domain.Decision
	Target   domain.Target
	Action   domain.Action
}

func (e *DeniedError) Error() string {
	return fmt.Sprintf("denied: %s", e.Decision.Reason)
}

// InvalidError is a request validation failure.
type InvalidError struct {
	Message string
	Fields  map[string]string
}

func (e *InvalidError) Error() string { return e.Message }

// Invalid builds an InvalidError for one field.
func Invalid(field, msg string) *InvalidError {
	return &InvalidError{Message: msg, Fields: map[string]string{field: msg}}
}

// ClusterError wraps a failure talking to a cluster.
type ClusterError struct {
	Cluster string
	Err     error
}

func (e *ClusterError) Error() string { return fmt.Sprintf("cluster %s: %v", e.Cluster, e.Err) }
func (e *ClusterError) Unwrap() error { return e.Err }

// ArgoError wraps a failed call to Argo CD (unreachable, or it refused).
type ArgoError struct{ Err error }

func (e *ArgoError) Error() string { return fmt.Sprintf("argo cd: %v", e.Err) }
func (e *ArgoError) Unwrap() error { return e.Err }
