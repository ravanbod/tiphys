import { useEffect, useId, useMemo, useState } from 'react';
import { api } from '../../api';
import type { RocketEvent, SettingsView } from '../../api';
import { AdminErrorBanner, FieldError, ReadOnlyNote } from '../../components/AdminPieces';
import { ErrorBanner, Loading } from '../../components/Feedback';
import { Icon } from '../../components/Icon';
import { PageHeader } from '../../components/PageHeader';
import { useAuth } from '../../context/auth';
import { useToast } from '../../context/toast';
import { useApi } from '../../hooks/useApi';
import { adminErrorText, adminFieldErrors } from '../../lib/adminErrorText';
import { useAdminMutation } from '../../lib/useAdminMutation';

type NumKey =
  | 'execMaxSessionMinutes'
  | 'execIdleTimeoutMinutes'
  | 'breakGlassDefaultMinutes'
  | 'breakGlassMaxMinutes'
  | 'retentionAuditDays'
  | 'retentionRecordingsDays';

const NUM_KEYS: NumKey[] = [
  'execMaxSessionMinutes', 'execIdleTimeoutMinutes', 'breakGlassDefaultMinutes', 'breakGlassMaxMinutes',
  'retentionAuditDays', 'retentionRecordingsDays',
];

const NUM_LABEL: Record<NumKey, string> = {
  execMaxSessionMinutes: 'Maximum exec session length',
  execIdleTimeoutMinutes: 'Idle timeout',
  breakGlassDefaultMinutes: 'Break-glass default duration',
  breakGlassMaxMinutes: 'Break-glass maximum duration',
  retentionAuditDays: 'Audit events',
  retentionRecordingsDays: 'Session recordings',
};

const EVENTS: { key: RocketEvent; label: string; recommended?: boolean }[] = [
  { key: 'break_glass_started', label: 'Break-glass started', recommended: true },
  { key: 'request_created', label: 'Access request created' },
  { key: 'request_decided', label: 'Access request decided' },
  { key: 'exec_in_prod', label: 'Exec opened in prod' },
  { key: 'admin_changes', label: 'Admin changes to grants or roles' },
];

interface Draft {
  breakGlassEnabled: boolean;
  nums: Record<NumKey, string>;
  webhookUrl: string;
  channel: string;
  events: Record<RocketEvent, boolean>;
}

function toDraft(s: SettingsView): Draft {
  const nums = {} as Record<NumKey, string>;
  NUM_KEYS.forEach((k) => {
    nums[k] = String(s[k]);
  });
  return { breakGlassEnabled: s.breakGlassEnabled, nums, webhookUrl: s.rocket.webhookUrl, channel: s.rocket.channel, events: { ...s.rocket.events } };
}

function validate(d: Draft): Record<string, string> {
  const errs: Record<string, string> = {};
  NUM_KEYS.forEach((k) => {
    const v = d.nums[k].trim();
    if (!/^\d+$/.test(v) || Number(v) < 1) errs[k] = 'Enter a whole number of 1 or more';
  });
  if (!errs.breakGlassDefaultMinutes && !errs.breakGlassMaxMinutes && Number(d.nums.breakGlassDefaultMinutes) > Number(d.nums.breakGlassMaxMinutes)) {
    errs.breakGlassDefaultMinutes = 'Default cannot be longer than the maximum';
  }
  return errs;
}

function toSettings(d: Draft, base: SettingsView): SettingsView {
  const out: SettingsView = { ...base, breakGlassEnabled: d.breakGlassEnabled, rocket: { webhookUrl: d.webhookUrl.trim(), channel: d.channel.trim(), events: { ...d.events } } };
  NUM_KEYS.forEach((k) => {
    out[k] = Number(d.nums[k]);
  });
  return out;
}

function sameDraft(a: Draft, b: Draft): boolean {
  return (
    a.breakGlassEnabled === b.breakGlassEnabled &&
    NUM_KEYS.every((k) => a.nums[k].trim() === b.nums[k].trim()) &&
    a.webhookUrl.trim() === b.webhookUrl.trim() &&
    a.channel.trim() === b.channel.trim() &&
    EVENTS.every((e) => a.events[e.key] === b.events[e.key])
  );
}

export function Settings() {
  const { isAdmin } = useAuth();
  const toast = useToast();
  const settings = useApi(() => api.settings(), []);
  const save = useAdminMutation();
  const [draft, setDraft] = useState<Draft | null>(null);
  const [showErrors, setShowErrors] = useState(false);
  const [testing, setTesting] = useState(false);

  useEffect(() => {
    if (settings.data) setDraft(toDraft(settings.data));
  }, [settings.data]);

  const base = settings.data;
  const original = useMemo(() => (base ? toDraft(base) : null), [base]);
  const dirty = !!draft && !!original && !sameDraft(draft, original);
  const rocketDirty = !!draft && !!original && (draft.webhookUrl.trim() !== original.webhookUrl.trim() || draft.channel.trim() !== original.channel.trim());
  const localErrs = useMemo(() => (draft ? validate(draft) : {}), [draft]);
  const serverErrs = adminFieldErrors(save.error);
  const errs = (k: string): string | undefined => (showErrors ? localErrs[k] : undefined) ?? serverErrs[k] ?? serverErrs[`rocket.${k}`];

  const setNum = (k: NumKey, v: string) => {
    save.clear();
    setDraft((d) => d && { ...d, nums: { ...d.nums, [k]: v } });
  };

  const setBreakGlass = (on: boolean) => {
    save.clear();
    setDraft((d) => d && { ...d, breakGlassEnabled: on });
  };

  const submit = async () => {
    if (!draft || !base) return;
    setShowErrors(true);
    if (Object.keys(validate(draft)).length) return;
    const r = await save.run(() => api.saveSettings(toSettings(draft, base)), 'Settings saved');
    if (r.ok) {
      setShowErrors(false);
      settings.setData(r.value);
    }
  };

  const discard = () => {
    if (!original) return;
    setDraft(original);
    setShowErrors(false);
    save.clear();
  };

  const sendTest = async () => {
    setTesting(true);
    try {
      await api.testRocket();
      toast('Test message sent to Rocket.Chat');
    } catch (e) {
      toast(`Test message failed: ${adminErrorText(e)}`, 'err');
    } finally {
      setTesting(false);
    }
  };

  const failedFields = Object.keys(serverErrs).length > 0;

  return (
    <div style={{ maxWidth: 980 }}>
      <PageHeader title="Settings" subtitle="Every change here is recorded in the audit log.">
        {isAdmin ? (
          <>
            <button type="button" className="btn" disabled={!dirty || save.busy} onClick={discard}>
              Discard
            </button>
            <button type="button" className="btn pri" disabled={!dirty || save.busy} onClick={() => void submit()}>
              {save.busy ? 'Saving…' : 'Save changes'}
            </button>
          </>
        ) : (
          <ReadOnlyNote />
        )}
      </PageHeader>

      {settings.loading && !base && <Loading what="settings" />}
      {!!settings.error && !base && <ErrorBanner error={settings.error} onRetry={settings.reload} />}

      {draft && base && (
        <div className="col" style={{ gap: 16, marginTop: 12 }}>
          <AdminErrorBanner error={failedFields ? null : save.error} />
          {failedFields && (
            <div className="banner red" role="alert">
              The server rejected some values. Fix the marked fields and save again.
            </div>
          )}
          {isAdmin && showErrors && Object.keys(localErrs).length > 0 && (
            <div className="banner red" role="alert">
              Some values are not valid. Fix the marked fields and save again.
            </div>
          )}

          <div className="card">
            <div className="hd">Break-glass</div>
            <div className="bd col" style={{ gap: 8 }}>
              <label className="row">
                <input
                  className="sw"
                  type="checkbox"
                  role="switch"
                  checked={draft.breakGlassEnabled}
                  disabled={!isAdmin}
                  onChange={(e) => setBreakGlass(e.target.checked)}
                />
                <span>Allow break-glass</span>
              </label>
              <div className="muted" style={{ fontSize: 12 }}>
                {draft.breakGlassEnabled
                  ? 'Users can override time windows and holidays for a limited time, with a reason, and the platform team is notified.'
                  : 'Off: nobody can start break-glass, and ones already active stop working as soon as you save, ending sessions that depended on them. Exec, restart and run-job stay closed outside their windows.'}
                {' '}Reading logs is never limited by time windows, so it never needs break-glass.
              </div>
            </div>
          </div>

          <div className="card">
            <div className="hd">Session limits</div>
            <div className="bd row wrap" style={{ gap: 24, alignItems: 'flex-start' }}>
              {(['execMaxSessionMinutes', 'execIdleTimeoutMinutes', 'breakGlassDefaultMinutes', 'breakGlassMaxMinutes'] as NumKey[]).map((k) => (
                <NumField key={k} k={k} unit="minutes" value={draft.nums[k]} error={errs(k)} readOnly={!isAdmin} onChange={setNum} />
              ))}
            </div>
          </div>

          <div className="card">
            <div className="hd">Retention</div>
            <div className="bd row wrap" style={{ gap: 32, alignItems: 'flex-start' }}>
              {(['retentionAuditDays', 'retentionRecordingsDays'] as NumKey[]).map((k) => (
                <NumField key={k} k={k} unit="days" value={draft.nums[k]} error={errs(k)} readOnly={!isAdmin} onChange={setNum} />
              ))}
            </div>
          </div>

          <RocketCard
            draft={draft}
            isAdmin={isAdmin}
            errs={errs}
            onChange={(patch) => {
              save.clear();
              setDraft((d) => d && { ...d, ...patch });
            }}
            onEvent={(key, on) => {
              save.clear();
              setDraft((d) => d && { ...d, events: { ...d.events, [key]: on } });
            }}
            onTest={() => void sendTest()}
            testing={testing}
            testDisabledReason={rocketDirty ? 'Save the webhook changes first. The test uses the saved settings.' : ''}
          />

          <div className="card">
            <div className="hd">Loki retention</div>
            <div className="bd">
              {base.lokiRetentionInfo ? (
                <span>{base.lokiRetentionInfo}</span>
              ) : (
                <span className="muted">Loki did not report a retention period.</span>
              )}
              <div className="muted" style={{ fontSize: 12, marginTop: 4 }}>
                Informational only. Log retention is configured in Loki, not here.
              </div>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}

function NumField({
  k, unit, value, error, readOnly, onChange,
}: {
  k: NumKey;
  unit: string;
  value: string;
  error: string | undefined;
  readOnly: boolean;
  onChange: (k: NumKey, v: string) => void;
}) {
  const id = useId();
  return (
    <label className="f" style={{ width: 220 }}>
      {NUM_LABEL[k]}
      <div className="row" style={{ gap: 8 }}>
        <input
          className="input"
          style={{ width: 90 }}
          inputMode="numeric"
          value={value}
          readOnly={readOnly}
          onChange={(e) => onChange(k, e.target.value)}
          aria-label={`${NUM_LABEL[k]} in ${unit}`}
          aria-invalid={!!error}
          aria-describedby={error ? `${id}-e` : undefined}
        />
        <span className="muted">{unit}</span>
      </div>
      <FieldError id={`${id}-e`} text={error} />
    </label>
  );
}

function RocketCard({
  draft, isAdmin, errs, onChange, onEvent, onTest, testing, testDisabledReason,
}: {
  draft: Draft;
  isAdmin: boolean;
  errs: (k: string) => string | undefined;
  onChange: (p: Partial<Pick<Draft, 'webhookUrl' | 'channel'>>) => void;
  onEvent: (k: RocketEvent, on: boolean) => void;
  onTest: () => void;
  testing: boolean;
  testDisabledReason: string;
}) {
  const id = useId();
  return (
    <div className="card">
      <div className="hd">Rocket.Chat notifications</div>
      <div className="bd col" style={{ gap: 16 }}>
        <div className="row wrap" style={{ gap: 16, alignItems: 'flex-end' }}>
          <label className="f" style={{ flex: 2, minWidth: 260 }}>
            Webhook URL
            <input
              className="input mono"
              value={draft.webhookUrl}
              readOnly={!isAdmin}
              onChange={(e) => onChange({ webhookUrl: e.target.value })}
              aria-invalid={!!errs('webhookUrl')}
              aria-describedby={errs('webhookUrl') ? `${id}-w` : undefined}
              autoComplete="off"
              spellCheck={false}
            />
            <FieldError id={`${id}-w`} text={errs('webhookUrl')} />
          </label>
          <label className="f" style={{ flex: 1, minWidth: 160 }}>
            Channel
            <input
              className="input mono"
              value={draft.channel}
              readOnly={!isAdmin}
              onChange={(e) => onChange({ channel: e.target.value })}
              aria-invalid={!!errs('channel')}
              aria-describedby={errs('channel') ? `${id}-c` : undefined}
            />
            <FieldError id={`${id}-c`} text={errs('channel')} />
          </label>
          {isAdmin && (
            <button
              type="button"
              className="btn"
              disabled={testing || !!testDisabledReason || !draft.webhookUrl.trim()}
              title={testDisabledReason || undefined}
              onClick={onTest}
            >
              <Icon name="bolt" />
              {testing ? 'Sending…' : 'Send test'}
            </button>
          )}
        </div>
        {isAdmin && testDisabledReason && <div className="muted" style={{ fontSize: 12, marginTop: -8 }}>{testDisabledReason}</div>}
        <div className="col" style={{ gap: 0, border: '1px solid var(--ln)', borderRadius: 6 }}>
          {EVENTS.map((ev, i) => (
            <label key={ev.key} className="row" style={{ padding: '10px 14px', borderTop: i ? '1px solid var(--ln)' : undefined }}>
              <input
                className="sw"
                type="checkbox"
                role="switch"
                checked={draft.events[ev.key]}
                disabled={!isAdmin}
                onChange={(e) => onEvent(ev.key, e.target.checked)}
              />
              <span>{ev.label}</span>
              {ev.recommended && (
                <span className="chip err" style={{ marginLeft: 'auto' }}>
                  Recommended
                </span>
              )}
            </label>
          ))}
        </div>
      </div>
    </div>
  );
}
