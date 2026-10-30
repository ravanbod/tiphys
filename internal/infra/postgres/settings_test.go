package postgres

import (
	"reflect"
	"testing"

	"tiphys/internal/domain"
)

func TestSettings(t *testing.T) {
	f := newFixture(t)

	// Nothing saved: all defaults.
	got, err := f.r.Settings.Load(f.ctx)
	must(t, err)
	if !reflect.DeepEqual(got, domain.DefaultSettings()) {
		t.Fatalf("defaults: %+v", got)
	}

	s := domain.DefaultSettings()
	s.ExecMaxSessionMinutes, s.RetentionAuditDays, s.BreakGlassMaxMinutes = 90, 30, 480
	s.BreakGlassEnabled = false // off must survive a round trip (the default is on)
	s.Rocket.WebhookURL, s.Rocket.Channel = "https://chat.example/hooks/x", "#ops"
	s.Rocket.Events[domain.NotifyExecInProd] = false
	must(t, f.r.Settings.Save(f.ctx, s, "admin"))
	got, err = f.r.Settings.Load(f.ctx)
	must(t, err)
	if !reflect.DeepEqual(got, s) {
		t.Fatalf("round trip:\n got %+v\nwant %+v", got, s)
	}

	// Saving again overwrites; one row per key.
	s.RetentionRecordingsDays = 7
	must(t, f.r.Settings.Save(f.ctx, s, "admin2"))
	var rows int
	var by string
	must(t, f.owner.QueryRow(f.ctx, `SELECT count(*), max(updated_by) FROM settings`).Scan(&rows, &by))
	if rows != 8 || by != "admin2" {
		t.Fatalf("rows=%d updated_by=%s", rows, by)
	}

	// A key that goes missing falls back to its default (break-glass on); unknown keys are ignored.
	_, err = f.owner.Exec(f.ctx, `DELETE FROM settings WHERE key = 'break_glass.enabled'`)
	must(t, err)
	if got, _ = f.r.Settings.Load(f.ctx); !got.BreakGlassEnabled {
		t.Fatalf("a missing break_glass.enabled must default to on: %+v", got)
	}
	_, err = f.owner.Exec(f.ctx, `DELETE FROM settings WHERE key = 'retention.audit_days'`)
	must(t, err)
	_, err = f.owner.Exec(f.ctx, `INSERT INTO settings (key, value) VALUES ('future.thing', '{"a":1}')`)
	must(t, err)
	got, err = f.r.Settings.Load(f.ctx)
	must(t, err)
	if got.RetentionAuditDays != domain.DefaultSettings().RetentionAuditDays || got.RetentionRecordingsDays != 7 {
		t.Fatalf("partial load: %+v", got)
	}

	// An event missing from the stored map keeps its default (true).
	_, err = f.owner.Exec(f.ctx, `UPDATE settings SET value = '{"webhookUrl":"u","channel":"c","events":{"exec_in_prod":false}}' WHERE key = 'rocket'`)
	must(t, err)
	got, _ = f.r.Settings.Load(f.ctx)
	if got.Rocket.Events[domain.NotifyExecInProd] || !got.Rocket.Events[domain.NotifyAdminChanges] || got.Rocket.WebhookURL != "u" {
		t.Fatalf("rocket merge: %+v", got.Rocket)
	}

	// Corrupt value is an error, not a silent default.
	_, err = f.owner.Exec(f.ctx, `UPDATE settings SET value = '"oops"' WHERE key = 'exec.max_session_minutes'`)
	must(t, err)
	if _, err := f.r.Settings.Load(f.ctx); err == nil {
		t.Fatal("want error for a wrongly typed value")
	}
}
