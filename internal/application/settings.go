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
	pos("breakGlassRequestMaxAgeMinutes", v.BreakGlassRequestMaxAgeMin, 7*24*60)
	if !v.BreakGlassMode.Valid() {
		fields["breakGlassMode"] = "must be off, self or approval"
	}
	pos("retentionAuditDays", v.RetentionAuditDays, 36500)
	pos("retentionRecordingsDays", v.RetentionRecordingsDays, 36500)
	if v.BreakGlassDefaultMin > v.BreakGlassMaxMinutes {
		fields["breakGlassDefaultMinutes"] = "must not exceed the maximum"
	}
	if msg := checkSyncClusterKinds(v.ArgoSyncClusterKinds); msg != "" {
		fields["argoSyncClusterKinds"] = msg
	}
	if len(fields) > 0 {
		return &InvalidError{Message: "invalid settings", Fields: fields}
	}
	return nil
}

// checkSyncClusterKinds explains the first problem in the list ("" if none).
func checkSyncClusterKinds(kinds []string) string {
	if len(kinds) > domain.MaxSyncClusterKinds {
		return "at most " + itoa(domain.MaxSyncClusterKinds) + " kinds"
	}
	seen := map[string]struct{}{}
	for _, k := range kinds {
		if msg := domain.CheckSyncClusterKind(k); msg != "" {
			return k + ": " + msg
		}
		if _, dup := seen[k]; dup {
			return k + ": listed twice"
		}
		seen[k] = struct{}{}
	}
	return ""
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
