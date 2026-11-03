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
	keyBGEnabled    = "break_glass.enabled" // still written for the previous release: mode != off
	keyBGMode       = "break_glass.mode"
	keyBGReqMaxAge  = "break_glass.request_max_age_minutes"
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
		keyBGReqMaxAge: &s.BreakGlassRequestMaxAgeMin,
		keyRetAudit:    &s.RetentionAuditDays, keyRetRecording: &s.RetentionRecordingsDays,
	}
	var enabled *bool // break_glass.enabled, the mode before break_glass.mode existed
	var mode string
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
			if err := json.Unmarshal(raw, &enabled); err != nil {
				return s, fmt.Errorf("load settings: key %s: %w", key, err)
			}
			continue
		}
		if key == keyBGMode {
			if err := json.Unmarshal(raw, &mode); err != nil {
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
	if err := rows.Err(); err != nil {
		return s, mapErr(err, "load settings")
	}
	switch {
	case domain.BreakGlassMode(mode).Valid():
		s.BreakGlassMode = domain.BreakGlassMode(mode)
	case enabled != nil && !*enabled:
		s.BreakGlassMode = domain.BreakGlassOff
	}
	return s, nil
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
		{keyBGMode, s.BreakGlassMode}, {keyBGEnabled, s.BreakGlassMode.Enabled()},
		{keyBGDefault, s.BreakGlassDefaultMin}, {keyBGMax, s.BreakGlassMaxMinutes},
		{keyBGReqMaxAge, s.BreakGlassRequestMaxAgeMin},
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
