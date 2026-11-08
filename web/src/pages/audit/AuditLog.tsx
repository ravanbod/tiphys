import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import { api, errorText } from '../../api';
import type { Action, AuditEvent } from '../../api';
import { AuditDrawer, AuditSheet } from '../../components/AuditEventPanel';
import { BreakGlassMark, EnvBadge, ResultChip } from '../../components/Badges';
import { Empty, ErrorBanner, Skeleton } from '../../components/Feedback';
import { Icon } from '../../components/Icon';
import { ComboBox } from '../../components/ComboBox';
import { Select } from '../../components/Select';
import type { SelectOption } from '../../components/Select';
import { PageHeader } from '../../components/PageHeader';
import { useApi } from '../../hooks/useApi';
import { useMedia } from '../../hooks/useMedia';
import {
  RANGE_OPTIONS, filtersFromParams, filtersToParams, isDefaultFilters, moreFilterCount, serverFilterKey, toApiFilters,
} from '../../lib/auditFilters';
import type { AuditUiFilters } from '../../lib/auditFilters';
import { KNOWN_EVENT_TYPES, eventTitle, reasonText, targetFull, targetLine } from '../../lib/auditLabels';
import { fmtDay, fmtTime } from '../../lib/time';
import '../../styles/auditpages.css';

const PAGE = 50;
const ACTIONS: Action[] = ['logs', 'exec', 'restart', 'run-job', 'sync', 'rollback'];
const RESULTS = ['allowed', 'denied', 'succeeded', 'failed'];
// ---- data: cursor-paged audit events

interface Pages {
  items: AuditEvent[];
  next: string | null;
  loading: boolean;
  loadingMore: boolean;
  error: unknown;
  /** "now" used for relative ranges; the CSV link uses the same anchor. */
  anchor: number;
}

function useAuditPages(filters: AuditUiFilters) {
  const key = serverFilterKey(filters);
  const fRef = useRef(filters);
  fRef.current = filters;
  const gen = useRef<{ stale: boolean }>({ stale: false });
  const [tick, setTick] = useState(0);
  const [st, setSt] = useState<Pages>({ items: [], next: null, loading: true, loadingMore: false, error: null, anchor: Date.now() });
  const stRef = useRef(st);
  stRef.current = st;

  useEffect(() => {
    const my = { stale: false };
    gen.current = my;
    const anchor = Date.now();
    setSt({ items: [], next: null, loading: true, loadingMore: false, error: null, anchor });
    api
      .audit(toApiFilters(fRef.current, anchor, { limit: PAGE }))
      .then((p) => {
        if (!my.stale) setSt((s) => ({ ...s, items: p.items, next: p.nextCursor, loading: false }));
      })
      .catch((e: unknown) => {
        if (!my.stale) setSt((s) => ({ ...s, error: e, loading: false }));
      });
    return () => {
      my.stale = true;
    };
  }, [key, tick]);

  const loadMore = useCallback(() => {
    const cur = stRef.current;
    if (!cur.next || cur.loadingMore || cur.loading) return;
    const my = gen.current;
    setSt((s) => ({ ...s, loadingMore: true }));
    api
      .audit(toApiFilters(fRef.current, cur.anchor, { limit: PAGE, cursor: cur.next }))
      .then((p) => {
        if (my.stale) return;
        setSt((s) => {
          const seen = new Set(s.items.map((x) => x.id));
          return { ...s, items: [...s.items, ...p.items.filter((x) => !seen.has(x.id))], next: p.nextCursor, loadingMore: false };
        });
      })
      .catch((e: unknown) => {
        if (!my.stale) setSt((s) => ({ ...s, error: e, loadingMore: false }));
      });
  }, []);

  const reload = useCallback(() => setTick((t) => t + 1), []);
  return { ...st, loadMore, reload };
}

// ---- small inputs

/**
 * Text input that commits to the URL after a short pause (or on blur / Enter).
 * With suggestions it is a free-text ComboBox, so they show in the page's font.
 */
function TextFilter({
  value, onCommit, label, placeholder, suggestions, className = 'input', style, type = 'text',
}: {
  value: string;
  onCommit: (v: string) => void;
  label: string;
  placeholder?: string;
  suggestions?: string[];
  className?: string;
  style?: React.CSSProperties;
  type?: string;
}) {
  const [local, setLocal] = useState(value);
  const committed = useRef(value);
  const cb = useRef(onCommit);
  cb.current = onCommit;
  useEffect(() => {
    if (value !== committed.current) {
      committed.current = value;
      setLocal(value);
    }
  }, [value]);
  useEffect(() => {
    if (local === committed.current) return;
    const t = window.setTimeout(() => {
      committed.current = local;
      cb.current(local);
    }, 350);
    return () => window.clearTimeout(t);
  }, [local]);
  const flush = (v = local) => {
    if (v !== committed.current) {
      committed.current = v;
      cb.current(v);
    }
  };
  if (suggestions) {
    return (
      <ComboBox free small label={label} placeholder={placeholder} value={local} onChange={setLocal} onCommit={flush}
        inputStyle={style} options={suggestions.map((x) => ({ value: x }))} />
    );
  }
  return (
    <input
      className={className}
      style={style}
      type={type}
      aria-label={label}
      placeholder={placeholder}
      value={local}
      onChange={(e) => setLocal(e.target.value)}
      onBlur={() => flush()}
      onKeyDown={(e) => {
        if (e.key === 'Enter') flush();
      }}
    />
  );
}

function SearchBox({ value, onCommit, tall }: { value: string; onCommit: (v: string) => void; tall?: boolean }) {
  return (
    <label className="search" style={tall ? { width: '100%', height: 44, background: 'var(--sf)' } : undefined}>
      <Icon name="search" />
      <TextFilter className="" value={value} onCommit={onCommit} label="Search events" placeholder="Search events" />
    </label>
  );
}

interface Suggest {
  users: string[];
  clusters: string[];
  envs: string[];
}

/** Environment filter options: the known ones and the current one, after "Any environment". */
function envOptions(envs: string[], current: string): SelectOption<string>[] {
  const all = current && !envs.includes(current) ? [...envs, current] : envs;
  return [{ value: '', label: 'Any environment' }, ...all.map((e) => ({ value: e, label: e }))];
}

function RangeInputs({ f, set, tall }: { f: AuditUiFilters; set: (p: Partial<AuditUiFilters>) => void; tall?: boolean }) {
  if (f.range !== 'custom') return null;
  const st = tall ? { height: 44 } : undefined;
  return (
    <>
      <TextFilter type="datetime-local" label="From" value={f.from} onCommit={(v) => set({ from: v })} style={st} />
      <TextFilter type="datetime-local" label="To" value={f.to} onCommit={(v) => set({ to: v })} style={st} />
    </>
  );
}

/** eventType / cluster / namespace / session id (hidden behind "+ more filters"). */
function MoreFields({ f, set, suggest, tall }: { f: AuditUiFilters; set: (p: Partial<AuditUiFilters>) => void; suggest: Suggest; tall?: boolean }) {
  const st = tall ? { height: 44 } : undefined;
  return (
    <>
      <label>
        Event type
        <TextFilter label="Event type" suggestions={KNOWN_EVENT_TYPES} placeholder="e.g. exec.start" value={f.eventType} onCommit={(v) => set({ eventType: v })} style={st} />
      </label>
      <label>
        Cluster
        <TextFilter label="Cluster" suggestions={suggest.clusters} placeholder="Any cluster" value={f.cluster} onCommit={(v) => set({ cluster: v })} style={st} />
      </label>
      <label>
        Namespace
        <TextFilter label="Namespace" placeholder="Any namespace" value={f.namespace} onCommit={(v) => set({ namespace: v })} style={st} />
      </label>
      <label>
        Session id
        <TextFilter label="Session id" placeholder="Any session" value={f.sessionId} onCommit={(v) => set({ sessionId: v })} style={st} />
      </label>
    </>
  );
}

// ---- rows

function whenCell(iso: string, now: number) {
  const t = Date.parse(iso);
  const sameDay = fmtDay(t) === fmtDay(now);
  return (
    <>
      {!sameDay && <span className="muted">{fmtDay(t)} </span>}
      {fmtTime(iso, undefined, true)}
    </>
  );
}

function ResultCell({ e }: { e: AuditEvent }) {
  return (
    <span className="row wrap" style={{ gap: 6 }}>
      <ResultChip result={e.result} />
      {e.reason && <span className="muted">{reasonText(e.reason)}</span>}
      {e.breakGlassId !== null && <BreakGlassMark />}
    </span>
  );
}

function AuditTable({ items, selectedId, onSelect, now }: { items: AuditEvent[]; selectedId: number | null; onSelect: (e: AuditEvent) => void; now: number }) {
  return (
    <div className="tbl-wrap">
      <table className="tbl">
        <thead>
          <tr><th>Time</th><th>User</th><th>Action</th><th>Target</th><th>Env</th><th>Result</th></tr>
        </thead>
        <tbody>
          {items.map((e) => (
            <tr key={e.id} className={`click${e.id === selectedId ? ' sel' : ''}`} onClick={() => onSelect(e)}>
              <td className="mono">
                <button type="button" className="au-rowbtn mono" aria-label={`Open details: ${eventTitle(e)} by ${e.username}`} aria-current={e.id === selectedId ? 'true' : undefined}>
                  {whenCell(e.at, now)}
                </button>
              </td>
              <td>{e.username}</td>
              <td title={e.eventType}>{eventTitle(e)}</td>
              <td className="mono au-targetcell" title={targetFull(e) ?? undefined}>{targetLine(e) ?? <span className="muted">—</span>}</td>
              <td><EnvBadge env={e.environment} /></td>
              <td><ResultCell e={e} /></td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function AuditCards({ items, selectedId, onSelect, now }: { items: AuditEvent[]; selectedId: number | null; onSelect: (e: AuditEvent) => void; now: number }) {
  return (
    <div className="col" style={{ gap: 10 }}>
      {items.map((e) => (
        <button key={e.id} type="button" className={`card au-card${e.id === selectedId ? ' sel' : ''}`} onClick={() => onSelect(e)}>
          <div className="row">
            <b>{eventTitle(e)}</b>
            <EnvBadge env={e.environment} />
            <span className="grow" />
            <span className="mono muted">{whenCell(e.at, now)}</span>
          </div>
          <div className="mono muted" style={{ marginTop: 4 }}>{targetLine(e) ?? '—'}</div>
          <div className="row" style={{ marginTop: 8 }}>
            <span>{e.username}</span>
            {e.breakGlassId !== null && <BreakGlassMark />}
            <span className="grow" />
            <span className="row" style={{ gap: 6 }}>
              <ResultChip result={e.result} />
              {e.result === 'denied' && e.reason && <span className="muted">{reasonText(e.reason)}</span>}
            </span>
          </div>
        </button>
      ))}
    </div>
  );
}

// ---- page

export function AuditLog() {
  const [params, setParams] = useSearchParams();
  const phone = useMedia('(max-width:640px)');
  const filters = useMemo(() => filtersFromParams(params), [params]);
  const set = useCallback(
    (patch: Partial<AuditUiFilters>) => {
      setParams((prev) => filtersToParams({ ...filtersFromParams(prev), ...patch }), { replace: true });
    },
    [setParams],
  );

  const pages = useAuditPages(filters);
  const [selected, setSelected] = useState<AuditEvent | null>(null);
  const [showMore, setShowMore] = useState(() => moreFilterCount(filters) > 0);
  const now = Date.now();

  // Filter suggestions: best effort, the page works without them.
  const users = useApi(() => api.users(), []);
  const clusters = useApi(() => api.clusters(), []);
  const suggest: Suggest = useMemo(() => {
    const cl = clusters.data ?? [];
    const envs = Array.from(new Set(cl.map((c) => c.environment).filter((x): x is string => !!x)));
    return {
      users: (users.data ?? []).map((u) => u.username),
      clusters: cl.map((c) => c.name),
      envs,
    };
  }, [users.data, clusters.data]);

  const visible = pages.items;

  // Close the detail if the selected event disappears from the list (filters changed).
  useEffect(() => {
    if (selected && !visible.some((e) => e.id === selected.id)) setSelected(null);
  }, [visible, selected]);

  const csvHref = api.auditCsvUrl(toApiFilters(filters, pages.anchor));
  const rangeLabel = RANGE_OPTIONS.find((r) => r.value === filters.range)?.label.toLowerCase() ?? '';
  const subtitle = pages.loading
    ? 'Newest first'
    : `Newest first · ${visible.length} event${visible.length === 1 ? '' : 's'} loaded${pages.next ? ' (more available)' : ''} · ${rangeLabel}`;
  const isDefault = isDefaultFilters(filters);
  const hidden = moreFilterCount(filters);

  const csvButton = (
    <a
      className={`btn${phone ? ' sm' : ''}`}
      style={phone ? { height: 36 } : undefined}
      href={csvHref}
      download
      title="Up to 10,000 rows"
    >
      <Icon name="down" />{phone ? 'CSV' : 'Export CSV'}
    </a>
  );

  const body = pages.error && pages.items.length === 0 ? (
    <ErrorBanner error={pages.error} onRetry={pages.reload} title="Could not load the audit log" />
  ) : pages.loading ? (
    <div className="card"><Skeleton rows={6} /></div>
  ) : visible.length === 0 ? (
    <div className="card">
      <Empty>
        No events match these filters.
        {!isDefault && (
          <div style={{ marginTop: 10 }}>
            <button type="button" className="btn ghost sm" onClick={() => setParams({}, { replace: true })}>Reset filters</button>
          </div>
        )}
      </Empty>
    </div>
  ) : phone ? (
    <AuditCards items={visible} selectedId={selected?.id ?? null} onSelect={setSelected} now={now} />
  ) : (
    <div className="card">
      <AuditTable items={visible} selectedId={selected?.id ?? null} onSelect={setSelected} now={now} />
    </div>
  );

  const footer = !pages.loading && visible.length > 0 && (pages.next !== null || pages.error != null) && (
    <div className="au-more-row">
      {pages.error != null && <span className="err-text" role="alert">{errorText(pages.error)}</span>}
      {pages.next && (
        <button type="button" className="btn" onClick={pages.loadMore} disabled={pages.loadingMore} style={phone ? { height: 44, width: '100%', justifyContent: 'center' } : undefined}>
          {pages.loadingMore ? 'Loading…' : 'Load more'}
        </button>
      )}
    </div>
  );

  return (
    <>
      <PageHeader title="Audit log" subtitle={subtitle}>
        {csvButton}
      </PageHeader>

      {phone ? (
        <div className="col" style={{ gap: 10 }}>
          <SearchBox tall value={filters.q} onCommit={(v) => set({ q: v })} />
          <div className="au-chips" role="group" aria-label="Filters">
            <Select className="au-chip on" label="Time range" value={filters.range} onChange={(v) => set({ range: v })}
              options={RANGE_OPTIONS.map((r) => ({ value: r.value, label: r.short }))} />
            <Select className={`au-chip${filters.environment ? ' on' : ''}`} label="Environment" value={filters.environment} onChange={(v) => set({ environment: v })}
              options={envOptions(suggest.envs, filters.environment)} />
            <Select className={`au-chip${filters.result ? ' on' : ''}`} label="Result" value={filters.result} onChange={(v) => set({ result: v })}
              options={[{ value: '', label: 'Result' }, ...RESULTS.map((r) => ({ value: r, label: r }))]} />
            <button type="button" className={`au-chip${filters.breakGlassOnly ? ' on' : ''}`} aria-pressed={filters.breakGlassOnly} onClick={() => set({ breakGlassOnly: !filters.breakGlassOnly })}>
              Break-glass
            </button>
            <button type="button" className={`au-chip${showMore || hidden ? ' on' : ''}`} aria-expanded={showMore} onClick={() => setShowMore((v) => !v)}>
              More{hidden ? ` (${hidden})` : ''}
            </button>
          </div>
          <RangeInputs f={filters} set={set} tall />
          {showMore && (
            <div className="col au-more" style={{ alignItems: 'stretch' }}>
              <label>
                User
                <TextFilter label="User" suggestions={suggest.users} placeholder="Any user" value={filters.user} onCommit={(v) => set({ user: v })} style={{ height: 44 }} />
              </label>
              <label>
                Action
                <Select label="Action" style={{ height: 44, width: '100%' }} value={filters.action} onChange={(v) => set({ action: v })}
                  options={[{ value: '', label: 'Any action' }, ...ACTIONS.map((a) => ({ value: a, label: a }))]} />
              </label>
              <MoreFields f={filters} set={set} suggest={suggest} tall />
            </div>
          )}
        </div>
      ) : (
        <div className="col" style={{ gap: 10 }}>
          <div className="au-filters" role="group" aria-label="Filters">
            <Select label="Time range" value={filters.range} onChange={(v) => set({ range: v })} options={RANGE_OPTIONS.map((r) => ({ value: r.value, label: r.label }))} />
            <RangeInputs f={filters} set={set} />
            <TextFilter label="User" suggestions={suggest.users} placeholder="Any user" value={filters.user} onCommit={(v) => set({ user: v })} style={{ width: 150 }} />
            <Select label="Action" value={filters.action} onChange={(v) => set({ action: v })} options={[{ value: '', label: 'Any action' }, ...ACTIONS.map((a) => ({ value: a, label: a }))]} />
            <Select label="Environment" value={filters.environment} onChange={(v) => set({ environment: v })} options={envOptions(suggest.envs, filters.environment)} />
            <Select label="Result" value={filters.result} onChange={(v) => set({ result: v })}
              options={[{ value: '', label: 'Any result' }, ...RESULTS.map((r) => ({ value: r, label: r.charAt(0).toUpperCase() + r.slice(1) }))]} />
            <label className="chk">
              <input type="checkbox" checked={filters.breakGlassOnly} onChange={(e) => set({ breakGlassOnly: e.target.checked })} /> Break-glass only
            </label>
            <SearchBox value={filters.q} onCommit={(v) => set({ q: v })} />
            <button type="button" className="btn ghost sm" aria-expanded={showMore} onClick={() => setShowMore((v) => !v)}>
              {showMore ? 'Hide more filters' : `+ more filters${hidden ? ` (${hidden} active)` : ''}`}
            </button>
            {!isDefault && (
              <button type="button" className="btn ghost sm" onClick={() => setParams({}, { replace: true })}>Reset</button>
            )}
          </div>
          {showMore && (
            <div className="au-more">
              <MoreFields f={filters} set={set} suggest={suggest} />
            </div>
          )}
        </div>
      )}

      {phone ? (
        <>
          {body}
          {footer}
        </>
      ) : (
        <div className="au-split">
          <div className="au-list">
            {body}
            {footer}
          </div>
          {selected && <AuditDrawer event={selected} onClose={() => setSelected(null)} />}
        </div>
      )}
      {phone && selected && <AuditSheet event={selected} onClose={() => setSelected(null)} />}
    </>
  );
}
