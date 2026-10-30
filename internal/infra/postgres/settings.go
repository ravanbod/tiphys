package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"tiphys/internal/application"
	"tiphys/internal/domain"
)

type settingsRepo struct{ q dbtx }

var _ application.SettingsRepository = settingsRepo{}

// Row keys. The retention keys are the names the spec gives.
const (
	keyExecMax      = "exec.max_session_minutes"
	keyExecIdle     = "exec.idle_timeout_minutes"
	keyBGEnabled    = "break_glass.enabled"
	keyBGDefault    = "break_glass.default_minutes"
	keyBGMax        = "break_glass.max_minutes"
	keyRetAudit     = "retention.audit_days"
	keyRetRecording = "retention.recordings_days"
	keyRocket       = "rocket"
)

type rocketJSON struct {
	WebhookURL string          `json:"webhookUrl"`
	Channel    string          `json:"channel"`
	Events     map[string]bool `json:"events"`
}

// Load starts from domain.DefaultSettings and overlays every stored key, so a
// key that was never saved keeps its default.
func (r settingsRepo) Load(ctx context.Context) (domain.Settings, error) {
	s := domain.DefaultSettings()
	rows, err := r.q.Query(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return s, mapErr(err, "load settings")
	}
	defer rows.Close()
	ints := map[string]*int{
		keyExecMax: &s.ExecMaxSessionMinutes, keyExecIdle: &s.ExecIdleTimeoutMinutes,
		keyBGDefault: &s.BreakGlassDefaultMin, keyBGMax: &s.BreakGlassMaxMinutes,
		keyRetAudit: &s.RetentionAuditDays, keyRetRecording: &s.RetentionRecordingsDays,
	}
	for rows.Next() {
		var key string
		var raw []byte
		if err := rows.Scan(&key, &raw); err != nil {
			return s, mapErr(err, "load settings")
		}
		if dst, ok := ints[key]; ok {
			if err := json.Unmarshal(raw, dst); err != nil {
				return s, fmt.Errorf("load settings: key %s: %w", key, err)
			}
			continue
		}
		if key == keyBGEnabled {
			if err := json.Unmarshal(raw, &s.BreakGlassEnabled); err != nil {
				return s, fmt.Errorf("load settings: key %s: %w", key, err)
			}
			continue
		}
		if key == keyRocket {
			var rj rocketJSON
			if err := json.Unmarshal(raw, &rj); err != nil {
				return s, fmt.Errorf("load settings: key %s: %w", key, err)
			}
			s.Rocket.WebhookURL, s.Rocket.Channel = rj.WebhookURL, rj.Channel
			for ev, on := range rj.Events {
				s.Rocket.Events[domain.NotificationEvent(ev)] = on
			}
		}
		// Unknown keys are ignored so a newer version can add settings.
	}
	return s, mapErr(rows.Err(), "load settings")
}

func (r settingsRepo) Save(ctx context.Context, s domain.Settings, updatedBy string) error {
	events := make(map[string]bool, len(s.Rocket.Events))
	for ev, on := range s.Rocket.Events {
		events[string(ev)] = on
	}
	values := []struct {
		key string
		val any
	}{
		{keyExecMax, s.ExecMaxSessionMinutes}, {keyExecIdle, s.ExecIdleTimeoutMinutes},
		{keyBGEnabled, s.BreakGlassEnabled},
		{keyBGDefault, s.BreakGlassDefaultMin}, {keyBGMax, s.BreakGlassMaxMinutes},
		{keyRetAudit, s.RetentionAuditDays}, {keyRetRecording, s.RetentionRecordingsDays},
		{keyRocket, rocketJSON{WebhookURL: s.Rocket.WebhookURL, Channel: s.Rocket.Channel, Events: events}},
	}
	return withTx(ctx, r.q, func(tx dbtx) error {
		for _, v := range values {
			b, err := json.Marshal(v.val)
			if err != nil {
				return fmt.Errorf("save settings: %s: %w", v.key, err)
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO settings (key, value, updated_by) VALUES ($1, $2, $3)
				ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now(),
					updated_by = EXCLUDED.updated_by`, v.key, b, updatedBy); err != nil {
				return mapErr(err, "save settings")
			}
		}
		return nil
	})
}
