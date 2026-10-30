package application

import (
	"context"
	"time"

	"go.uber.org/zap"

	"tiphys/internal/domain"
)

// MaintenanceService runs the nightly jobs: audit retention (under the
// retention database role), expiry of old pending requests, and cleanup of
// exec sessions left open by a crash. Recording retention is enforced by
// Object Lock periods and the bucket lifecycle rule, not by the portal.
type MaintenanceService struct {
	store     Store
	retention AuditRetention
	requests  *AccessRequestService
	settings  *SettingsService
	audit     *AuditService
	clock     Clock
	logger    *zap.Logger
}

func NewMaintenanceService(store Store, retention AuditRetention, requests *AccessRequestService, settings *SettingsService, audit *AuditService, clock Clock, logger *zap.Logger) *MaintenanceService {
	return &MaintenanceService{store: store, retention: retention, requests: requests, settings: settings, audit: audit, clock: clock, logger: logger}
}

// AbandonedSessions closes exec sessions that were open when the portal
// stopped. Call it once at startup.
func (s *MaintenanceService) AbandonedSessions(ctx context.Context) {
	n, err := s.store.Repos().ExecSessions.EndAbandoned(ctx, s.clock.Now())
	if err != nil {
		s.logger.Error("failed to close abandoned exec sessions", zap.Error(err))
		return
	}
	if n > 0 {
		s.logger.Warn("closed exec sessions left open by a restart", zap.Int64("count", n))
	}
}

// RunOnce executes every job once.
func (s *MaintenanceService) RunOnce(ctx context.Context) {
	if n, err := s.requests.ExpireOld(ctx); err != nil {
		s.logger.Error("failed to expire old access requests", zap.Error(err))
	} else if n > 0 {
		s.logger.Info("expired old access requests", zap.Int64("count", n))
	}
	if s.retention != nil {
		days := s.settings.Get().RetentionAuditDays
		cutoff := s.clock.Now().Add(-time.Duration(days) * 24 * time.Hour)
		n, err := s.retention.DeleteAuditBefore(ctx, cutoff)
		if err != nil {
			s.logger.Error("audit retention job failed", zap.Error(err))
		} else if n > 0 {
			e := s.audit.SystemEvent(domain.EventAdminChange)
			e.Result = domain.ResultSucceeded
			e.Details = map[string]any{"entity": "audit_events", "operation": "retention_delete", "deleted": n, "before": cutoff}
			s.audit.RecordBestEffort(ctx, e)
		}
	}
}

// Run executes the jobs shortly after start and then daily until ctx is done.
func (s *MaintenanceService) Run(ctx context.Context) {
	timer := time.NewTimer(time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			s.RunOnce(ctx)
			timer.Reset(24 * time.Hour)
		}
	}
}
