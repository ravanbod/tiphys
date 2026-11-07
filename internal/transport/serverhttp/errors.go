package serverhttp

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"tiphys/internal/application"
	"tiphys/internal/domain"
	"tiphys/internal/requestid"
)

type apiError struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

func abort(c *gin.Context, status int, code, message string, details map[string]any) {
	c.AbortWithStatusJSON(status, gin.H{"error": apiError{Code: code, Message: message, Details: details}})
}

// decisionInfo is the JSON form of domain.Decision.
type decisionInfo struct {
	Allowed      bool    `json:"allowed"`
	Reason       string  `json:"reason"`
	Environment  string  `json:"environment"`
	GrantIDs     []int64 `json:"grantIds"`
	BreakGlassID *int64  `json:"breakGlassId"`
	ReopensAt    *string `json:"reopensAt"`
	ClosesAt     *string `json:"closesAt"`
}

func toDecisionInfo(d domain.Decision) decisionInfo {
	ids := d.GrantIDs
	if ids == nil {
		ids = []int64{}
	}
	return decisionInfo{
		Allowed: d.Allowed, Reason: string(d.Reason), Environment: d.Environment, GrantIDs: ids,
		BreakGlassID: d.BreakGlassID, ReopensAt: timeStr(d.ReopensAt), ClosesAt: timeStr(d.ClosesAt),
	}
}

var denialText = map[domain.Reason]string{
	domain.ReasonProtectedNamespace: "This namespace is protected.",
	domain.ReasonUserDisabled:       "Your account is disabled.",
	domain.ReasonRole:               "Your role does not allow this.",
	domain.ReasonNoGrant:            "You have no grant for this action here.",
	domain.ReasonGrantExpired:       "Your grant has expired.",
	domain.ReasonOutsideWindow:      "This action is closed outside its allowed time window.",
	domain.ReasonHoliday:            "This action is closed because of a holiday.",
	domain.ReasonArgoStale:          "Argo CD data is out of date, so project-scoped grants do not apply.",
	domain.ReasonAppScope:           "This application manages cluster-wide resources (or deploys nowhere the portal knows), so only an admin can sync or roll it back.",
}

// writeError maps an application error to an HTTP response. Business
// outcomes are not logged here; unexpected failures are logged once.
func (h *Handler) writeError(c *gin.Context, err error) {
	var denied *application.DeniedError
	var invalid *application.InvalidError
	var priv *application.PrivilegeError
	var clusterErr *application.ClusterError
	var argoErr *application.ArgoError
	switch {
	case errors.As(err, &denied):
		abort(c, http.StatusForbidden, "denied", denialText[denied.Decision.Reason], map[string]any{"decision": toDecisionInfo(denied.Decision)})
	case errors.As(err, &invalid):
		d := map[string]any{}
		if len(invalid.Fields) > 0 {
			d["fields"] = invalid.Fields
		}
		abort(c, http.StatusUnprocessableEntity, "invalid", invalid.Message, d)
	case errors.As(err, &priv):
		abort(c, http.StatusUnprocessableEntity, "invalid", priv.Error(), map[string]any{"violations": priv.Violations})
	case errors.Is(err, application.ErrInvalidCredentials):
		abort(c, http.StatusUnauthorized, "invalid_credentials", "Wrong username or password.", nil)
	case errors.Is(err, application.ErrUnauthenticated):
		abort(c, http.StatusUnauthorized, "unauthenticated", "Sign in required.", nil)
	case errors.Is(err, application.ErrForbiddenRole):
		abort(c, http.StatusForbidden, "forbidden_role", "Your role does not allow this.", nil)
	case errors.Is(err, application.ErrBreakGlassDisabled):
		abort(c, http.StatusForbidden, "break_glass_disabled", "Break-glass is turned off.", nil)
	case errors.Is(err, application.ErrBreakGlassApprovalRequired):
		abort(c, http.StatusConflict, "break_glass_approval_required", "Break-glass needs an admin's approval: request it instead.", nil)
	case errors.Is(err, application.ErrUserDisabled):
		abort(c, http.StatusForbidden, "user_disabled", "Your account is disabled.", nil)
	case errors.Is(err, application.ErrNotFound):
		abort(c, http.StatusNotFound, "not_found", "Not found.", nil)
	case errors.Is(err, application.ErrAlreadyRunning):
		abort(c, http.StatusConflict, "already_running", "A run of this CronJob is already in progress.", nil)
	case errors.Is(err, application.ErrSyncInProgress):
		abort(c, http.StatusConflict, "already_running", "Another operation on this application is already in progress in Argo CD.", nil)
	case errors.Is(err, application.ErrAutoSyncEnabled):
		abort(c, http.StatusConflict, "auto_sync_enabled", "This application syncs automatically, so Argo CD does not roll it back. Turn off auto-sync in Git or Argo CD first.", nil)
	case errors.Is(err, application.ErrLastAdmin):
		abort(c, http.StatusConflict, "last_admin", "At least one active admin must exist.", nil)
	case errors.Is(err, application.ErrConflict):
		abort(c, http.StatusConflict, "conflict", err.Error(), nil)
	case errors.Is(err, application.ErrRecordingUnavailable):
		abort(c, http.StatusServiceUnavailable, "recording_unavailable", "Session recording storage is unavailable, so exec is refused.", nil)
	case errors.Is(err, application.ErrProtectedNamespace):
		abort(c, http.StatusForbidden, "denied", denialText[domain.ReasonProtectedNamespace],
			map[string]any{"decision": decisionInfo{Reason: string(domain.ReasonProtectedNamespace), GrantIDs: []int64{}}})
	case errors.Is(err, application.ErrPodNotInWorkload):
		abort(c, http.StatusForbidden, "pod_not_in_workload", "The pod does not belong to this workload.", nil)
	case errors.Is(err, application.ErrInvalidName):
		abort(c, http.StatusUnprocessableEntity, "invalid", "Invalid name.", nil)
	case errors.As(err, &argoErr):
		h.logger.Warn("argo cd error", requestid.Field(c.Request.Context()), zap.String("path", c.FullPath()), zap.Error(err))
		abort(c, http.StatusBadGateway, "argo_error", "Argo CD could not be reached or refused the request.", nil)
	case errors.As(err, &clusterErr), errors.Is(err, application.ErrClusterUnknown), errors.Is(err, application.ErrClusterUnusable):
		h.logger.Warn("cluster error", requestid.Field(c.Request.Context()), zap.String("path", c.FullPath()), zap.Error(err))
		abort(c, http.StatusBadGateway, "cluster_error", "The cluster could not be reached or refused the request.", nil)
	default:
		h.logger.Error("unhandled error", requestid.Field(c.Request.Context()), zap.String("path", c.FullPath()), zap.Error(err))
		abort(c, http.StatusInternalServerError, "internal", "Internal error.", nil)
	}
}
