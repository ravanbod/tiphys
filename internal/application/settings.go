package application

import (
	"context"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"tiphys/internal/domain"
)

// SettingsService caches the admin-editable settings.
type SettingsService struct {
	store  Store
	audit  *AuditService
	logger *zap.Logger
	cur    atomic.Pointer[domain.Settings]
}

func NewSettingsService(store Store, audit *AuditService, logger *zap.Logger) *SettingsService {
	s := &SettingsService{store: store, audit: audit, logger: logger}
	d := domain.DefaultSettings()
	s.cur.Store(&d)
	return s
}

// Get returns the current settings.
func (s *SettingsService) Get() domain.Settings { return *s.cur.Load() }

// Reload reads the settings from the database.
func (s *SettingsService) Reload(ctx context.Context) error {
	v, err := s.store.Repos().Settings.Load(ctx)
	if err != nil {
		return err
	}
	s.cur.Store(&v)
	return nil
}

// Run reloads periodically (for other replicas' changes) until ctx is done.
func (s *SettingsService) Run(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.Reload(ctx); err != nil {
				s.logger.Error("settings reload failed", zap.Error(err))
			}
		}
	}
}

// Validate checks settings values.
func ValidateSettings(v domain.Settings) *InvalidError {
	fields := map[string]string{}
	pos := func(name string, n int, max int) {
		if n < 1 || n > max {
			fields[name] = "must be between 1 and " + itoa(max)
		}
	}
	pos("execMaxSessionMinutes", v.ExecMaxSessionMinutes, 24*60)
	pos("execIdleTimeoutMinutes", v.ExecIdleTimeoutMinutes, 24*60)
	pos("breakGlassDefaultMinutes", v.BreakGlassDefaultMin, 24*60)
	pos("breakGlassMaxMinutes", v.BreakGlassMaxMinutes, 24*60)
	pos("retentionAuditDays", v.RetentionAuditDays, 36500)
	pos("retentionRecordingsDays", v.RetentionRecordingsDays, 36500)
	if v.BreakGlassDefaultMin > v.BreakGlassMaxMinutes {
		fields["breakGlassDefaultMinutes"] = "must not exceed the maximum"
	}
	if len(fields) > 0 {
		return &InvalidError{Message: "invalid settings", Fields: fields}
	}
	return nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
