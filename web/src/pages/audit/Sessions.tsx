import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Link } from 'react-router-dom';
import { api, errorText } from '../../api';
import type { ExecSessionView } from '../../api';
import { BreakGlassMark, EnvBadge } from '../../components/Badges';
import { Empty, ErrorBanner, Skeleton } from '../../components/Feedback';
import { Icon } from '../../components/Icon';
import { PageHeader } from '../../components/PageHeader';
import { TerminateSessionDialog } from '../../components/TerminateSessionDialog';
import { useAuth } from '../../context/auth';
import { useApi } from '../../hooks/useApi';
import { useNow } from '../../hooks/useNow';
import { endChip } from '../../lib/endReason';
import { podMiddleEllipsis } from '../../lib/auditLabels';
import { fmtDuration, fmtWhen } from '../../lib/time';
import '../../styles/auditpages.css';

const LIMIT = 50;
const POLL_MS = 10_000;

function elapsedSeconds(s: ExecSessionView, now: number): number | null {
  if (s.durationSeconds !== null && !s.active) return s.durationSeconds;
  const t = Date.parse(s.startedAt);
  return Number.isNaN(t) ? null : Math.max(0, Math.floor((now - t) / 1000));
}

function EndCell({ s }: { s: ExecSessionView }) {
  if (s.active) {
    return (
      <span className="chip prod">
        <span className="dot rec" />Open now
      </span>
    );
  }
  const c = endChip(s.endReason);
  return <span className={`chip${c.tone === 'warn' ? ' warn' : c.tone === 'err' ? ' err' : ''}`}>{c.text}</span>;
}

export function Sessions() {
  const { isAdmin, isAuditor } = useAuth();
  const [openOnly, setOpenOnly] = useState(true);
  const [user, setUser] = useState('');
  const [env, setEnv] = useState('');
  const [bgOnly, setBgOnly] = useState(false);
  const [userDebounced, setUserDebounced] = useState('');
  const [extra, setExtra] = useState<ExecSessionView[]>([]);
  const [exhausted, setExhausted] = useState(false);
  const [loadingMore, setLoadingMore] = useState(false);
  const [moreError, setMoreError] = useState<unknown>(null);
  const [terminating, setTerminating] = useState<ExecSessionView | null>(null);

  useEffect(() => {
    const t = window.setTimeout(() => setUserDebounced(user.trim()), 300);
    return () => window.clearTimeout(t);
  }, [user]);

  const filters = useMemo(
    () => ({
      status: openOnly ? ('active' as const) : undefined,
      user: userDebounced || undefined,
      environment: env || undefined,
      breakGlass: bgOnly || undefined,
    }),
    [openOnly, userDebounced, env, bgOnly],
  );
  const fkey = JSON.stringify(filters);

  const first = useApi(() => api.sessions({ ...filters, limit: LIMIT }), [fkey], { poll: POLL_MS });
  // Extra pages are reset whenever the filters change.
  const lastKey = useRef(fkey);
  useEffect(() => {
    if (lastKey.current !== fkey) {
      lastKey.current = fkey;
      setExtra([]);
      setExhausted(false);
      setMoreError(null);
    }
  }, [fkey]);

  const items = useMemo(() => {
    const head = first.data ?? [];
    const seen = new Set(head.map((s) => s.id));
    return [...head, ...extra.filter((s) => !seen.has(s.id))];
  }, [first.data, extra]);

  const hasMore = !exhausted && (extra.length > 0 || (first.data?.length ?? 0) >= LIMIT);
  const anyActive = items.some((s) => s.active);
  const now = useNow(anyActive ? 1000 : 0);

  const loadMore = useCallback(async () => {
    const last = items[items.length - 1];
    if (!last) return;
    setLoadingMore(true);
    setMoreError(null);
    try {
      const page = await api.sessions({ ...filters, limit: LIMIT, before: last.startedAt });
      setExtra((x) => [...x, ...page]);
      if (page.length < LIMIT) setExhausted(true);
    } catch (e) {
      setMoreError(e);
    } finally {
      setLoadingMore(false);
    }
  }, [items, filters]);

  // Environments of the registered clusters (best effort) and of the loaded sessions; the selected one stays.
  const clusters = useApi(() => api.clusters(), []);
  const envs = useMemo(() => {
    const fromClusters = (clusters.data ?? []).map((c) => c.environment);
    return Array.from(new Set([...fromClusters, ...items.map((s) => s.environment), env].filter((x): x is string => !!x)));
  }, [clusters.data, items, env]);
  const nowMs = Date.now();

  return (
    <>
      <PageHeader title="Sessions" subtitle="Every exec session is recorded. Open sessions can be watched live.">
        <label className="chk">
          <input type="checkbox" checked={openOnly} onChange={(e) => setOpenOnly(e.target.checked)} /> Open now
        </label>
      </PageHeader>

      <div className="ss-filters" role="group" aria-label="Filters">
        <input
          className="input"
          style={{ width: 180, height: 28 }}
          aria-label="User"
          placeholder="Any user"
          value={user}
          onChange={(e) => setUser(e.target.value)}
        />
        <select className="sel" aria-label="Environment" value={env} onChange={(e) => setEnv(e.target.value)}>
          <option value="">Any environment</option>
          {envs.map((x) => <option key={x} value={x}>{x}</option>)}
        </select>
        <label className="chk">
          <input type="checkbox" checked={bgOnly} onChange={(e) => setBgOnly(e.target.checked)} /> Break-glass only
        </label>
        <button type="button" className="btn ghost sm" onClick={first.reload} aria-label="Refresh sessions">
          <Icon name="refresh" size={12} />Refresh
        </button>
        <span className="muted" style={{ fontSize: 12 }}>Updates every 10 seconds</span>
      </div>

      {first.error != null && !first.data ? (
        <ErrorBanner error={first.error} onRetry={first.reload} title="Could not load sessions" />
      ) : first.loading && !first.data ? (
        <div className="card"><Skeleton rows={5} /></div>
      ) : (
        <div className="card">
          {first.error != null && (
            <div className="banner yel" role="alert" style={{ borderRadius: 0 }}>Refresh failed: {errorText(first.error)}. Showing the last result.</div>
          )}
          {items.length === 0 ? (
            <Empty>{openOnly ? 'No open sessions right now.' : 'No sessions match these filters.'}</Empty>
          ) : (
            <div className="tbl-wrap">
              <table className="tbl">
                <thead>
                  <tr>
                    <th>User</th><th>Pod</th><th>Container</th><th>Env</th><th>Started</th><th>Duration</th><th>End reason</th>
                    <th style={{ textAlign: 'right' }}><span className="sr-only">Actions</span></th>
                  </tr>
                </thead>
                <tbody>
                  {items.map((s) => {
                    const secs = elapsedSeconds(s, s.active ? now : nowMs);
                    return (
                      <tr key={s.id}>
                        <td>{s.username}</td>
                        <td className="mono" title={`${s.cluster} / ${s.namespace} / ${s.pod}`}>{podMiddleEllipsis(s.pod)}</td>
                        <td className="mono">{s.container}{s.debugContainer && <span className="chip" style={{ marginLeft: 6 }} title={s.debugContainer}>debug</span>}</td>
                        <td><EnvBadge env={s.environment} /></td>
                        <td className="mono" title={s.startedAt}>{fmtWhen(s.startedAt, nowMs)}</td>
                        <td className="mono">{fmtDuration(secs)}</td>
                        <td>
                          <span className="row wrap" style={{ gap: 6 }}>
                            <EndCell s={s} />
                            {s.breakGlassId !== null && <BreakGlassMark />}
                          </span>
                        </td>
                        <td>
                          <span className="ss-actions">
                            {s.active ? (
                              <>
                                <Link className="btn sm" to={`/sessions/${encodeURIComponent(s.id)}/watch`}>
                                  <Icon name="eye" size={12} />Watch live
                                </Link>
                                {isAdmin && (
                                  <button type="button" className="btn dng sm" onClick={() => setTerminating(s)}>
                                    Terminate
                                  </button>
                                )}
                              </>
                            ) : (
                              <Link className="btn sm" to={`/sessions/${encodeURIComponent(s.id)}`}>
                                <Icon name="play" size={12} />Replay
                              </Link>
                            )}
                          </span>
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )}
          {hasMore && items.length > 0 && (
            <div className="au-more-row" style={{ borderTop: '1px solid var(--ln)' }}>
              {moreError != null && <span className="err-text" role="alert">{errorText(moreError)}</span>}
              <button type="button" className="btn" onClick={loadMore} disabled={loadingMore}>
                {loadingMore ? 'Loading…' : 'Load more'}
              </button>
            </div>
          )}
        </div>
      )}

      {isAuditor && <div className="muted" style={{ fontSize: 12 }}>Auditors see this list read-only: no Terminate.</div>}

      {terminating && (
        <TerminateSessionDialog
          session={terminating}
          onClose={() => setTerminating(null)}
          onDone={() => {
            setTerminating(null);
            first.reload();
          }}
        />
      )}
    </>
  );
}
