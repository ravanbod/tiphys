import type { AuditFilters } from '../api/types';

export type RangeKey = '1h' | '24h' | '7d' | '30d' | 'custom';

export const RANGE_OPTIONS: { value: RangeKey; label: string; short: string; ms: number }[] = [
  { value: '1h', label: 'Last hour', short: 'Last 1 h', ms: 3600_000 },
  { value: '24h', label: 'Last 24 hours', short: 'Last 24 h', ms: 24 * 3600_000 },
  { value: '7d', label: 'Last 7 days', short: 'Last 7 d', ms: 7 * 24 * 3600_000 },
  { value: '30d', label: 'Last 30 days', short: 'Last 30 d', ms: 30 * 24 * 3600_000 },
  { value: 'custom', label: 'Custom range', short: 'Custom', ms: 0 },
];

/** UI-level audit filters. Mirrored in the URL search params. */
export interface AuditUiFilters {
  range: RangeKey;
  /** datetime-local value (local time), only for range=custom */
  from: string;
  to: string;
  user: string;
  action: string;
  eventType: string;
  environment: string;
  result: string;
  cluster: string;
  namespace: string;
  sessionId: string;
  /** Only events under some break-glass (API `breakGlass=true`). */
  breakGlassOnly: boolean;
  q: string;
}

export const DEFAULT_RANGE: RangeKey = '24h';

export const EMPTY_AUDIT_FILTERS: AuditUiFilters = {
  range: DEFAULT_RANGE, from: '', to: '', user: '', action: '', eventType: '', environment: '', result: '',
  cluster: '', namespace: '', sessionId: '', breakGlassOnly: false, q: '',
};

// URL param names (short ones kept readable)
const TEXT_KEYS = ['from', 'to', 'user', 'action', 'eventType', 'environment', 'result', 'cluster', 'namespace', 'sessionId', 'q'] as const;

export function filtersFromParams(p: URLSearchParams): AuditUiFilters {
  const f: AuditUiFilters = { ...EMPTY_AUDIT_FILTERS };
  const range = p.get('range');
  if (range && RANGE_OPTIONS.some((r) => r.value === range)) f.range = range as RangeKey;
  for (const k of TEXT_KEYS) f[k] = (p.get(k) ?? '').trim();
  if (f.from || f.to) f.range = 'custom';
  f.breakGlassOnly = p.get('bg') === '1' || p.get('bg') === 'true';
  return f;
}

export function filtersToParams(f: AuditUiFilters): URLSearchParams {
  const p = new URLSearchParams();
  if (f.range !== DEFAULT_RANGE) p.set('range', f.range);
  for (const k of TEXT_KEYS) {
    if (k === 'from' || k === 'to') {
      if (f.range !== 'custom') continue;
    }
    const v = f[k].trim();
    if (v) p.set(k, v);
  }
  if (f.breakGlassOnly) p.set('bg', '1');
  return p;
}

function localToIso(v: string): string | undefined {
  if (!v) return undefined;
  const t = new Date(v);
  return Number.isNaN(t.getTime()) ? undefined : t.toISOString();
}

/** Converts UI filters into the API query. `now` anchors relative ranges. */
export function toApiFilters(f: AuditUiFilters, now: number, extra: { limit?: number; cursor?: string } = {}): AuditFilters {
  const out: AuditFilters = {};
  if (f.range === 'custom') {
    out.from = localToIso(f.from);
    out.to = localToIso(f.to);
  } else {
    const r = RANGE_OPTIONS.find((x) => x.value === f.range);
    if (r && r.ms) out.from = new Date(now - r.ms).toISOString();
  }
  const set = <K extends keyof AuditFilters>(k: K, v: string) => {
    if (v.trim()) (out as Record<string, unknown>)[k] = v.trim();
  };
  set('user', f.user);
  set('action', f.action);
  set('eventType', f.eventType);
  set('environment', f.environment);
  set('result', f.result);
  set('cluster', f.cluster);
  set('namespace', f.namespace);
  set('sessionId', f.sessionId);
  set('q', f.q);
  if (f.breakGlassOnly) out.breakGlass = true;
  if (extra.limit) out.limit = extra.limit;
  if (extra.cursor) out.cursor = extra.cursor;
  return out;
}

/** Number of non-default filters hidden behind "+ more filters". */
export function moreFilterCount(f: AuditUiFilters): number {
  return [f.eventType, f.cluster, f.namespace, f.sessionId].filter((v) => v.trim()).length;
}

/** Stable key that changes when a filter changes (all of them are applied by the server). */
export function serverFilterKey(f: AuditUiFilters): string {
  return JSON.stringify(f);
}

export function isDefaultFilters(f: AuditUiFilters): boolean {
  return filtersToParams(f).toString() === '';
}
