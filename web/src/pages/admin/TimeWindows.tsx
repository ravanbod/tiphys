import { useMemo, useState } from 'react';
import { ApiError, api } from '../../api';
import type { Action, ClusterView, HolidayInput, HolidayView, TeamView, TimeWindowInput, TimeWindowView } from '../../api';
import { FieldError, ReadOnlyNote } from '../../components/AdminBits';
import '../../components/adminPages.css';
import { EnvBadge } from '../../components/Badges';
import { Dialog } from '../../components/Dialog';
import { Empty, ErrorBanner, Loading } from '../../components/Feedback';
import { Icon } from '../../components/Icon';
import { PageHeader } from '../../components/PageHeader';
import { useAuth } from '../../context/auth';
import { useToast } from '../../context/toast';
import { useApi } from '../../hooks/useApi';
import { useNow } from '../../hooks/useNow';
import { fieldError, unmatchedFields } from '../../lib/adminFields';
import { parseHolidayCsv } from '../../lib/holidayCsv';
import { fmtDay, pad2 } from '../../lib/time';
import {
  DAY_SHORT, DEFAULT_TZ, WEEK_ORDER, buildWeeklyPreview, fmtDays, fmtMinutes, isValidTimezone, parseHHMM,
} from '../../lib/windowPreview';
import type { PreviewSegment } from '../../lib/windowPreview';
import { ARGO_UI } from '../../lib/features';
import { actionOptionLabel } from '../../lib/grantForm';

// Logs are never limited by time windows (they never need break-glass), so a rule cannot list them.
const ACTIONS: Action[] = ['exec', 'restart', 'run-job', 'sync'];
const COMMON_TZ = ['Asia/Tehran', 'UTC', 'Asia/Dubai', 'Europe/Istanbul', 'Europe/London', 'Europe/Berlin', 'America/New_York'];
const SCOPE_LABEL: Record<TimeWindowView['scopeKind'], string> = {
  global: 'Global', environment: 'Environment', cluster: 'Cluster', argo_project: 'Argo project', team: 'Team',
};
const RULE_FIELDS = ['name', 'scopeKind', 'scopeValue', 'actions', 'daysOfWeek', 'startTime', 'endTime', 'timezone', 'effect', 'enabled'];
const HOLIDAY_FIELDS = ['date', 'title', 'environments'];

function uniq(xs: (string | undefined | null)[]): string[] {
  return Array.from(new Set(xs.filter((x): x is string => !!x))).sort();
}

function ScopeCell({ r }: { r: TimeWindowView }) {
  return (
    <span>
      <span className="tag">{SCOPE_LABEL[r.scopeKind]}</span>{' '}
      {r.scopeKind === 'global' ? (
        <span className="muted">all targets</span>
      ) : r.scopeKind === 'environment' ? (
        <EnvBadge env={r.scopeValue} />
      ) : r.scopeKind === 'team' ? (
        <span>{r.scopeValue}</span>
      ) : (
        <span className="mono">{r.scopeValue}</span>
      )}
    </span>
  );
}

function EffectChip({ effect }: { effect: TimeWindowView['effect'] }) {
  return effect === 'allow_inside' ? (
    <span className="chip ok">Allow only inside</span>
  ) : (
    <span className="chip err">Deny inside</span>
  );
}

function holidayDate(date: string, now: number): string {
  const d = fmtDay(`${date}T12:00:00Z`, 'UTC');
  const y = date.slice(0, 4);
  return y === String(new Date(now).getFullYear()) ? d : `${d}, ${y}`;
}

/* ------------------------------------------------------------- rule dialog */

interface RuleForm {
  name: string;
  scopeKind: TimeWindowView['scopeKind'];
  scopeValue: string;
  actions: Action[];
  days: number[];
  start: string;
  end: string;
  timezone: string;
  effect: TimeWindowView['effect'];
  enabled: boolean;
}

function ruleToForm(r: TimeWindowView | null): RuleForm {
  return {
    name: r?.name ?? '',
    scopeKind: r?.scopeKind ?? 'environment',
    scopeValue: r?.scopeValue ?? '',
    actions: r?.actions ?? ['exec', 'restart', 'run-job'],
    days: r?.daysOfWeek ?? [6, 0, 1, 2, 3],
    start: r?.startTime ?? '08:00',
    end: r?.endTime ?? '17:00',
    timezone: r?.timezone ?? DEFAULT_TZ,
    effect: r?.effect ?? 'allow_inside',
    enabled: r?.enabled ?? true,
  };
}

function validateRule(f: RuleForm): Record<string, string> {
  const e: Record<string, string> = {};
  if (!f.name.trim()) e.name = 'Give the rule a name.';
  if (f.scopeKind !== 'global' && !f.scopeValue.trim()) e.scopeValue = `Choose ${f.scopeKind === 'argo_project' ? 'an Argo project' : `a ${f.scopeKind}`}.`;
  if (f.actions.length === 0) e.actions = 'Select at least one action.';
  if (f.days.length === 0) e.daysOfWeek = 'Select at least one day.';
  const s = parseHHMM(f.start);
  const en = parseHHMM(f.end);
  if (s === null) e.startTime = 'Enter a start time (HH:MM).';
  if (en === null) e.endTime = 'Enter an end time (HH:MM).';
  if (!isValidTimezone(f.timezone)) e.timezone = 'Unknown timezone. Use an IANA name such as Asia/Tehran.';
  return e;
}

function RuleDialog({
  rule, envs, clusters, teams, projects, onClose, onSaved,
}: {
  rule: TimeWindowView | null;
  envs: string[];
  clusters: ClusterView[];
  teams: TeamView[];
  projects: string[];
  onClose: () => void;
  onSaved: () => void;
}) {
  const toast = useToast();
  const [f, setF] = useState<RuleForm>(() => ruleToForm(rule));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [local, setLocal] = useState<Record<string, string>>({});
  const [server, setServer] = useState<Record<string, string>>({});
  const fields = { ...server, ...local };
  const set = (p: Partial<RuleForm>) => {
    setF((cur) => ({ ...cur, ...p }));
    setLocal({});
  };
  const s = parseHHMM(f.start);
  const en = parseHHMM(f.end);
  const crosses = s !== null && en !== null && en < s;
  const allDay = s !== null && en !== null && en === s;

  const save = async () => {
    const errs = validateRule(f);
    setLocal(errs);
    setServer({});
    setError(null);
    if (Object.keys(errs).length > 0) return;
    const body: TimeWindowInput = {
      name: f.name.trim(),
      scopeKind: f.scopeKind,
      scopeValue: f.scopeKind === 'global' ? null : f.scopeValue.trim(),
      actions: ACTIONS.filter((a) => f.actions.includes(a)),
      daysOfWeek: WEEK_ORDER.filter((d) => f.days.includes(d)),
      startTime: f.start,
      endTime: f.end,
      timezone: f.timezone.trim(),
      effect: f.effect,
      enabled: f.enabled,
    };
    setBusy(true);
    try {
      if (rule) await api.updateTimeWindow(rule.id, body);
      else await api.createTimeWindow(body);
      toast(rule ? 'Rule saved.' : 'Rule created.');
      onSaved();
    } catch (e) {
      setError(e);
      if (e instanceof ApiError) setServer(e.fields);
    } finally {
      setBusy(false);
    }
  };

  const leftover = unmatchedFields(fields, RULE_FIELDS);
  const vId = 'tw-scope-values';
  return (
    <Dialog
      title={rule ? 'Edit rule' : 'New rule'}
      subtitle="A window can allow an action only inside it, or deny it inside it."
      width={560}
      onClose={onClose}
      footer={
        <>
          <button type="button" className="btn" onClick={onClose} disabled={busy}>Cancel</button>
          <button type="submit" form="tw-rule-form" className="btn pri" disabled={busy}>
            {busy ? 'Saving…' : rule ? 'Save rule' : 'Create rule'}
          </button>
        </>
      }
    >
      <form id="tw-rule-form" className="col" style={{ gap: 14 }} onSubmit={(e) => { e.preventDefault(); void save(); }} noValidate>
        <label className="f">
          Name
          <input className="input" data-autofocus value={f.name} aria-invalid={fields.name ? true : undefined} onChange={(e) => set({ name: e.target.value })} />
          <FieldError msg={fieldError(fields, 'name')} />
        </label>

        <div className="row wrap" style={{ gap: 12, alignItems: 'flex-start' }}>
          <label className="f" style={{ flex: '0 0 150px' }}>
            Scope
            <select className="sel" style={{ height: 30 }} value={f.scopeKind} onChange={(e) => set({ scopeKind: e.target.value as RuleForm['scopeKind'], scopeValue: '' })}>
              {(Object.keys(SCOPE_LABEL) as TimeWindowView['scopeKind'][]).filter((k) => ARGO_UI || k !== 'argo_project').map((k) => (
                <option key={k} value={k}>{SCOPE_LABEL[k]}</option>
              ))}
            </select>
            <FieldError msg={fieldError(fields, 'scopeKind')} />
          </label>
          {f.scopeKind !== 'global' && (
            <label className="f" style={{ flex: '1 1 200px' }}>
              {SCOPE_LABEL[f.scopeKind]}
              {f.scopeKind === 'cluster' || f.scopeKind === 'team' ? (
                <select className="sel" style={{ height: 30 }} value={f.scopeValue} aria-invalid={fields.scopeValue ? true : undefined} onChange={(e) => set({ scopeValue: e.target.value })}>
                  <option value="">Choose…</option>
                  {uniq([...(f.scopeKind === 'cluster' ? clusters.map((c) => c.name) : teams.map((t) => t.name)), f.scopeValue]).map((v) => (
                    <option key={v} value={v}>{v}</option>
                  ))}
                </select>
              ) : (
                <>
                  <input
                    className="input"
                    list={vId}
                    value={f.scopeValue}
                    aria-invalid={fields.scopeValue ? true : undefined}
                    onChange={(e) => set({ scopeValue: e.target.value })}
                  />
                  <datalist id={vId}>
                    {(f.scopeKind === 'environment' ? envs : projects).map((v) => <option key={v} value={v} />)}
                  </datalist>
                </>
              )}
              <FieldError msg={fieldError(fields, 'scopeValue')} />
            </label>
          )}
        </div>

        <div className="f" role="group" aria-label="Actions">
          Actions <span className="hint">Reading logs is never limited by time windows or holidays.</span>
          <div className="row wrap" style={{ gap: 16 }}>
            {ACTIONS.map((a) => (
              <label key={a} className="chk">
                <input
                  type="checkbox"
                  checked={f.actions.includes(a)}
                  onChange={() => set({ actions: f.actions.includes(a) ? f.actions.filter((x) => x !== a) : [...f.actions, a] })}
                />
                {actionOptionLabel(a)}
              </label>
            ))}
          </div>
          <FieldError msg={fieldError(fields, 'actions')} />
        </div>

        <div className="f" role="group" aria-label="Days">
          <span className="row" style={{ gap: 8 }}>
            Days <span className="hint">week starts Saturday</span>
            <span className="grow" />
            <button type="button" className="linklike blue" onClick={() => set({ days: [6, 0, 1, 2, 3] })}>Sat – Wed</button>
            <button type="button" className="linklike blue" onClick={() => set({ days: [0, 1, 2, 3, 4, 5, 6] })}>Every day</button>
          </span>
          <div className="row wrap" style={{ gap: 14 }}>
            {WEEK_ORDER.map((d) => (
              <label key={d} className="chk">
                <input
                  type="checkbox"
                  checked={f.days.includes(d)}
                  onChange={() => set({ days: f.days.includes(d) ? f.days.filter((x) => x !== d) : [...f.days, d] })}
                />
                {DAY_SHORT[d]}
              </label>
            ))}
          </div>
          <FieldError msg={fieldError(fields, 'daysOfWeek')} />
        </div>

        <div className="row wrap" style={{ gap: 12, alignItems: 'flex-start' }}>
          <label className="f" style={{ flex: '0 0 120px' }}>
            Start
            <input className="input" type="time" value={f.start} aria-invalid={fields.startTime ? true : undefined} onChange={(e) => set({ start: e.target.value })} />
            <FieldError msg={fieldError(fields, 'startTime')} />
          </label>
          <label className="f" style={{ flex: '0 0 120px' }}>
            End
            <input className="input" type="time" value={f.end} aria-invalid={fields.endTime ? true : undefined} onChange={(e) => set({ end: e.target.value })} />
            <FieldError msg={fieldError(fields, 'endTime')} />
          </label>
          <label className="f" style={{ flex: '1 1 160px' }}>
            Timezone
            <input className="input" list="tw-tz" value={f.timezone} aria-invalid={fields.timezone ? true : undefined} onChange={(e) => set({ timezone: e.target.value })} />
            <datalist id="tw-tz">{COMMON_TZ.map((z) => <option key={z} value={z} />)}</datalist>
            <FieldError msg={fieldError(fields, 'timezone')} />
          </label>
        </div>
        {crosses && (
          <div className="muted">Crosses midnight: the window starts on each selected day and ends the next morning.</div>
        )}
        {allDay && (
          <div className="muted">Start equals end: the window lasts 24 hours from the start time (00:00 to 00:00 covers each selected day entirely).</div>
        )}

        <div className="row wrap" style={{ gap: 16, alignItems: 'flex-end' }}>
          <label className="f" style={{ flex: '1 1 220px' }}>
            Effect
            <select className="sel" style={{ height: 30 }} value={f.effect} onChange={(e) => set({ effect: e.target.value as RuleForm['effect'] })}>
              <option value="allow_inside">Allow only inside this window</option>
              <option value="deny_inside">Deny inside this window</option>
            </select>
            <FieldError msg={fieldError(fields, 'effect')} />
          </label>
          <label className="chk" style={{ height: 30 }}>
            <input type="checkbox" className="sw" role="switch" checked={f.enabled} onChange={(e) => set({ enabled: e.target.checked })} />
            Enabled
          </label>
        </div>

        {leftover.length > 0 && (
          <div className="banner red" role="alert" style={{ flexDirection: 'column', alignItems: 'flex-start' }}>
            {leftover.map(([k, m]) => <span key={k}><b>{k}</b>: {m}</span>)}
          </div>
        )}
        {error !== null && Object.keys(server).length === 0 && <ErrorBanner error={error} />}
      </form>
    </Dialog>
  );
}

function DeleteDialog({
  title, subtitle, onClose, onConfirm,
}: { title: string; subtitle: React.ReactNode; onClose: () => void; onConfirm: () => Promise<void> }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const go = async () => {
    setBusy(true);
    setError(null);
    try {
      await onConfirm();
    } catch (e) {
      setError(e);
      setBusy(false);
    }
  };
  return (
    <Dialog
      title={title}
      subtitle={subtitle}
      onClose={onClose}
      footer={
        <>
          <button type="button" className="btn" onClick={onClose} disabled={busy}>Cancel</button>
          <button type="button" className="btn dng" onClick={() => void go()} disabled={busy}>{busy ? 'Deleting…' : 'Delete'}</button>
        </>
      }
    >
      {error !== null && <ErrorBanner error={error} />}
    </Dialog>
  );
}

/* ----------------------------------------------------------- holiday dialogs */

function HolidayDialog({
  holiday, envs, onClose, onSaved,
}: { holiday: HolidayView | null; envs: string[]; onClose: () => void; onSaved: () => void }) {
  const toast = useToast();
  const [date, setDate] = useState(holiday?.date ?? '');
  const [title, setTitle] = useState(holiday?.title ?? '');
  const [selected, setSelected] = useState<string[]>(holiday?.environments ?? []);
  const [custom, setCustom] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [local, setLocal] = useState<Record<string, string>>({});
  const [server, setServer] = useState<Record<string, string>>({});
  const fields = { ...server, ...local };
  const options = uniq([...envs, ...selected]);

  const toggle = (e: string) => setSelected((cur) => (cur.includes(e) ? cur.filter((x) => x !== e) : [...cur, e]));
  const addCustom = () => {
    const v = custom.trim();
    if (v && !selected.includes(v)) setSelected([...selected, v]);
    setCustom('');
  };

  const save = async () => {
    const errs: Record<string, string> = {};
    if (!/^\d{4}-\d{2}-\d{2}$/.test(date)) errs.date = 'Choose a date.';
    if (!title.trim()) errs.title = 'Give the holiday a title.';
    setLocal(errs);
    setServer({});
    setError(null);
    if (Object.keys(errs).length > 0) return;
    const body: HolidayInput = { date, title: title.trim(), environments: selected };
    setBusy(true);
    try {
      if (holiday) await api.updateHoliday(holiday.id, body);
      else await api.createHoliday(body);
      toast(holiday ? 'Holiday saved.' : 'Holiday added.');
      onSaved();
    } catch (e) {
      setError(e);
      if (e instanceof ApiError) setServer(e.fields);
    } finally {
      setBusy(false);
    }
  };
  const leftover = unmatchedFields(fields, HOLIDAY_FIELDS);

  return (
    <Dialog
      title={holiday ? 'Edit holiday' : 'Add holiday'}
      subtitle="Closes the selected environments for the whole day (in the rule's timezone)."
      onClose={onClose}
      footer={
        <>
          <button type="button" className="btn" onClick={onClose} disabled={busy}>Cancel</button>
          <button type="submit" form="tw-holiday-form" className="btn pri" disabled={busy}>{busy ? 'Saving…' : holiday ? 'Save holiday' : 'Add holiday'}</button>
        </>
      }
    >
      <form id="tw-holiday-form" className="col" style={{ gap: 14 }} onSubmit={(e) => { e.preventDefault(); void save(); }} noValidate>
        <label className="f">
          Date
          <input className="input" type="date" data-autofocus value={date} aria-invalid={fields.date ? true : undefined} onChange={(e) => { setDate(e.target.value); setLocal({}); }} />
          <FieldError msg={fieldError(fields, 'date')} />
        </label>
        <label className="f">
          Title
          <input className="input" value={title} aria-invalid={fields.title ? true : undefined} onChange={(e) => { setTitle(e.target.value); setLocal({}); }} />
          <FieldError msg={fieldError(fields, 'title')} />
        </label>
        <div className="f" role="group" aria-label="Applies to">
          <span>Applies to <span className="hint">None selected = all environments</span></span>
          <div className="row wrap" style={{ gap: 14 }}>
            {options.length === 0 && <span className="muted">All environments</span>}
            {options.map((e) => (
              <label key={e} className="chk"><input type="checkbox" checked={selected.includes(e)} onChange={() => toggle(e)} />{e}</label>
            ))}
          </div>
          <div className="row" style={{ gap: 8 }}>
            <input
              className="input"
              style={{ maxWidth: 200 }}
              aria-label="Other environment name"
              placeholder="Other environment…"
              value={custom}
              onChange={(e) => setCustom(e.target.value)}
              onKeyDown={(e) => { if (e.key === 'Enter') { e.preventDefault(); addCustom(); } }}
            />
            <button type="button" className="btn sm" onClick={addCustom} disabled={!custom.trim()}>Add</button>
          </div>
          <FieldError msg={fieldError(fields, 'environments')} />
        </div>
        {leftover.length > 0 && (
          <div className="banner red" role="alert" style={{ flexDirection: 'column', alignItems: 'flex-start' }}>
            {leftover.map(([k, m]) => <span key={k}><b>{k}</b>: {m}</span>)}
          </div>
        )}
        {error !== null && Object.keys(server).length === 0 && <ErrorBanner error={error} />}
      </form>
    </Dialog>
  );
}

function ImportDialog({ onClose, onDone }: { onClose: () => void; onDone: () => void }) {
  const toast = useToast();
  const [text, setText] = useState('');
  const [busy, setBusy] = useState(false);
  const [failures, setFailures] = useState<string[]>([]);
  const parsed = useMemo(() => parseHolidayCsv(text), [text]);

  const run = async () => {
    setBusy(true);
    setFailures([]);
    const failed: string[] = [];
    let ok = 0;
    for (const row of parsed.rows) {
      try {
        await api.createHoliday(row);
        ok++;
      } catch (e) {
        failed.push(`${row.date} ${row.title}: ${e instanceof Error ? e.message : 'failed'}`);
        if (e instanceof ApiError && e.code === 'rate_limited') {
          failed.push('Stopped: too many actions per minute. Import the rest in a minute.');
          break;
        }
      }
    }
    setBusy(false);
    if (ok > 0) {
      toast(`Imported ${ok} holiday${ok === 1 ? '' : 's'}.`);
      onDone();
    }
    if (failed.length > 0) setFailures(failed);
    else onClose();
  };

  return (
    <Dialog
      title="Import holidays"
      subtitle="Paste CSV, one holiday per line: date,title,env1|env2. Leave environments empty for all."
      width={560}
      onClose={onClose}
      footer={
        <>
          <button type="button" className="btn" onClick={onClose} disabled={busy}>Close</button>
          <button type="button" className="btn pri" onClick={() => void run()} disabled={busy || parsed.rows.length === 0}>
            {busy ? 'Importing…' : `Import ${parsed.rows.length} holiday${parsed.rows.length === 1 ? '' : 's'}`}
          </button>
        </>
      }
    >
      <label className="f">
        CSV
        <textarea
          className="input mono"
          data-autofocus
          rows={8}
          placeholder={'2026-10-28,Public holiday,prod\n2026-11-11,Company day off,prod|staging'}
          value={text}
          onChange={(e) => setText(e.target.value)}
        />
      </label>
      {parsed.errors.length > 0 && (
        <div className="banner yel" role="status" style={{ flexDirection: 'column', alignItems: 'flex-start' }}>
          {parsed.errors.map((e) => <span key={e.line}>Line {e.line}: {e.message}</span>)}
        </div>
      )}
      {failures.length > 0 && (
        <div className="banner red" role="alert" style={{ flexDirection: 'column', alignItems: 'flex-start' }}>
          {failures.map((m) => <span key={m}>{m}</span>)}
        </div>
      )}
    </Dialog>
  );
}

/* ------------------------------------------------------------ weekly preview */

interface Tip {
  day: string;
  seg: PreviewSegment;
}

function tipText(t: Tip): { head: string; body: string } {
  const { seg } = t;
  const range = `${t.day} ${fmtMinutes(seg.fromMin)} – ${fmtMinutes(seg.toMin)}`;
  if (seg.state === 'open') return { head: `${range} · open`, body: seg.rules.length ? `Allowed by: ${seg.rules.join(', ')}` : 'No rule closes this action' };
  if (seg.state === 'deny') {
    if (seg.holiday) return { head: `${range} · closed`, body: `Holiday: ${seg.holiday}` };
    return { head: `${range} · closed`, body: `Rule: ${seg.rules.join(', ')} (deny inside)` };
  }
  return { head: `${range} · closed`, body: 'Outside every allow window' };
}

function WeeklyPreviewCard({
  rules, holidays, envs, clusterEnv, onHoverRules,
}: {
  rules: TimeWindowView[];
  holidays: HolidayView[];
  envs: string[];
  clusterEnv: Record<string, string>;
  onHoverRules: (names: string[]) => void;
}) {
  const now = useNow(60_000);
  const [env, setEnv] = useState('');
  const [action, setAction] = useState<Action>('exec');
  const [tip, setTip] = useState<Tip | null>(null);
  const effectiveEnv = env || (envs.includes('prod') ? 'prod' : (envs[0] ?? ''));

  const preview = useMemo(
    () => buildWeeklyPreview({ rules, holidays, environment: effectiveEnv, action, now, tz: DEFAULT_TZ, clusterEnv }),
    [rules, holidays, effectiveEnv, action, now, clusterEnv],
  );

  const hover = (t: Tip | null) => {
    setTip(t);
    onHoverRules(t ? t.seg.rules : []);
  };
  const pct = (min: number) => `${(min / 1440) * 100}%`;
  const nowLabel = `${pad2(Math.floor(preview.nowMinute / 60))}:${pad2(preview.nowMinute % 60)}`;
  const tt = tip ? tipText(tip) : null;

  return (
    <div className="card" style={{ flex: '1 1 460px', minWidth: 0 }}>
      <div className="hd">
        Weekly preview <small>all rules combined</small>
        <span className="grow" />
        <select className="sel" aria-label="Environment" value={effectiveEnv} onChange={(e) => setEnv(e.target.value)}>
          {envs.length === 0 && <option value="">(any)</option>}
          {envs.map((e) => <option key={e} value={e}>{e}</option>)}
        </select>
        <select className="sel" aria-label="Action" value={action} onChange={(e) => setAction(e.target.value as Action)}>
          {ACTIONS.map((a) => <option key={a} value={a}>{a}</option>)}
        </select>
      </div>
      <div className="bd col" style={{ gap: 12 }}>
        <div className="wk" role="group" aria-label={`Weekly preview for ${action} in ${effectiveEnv || 'any environment'}, times in ${DEFAULT_TZ}`}>
          <div />
          {preview.days.map((d) => (
            <div key={d.date} className={`hd2${d.isToday ? ' today' : ''}`}>
              {d.label}
              <b>{d.dayOfMonth}{d.isToday ? ' · today' : ''}</b>
            </div>
          ))}
          <div className="hrs" aria-hidden="true">
            {[0, 4, 8, 12, 16, 20].map((h) => (
              <span key={h} style={{ top: pct(h * 60) }}>{pad2(h)}</span>
            ))}
          </div>
          {preview.days.map((d, i) => (
            <div key={d.date} className="day">
              {d.segments.map((s) => (
                <div
                  key={s.fromMin}
                  className={`seg-btn ${s.state === 'open' ? 'seg-open' : s.state === 'deny' ? 'seg-deny' : ''}`}
                  style={{ position: 'absolute', left: 0, right: 0, top: pct(s.fromMin), height: pct(s.toMin - s.fromMin) }}
                  role="img"
                  aria-label={`${tipText({ day: d.label, seg: s }).head}. ${tipText({ day: d.label, seg: s }).body}`}
                  tabIndex={s.state === 'closed' ? undefined : 0}
                  onMouseEnter={() => hover({ day: d.label, seg: s })}
                  onMouseLeave={() => hover(null)}
                  onFocus={() => hover({ day: d.label, seg: s })}
                  onBlur={() => hover(null)}
                />
              ))}
              {i === preview.nowDayIndex && <div className="nowline" style={{ top: pct(preview.nowMinute) }} />}
            </div>
          ))}
        </div>
        <ul className="sr-only">
          {preview.days.map((d) => (
            <li key={d.date}>
              {d.label} {d.dayOfMonth}: {d.segments.map((s) => `${s.state === 'open' ? 'open' : 'closed'} ${fmtMinutes(s.fromMin)} to ${fmtMinutes(s.toMin)}`).join(', ')}
            </li>
          ))}
        </ul>
        <div className="row wrap" style={{ gap: 16, fontSize: 12, color: 'var(--tx2)' }}>
          <span className="row" style={{ gap: 6 }}><i className="swatch" style={{ background: 'var(--grnbg)', border: '1px solid var(--grn)' }} />Open</span>
          <span className="row" style={{ gap: 6 }}><i className="swatch" style={{ background: 'var(--redbg)', border: '1px solid var(--red)' }} />Closed by a deny rule or holiday</span>
          <span className="row" style={{ gap: 6 }}><i className="swatch" style={{ background: 'var(--sf2)', border: '1px solid var(--ln)' }} />Closed (outside allow window)</span>
          <span className="row" style={{ gap: 6 }}><i className="swatch" style={{ height: 2, background: 'var(--ac)' }} />Now {nowLabel}</span>
          <span className="grow" />
          {tt && (
            <div className="tip" style={{ maxWidth: 340 }} role="status">
              {tt.head}<br /><span style={{ opacity: 0.85 }}>{tt.body}</span>
            </div>
          )}
        </div>
        <div className="muted" style={{ fontSize: 12 }}>
          Times in {DEFAULT_TZ}. Admins are never limited by windows; a break-glass session overrides them.
          {preview.unresolvedRules.length > 0 && (
            <> Not drawn, because they depend on the target&apos;s team, Argo project or cluster: {preview.unresolvedRules.map((r) => r.name).join(', ')}.</>
          )}
        </div>
      </div>
    </div>
  );
}

/* --------------------------------------------------------------------- page */

export function TimeWindows() {
  const { isAdmin } = useAuth();
  const toast = useToast();
  const now = useNow(60_000);
  const rules = useApi(() => api.timeWindows(), []);
  const holidays = useApi(() => api.holidays(), []);
  const clusters = useApi(() => api.clusters(), []);
  const teams = useApi(() => api.teams(), []);
  const projects = useApi(() => api.argoProjects(), [], { enabled: isAdmin && ARGO_UI });

  const [ruleDlg, setRuleDlg] = useState<{ rule: TimeWindowView | null } | null>(null);
  const [delRule, setDelRule] = useState<TimeWindowView | null>(null);
  const [holDlg, setHolDlg] = useState<{ holiday: HolidayView | null } | null>(null);
  const [delHol, setDelHol] = useState<HolidayView | null>(null);
  const [importOpen, setImportOpen] = useState(false);
  const [hoverNames, setHoverNames] = useState<string[]>([]);

  const ruleList = useMemo(() => rules.data ?? [], [rules.data]);
  const holList = useMemo(
    () => [...(holidays.data ?? [])].sort((a, b) => a.date.localeCompare(b.date) || a.title.localeCompare(b.title)),
    [holidays.data],
  );
  const envs = useMemo(
    () =>
      uniq([
        ...(clusters.data ?? []).map((c) => c.environment),
        ...ruleList.filter((r) => r.scopeKind === 'environment').map((r) => r.scopeValue),
        ...holList.flatMap((h) => h.environments),
      ]),
    [clusters.data, ruleList, holList],
  );
  const clusterEnv = useMemo(() => {
    const m: Record<string, string> = {};
    for (const c of clusters.data ?? []) if (c.environment) m[c.name] = c.environment;
    return m;
  }, [clusters.data]);

  const header = (
    <PageHeader title="Time windows & holidays" subtitle="Rules combine: a window can allow only inside or deny inside. Admins are never limited by windows.">
      {isAdmin ? (
        <button type="button" className="btn pri" onClick={() => setRuleDlg({ rule: null })}>
          <Icon name="plus" />
          New rule
        </button>
      ) : (
        <ReadOnlyNote />
      )}
    </PageHeader>
  );

  if (rules.error) return <>{header}<ErrorBanner error={rules.error} onRetry={rules.reload} /></>;
  if (rules.loading && !rules.data) return <>{header}<Loading what="time windows" /></>;

  const reloadAll = () => {
    rules.reload();
    holidays.reload();
  };

  return (
    <>
      {header}

      <div className="card">
        {ruleList.length === 0 ? (
          <Empty>No rules yet. Without rules every action is open (holidays still apply).</Empty>
        ) : (
          <div className="tbl-wrap">
            <table className="tbl">
              <thead>
                <tr>
                  <th>Rule</th><th>Scope</th><th>Actions</th><th>Days</th><th>Hours</th><th>Timezone</th><th>Effect</th>
                  {isAdmin && <th><span className="sr-only">Edit and delete</span></th>}
                </tr>
              </thead>
              <tbody>
                {ruleList.map((r) => (
                  <tr key={r.id} className={`${r.enabled ? '' : 'off'}${hoverNames.includes(r.name) ? ' sel' : ''}`}>
                    <td>
                      <b>{r.name}</b>{' '}
                      {!r.enabled && <span className="chip">Disabled</span>}
                    </td>
                    <td><ScopeCell r={r} /></td>
                    <td className="mono">{r.actions.filter((a) => a !== 'logs').join(' · ')}</td>
                    <td>{fmtDays(r.daysOfWeek)}</td>
                    <td className="mono">
                      {r.startTime} – {r.endTime}
                      {(parseHHMM(r.endTime) ?? 0) <= (parseHHMM(r.startTime) ?? 0) && <span className="muted"> (+1)</span>}
                    </td>
                    <td className="muted">{r.timezone}</td>
                    <td><EffectChip effect={r.effect} /></td>
                    {isAdmin && (
                      <td style={{ whiteSpace: 'nowrap' }}>
                        <button type="button" className="btn sm" aria-label={`Edit rule ${r.name}`} onClick={() => setRuleDlg({ rule: r })}>
                          <Icon name="edit" />
                        </button>{' '}
                        <button type="button" className="btn sm" aria-label={`Delete rule ${r.name}`} onClick={() => setDelRule(r)}>
                          <Icon name="trash" />
                        </button>
                      </td>
                    )}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>

      <div className="two">
        <WeeklyPreviewCard
          rules={ruleList}
          holidays={holList}
          envs={envs}
          clusterEnv={clusterEnv}
          onHoverRules={setHoverNames}
        />

        <div className="card" style={{ flex: '0 1 380px', minWidth: 0 }}>
          <div className="hd">
            Holidays
            <span className="grow" />
            {isAdmin && (
              <>
                <button type="button" className="btn sm" onClick={() => setImportOpen(true)}>Import CSV</button>
                <button type="button" className="btn sm" onClick={() => setHolDlg({ holiday: null })}>
                  <Icon name="plus" />
                  Add
                </button>
              </>
            )}
          </div>
          {holidays.error ? (
            <div className="bd"><ErrorBanner error={holidays.error} onRetry={holidays.reload} /></div>
          ) : holList.length === 0 ? (
            <Empty>No holidays.</Empty>
          ) : (
            <div className="tbl-wrap">
              <table className="tbl">
                <thead>
                  <tr><th>Date</th><th>Title</th><th>Applies to</th>{isAdmin && <th><span className="sr-only">Edit and delete</span></th>}</tr>
                </thead>
                <tbody>
                  {holList.map((h) => {
                    const past = Date.parse(`${h.date}T23:59:59Z`) < now - 86400_000;
                    return (
                      <tr key={h.id} className={past ? 'off' : ''}>
                        <td className="mono" style={{ whiteSpace: 'nowrap' }}>{holidayDate(h.date, now)}</td>
                        <td>{h.title}</td>
                        <td>
                          {h.environments.length === 0 ? (
                            <span className="chip">All</span>
                          ) : (
                            <span className="row wrap" style={{ gap: 4 }}>
                              {h.environments.map((e) => <EnvBadge key={e} env={e} />)}
                            </span>
                          )}
                        </td>
                        {isAdmin && (
                          <td style={{ whiteSpace: 'nowrap' }}>
                            <button type="button" className="btn sm" aria-label={`Edit holiday ${h.title}`} onClick={() => setHolDlg({ holiday: h })}>
                              <Icon name="edit" />
                            </button>{' '}
                            <button type="button" className="btn sm" aria-label={`Delete holiday ${h.title}`} onClick={() => setDelHol(h)}>
                              <Icon name="trash" />
                            </button>
                          </td>
                        )}
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )}
        </div>
      </div>

      {isAdmin && ruleDlg && (
        <RuleDialog
          rule={ruleDlg.rule}
          envs={envs}
          clusters={clusters.data ?? []}
          teams={teams.data ?? []}
          projects={projects.data ?? []}
          onClose={() => setRuleDlg(null)}
          onSaved={() => { setRuleDlg(null); rules.reload(); }}
        />
      )}
      {isAdmin && delRule && (
        <DeleteDialog
          title={`Delete rule “${delRule.name}”?`}
          subtitle="Access that this rule restricted changes immediately; open sessions are re-evaluated within 30 seconds."
          onClose={() => setDelRule(null)}
          onConfirm={async () => {
            await api.deleteTimeWindow(delRule.id);
            toast('Rule deleted.');
            setDelRule(null);
            rules.reload();
          }}
        />
      )}
      {isAdmin && holDlg && (
        <HolidayDialog
          holiday={holDlg.holiday}
          envs={envs}
          onClose={() => setHolDlg(null)}
          onSaved={() => { setHolDlg(null); holidays.reload(); }}
        />
      )}
      {isAdmin && delHol && (
        <DeleteDialog
          title={`Delete holiday “${delHol.title}”?`}
          subtitle={`${holidayDate(delHol.date, now)} will no longer close ${delHol.environments.length ? delHol.environments.join(', ') : 'any environment'}.`}
          onClose={() => setDelHol(null)}
          onConfirm={async () => {
            await api.deleteHoliday(delHol.id);
            toast('Holiday deleted.');
            setDelHol(null);
            holidays.reload();
          }}
        />
      )}
      {isAdmin && importOpen && <ImportDialog onClose={() => setImportOpen(false)} onDone={reloadAll} />}
    </>
  );
}
