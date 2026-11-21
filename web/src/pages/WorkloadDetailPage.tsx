import { useMemo, useState } from 'react';
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom';
import { api, kindFromSegment } from '../api';
import type { K8sEvent, LiveChange, Pod, WorkloadDetail } from '../api';
import { AccessHints } from '../components/AccessHints';
import { ActionButton } from '../components/ActionButton';
import { BreakGlassMark, EnvBadge, JobStatusChip, KindTag, PhaseChip } from '../components/Badges';
import { Bump } from '../components/Bump';
import { CronJobsPanel, ResultStrip, ScheduleText } from '../components/CronJobsPanel';
import { ErrorBanner, Loading } from '../components/Feedback';
import { JobsTable } from '../components/JobsTable';
import { Icon } from '../components/Icon';
import { DeletePodDialog, RestartDialog, RunCronDialog } from '../components/OperationDialogs';
import { Crumbs } from '../components/PageHeader';
import { WhoHasAccess } from '../components/WhoHasAccess';
import { useAuth } from '../context/auth';
import { useLive } from '../context/live';
import { useSetShellCtx } from '../context/shell';
import { eventKey, eventSig, rowClass, useAnimatedList } from '../hooks/useAnimatedList';
import { useApi } from '../hooks/useApi';
import { useCronJobs } from '../hooks/useCronJobs';
import { useNow } from '../hooks/useNow';
import { closesSoonLabel, explainDecision } from '../lib/decision';
import { execUrl, logsUrl, namespaceUrl } from '../lib/routes';
import { jobRows } from '../lib/jobs';
import { fmtAge, fmtDateTime, fmtRelative } from '../lib/time';
import { ARGO_UI } from '../lib/features';
import { breakGlassKey } from '../lib/breakGlass';

function Stat({ label, children, mono, small }: { label: string; children: React.ReactNode; mono?: boolean; small?: boolean }) {
  return (
    <div className="stat">
      <span className="lbl">{label}</span>
      <b className={mono ? 'mono' : undefined} style={small ? { fontSize: mono ? 12 : 14, lineHeight: '26px' } : undefined}>{children}</b>
    </div>
  );
}

const NO_PODS: Pod[] = [];

const podChange = (c: LiveChange) => c.type !== 'event';
const eventChange = (c: LiveChange) => c.type === 'event' || c.type === 'pod_restarted';

export function WorkloadDetailPage() {
  const { cluster = '', ns = '', kind: kindSeg = '', name = '' } = useParams();
  const kind = kindFromSegment(kindSeg);
  const [sp] = useSearchParams();
  // A CronJob has no Pods tab: it opens on its job history.
  const tab = sp.get('tab') === 'events' ? 'events' : sp.get('tab') === 'cronjobs' || kind === 'CronJob' ? 'cronjobs' : 'pods';
  const { me, usesGrants } = useAuth();
  const now = useNow(30000);
  const ref = { cluster, namespace: ns, kind, name };
  // Live changes refresh what they touch; polling is the fallback.
  const live = useLive({ cluster, namespace: ns, kind, name }, (changes) => {
    const all = changes.length === 0;
    if (all || changes.some(podChange)) {
      wl.reload();
      pods.reload();
    }
    if (all || changes.some(eventChange)) events.reload();
  });
  const wl = useApi(() => api.workload(ref), [cluster, ns, kind, name, breakGlassKey(me)], { poll: live ? 60000 : 30000 });
  useSetShellCtx(wl.data ? cluster : undefined, wl.data?.environment);
  const [restart, setRestart] = useState(false);
  const [deletePod, setDeletePod] = useState<string | null>(null);
  const [running, setRunning] = useState(false);
  const cron = useCronJobs(cluster, ns, { only: name, enabled: kind === 'CronJob' });
  const cj = cron.data?.find((c) => c.name === name);
  const navigate = useNavigate();

  const d = wl.data;
  const logsOk = !!d?.access.logs.allowed;
  const pods = useApi(() => api.pods(ref), [cluster, ns, kind, name, logsOk, breakGlassKey(me)], { enabled: logsOk && kind !== 'CronJob' && tab !== 'cronjobs', poll: live ? 60000 : 10000 });
  const events = useApi(() => api.events(ref), [cluster, ns, kind, name, logsOk], { enabled: logsOk });
  const [sel, setSel] = useState<string | null>(null);

  const podList = pods.data ?? NO_PODS;
  const selected = useMemo(() => podList.find((p) => p.name === sel) ?? podList.find((p) => p.lastTermination) ?? null, [podList, sel]);

  if (wl.loading && !d) return <Loading what="workload" />;
  if (wl.error && !d) return <ErrorBanner error={wl.error} onRetry={wl.reload} />;
  if (!d) return null;

  const bgActive = Object.values(d.access).some((x) => x.breakGlassId);
  const closes = closesSoonLabel(d.access.exec.allowed ? d.access.exec : d.access.logs, now);
  const isCron = d.kind === 'CronJob';
  const wlRequest = { cluster: d.cluster, namespace: d.namespace, workload: { kind: d.kind, name: d.name } };

  return (
    <>
      <Crumbs items={[
        { label: cluster, to: '/' },
        { label: ns, to: namespaceUrl(cluster, ns), mono: true },
        ...(ARGO_UI && d.argoApp ? [{ label: d.argoApp, to: `/?q=${encodeURIComponent(d.argoApp)}` }] : []),
        { label: name, mono: true },
      ]} />
      <div className="row wrap" style={{ alignItems: 'flex-start', gap: 16 }}>
        <div className="col" style={{ gap: 6, flex: 1, minWidth: 260 }}>
          <div className="row wrap">
            <KindTag kind={d.kind} />
            <h1 className="mono" style={{ margin: 0, fontSize: 20, fontWeight: 600, overflowWrap: 'anywhere' }}>{d.name}</h1>
            <EnvBadge env={d.environment} />
            {bgActive && <BreakGlassMark />}
            {closes && <span className="chip warn"><Icon name="clock" size={12} />{closes}</span>}
          </div>
          <div className="row wrap muted" style={{ gap: 14 }}>
            <span>Namespace <b className="mono" style={{ color: 'var(--tx)' }}>{d.namespace}</b></span>
            <span>Cluster <b className="mono" style={{ color: 'var(--tx)' }}>{d.cluster}</b></span>
            {ARGO_UI && d.argoApp && <span>Argo app <b className="mono" style={{ color: 'var(--tx)' }}>{d.argoApp}</b></span>}
          </div>
          <AccessHints access={d.access} target={d.name} actions={isCron ? ['logs', 'run-job'] : ['logs', 'exec', 'restart']} request={wlRequest} />
        </div>
        <div className="row wrap">
          <WhoHasAccess cluster={d.cluster} namespace={d.namespace} kind={d.kind} name={d.name} />
          {!isCron && <ActionButton action="restart" decision={d.access.restart} label="Restart" icon="restart" target={d.name} hint="tip" request={wlRequest} onClick={() => setRestart(true)} />}
          {isCron && <ActionButton action="run-job" decision={d.access['run-job']} label="Run now" icon="bolt" target={d.name} hint="tip" request={wlRequest} onClick={() => setRunning(true)} />}
        </div>
      </div>

      {isCron && cj && (
        <div className="row wrap" style={{ alignItems: 'stretch' }}>
          <Stat label="Schedule" small><ScheduleText cron={cj} /></Stat>
          <Stat label="Next run" small>
            {cj.suspended ? <span className="chip warn">Suspended</span> : cj.nextScheduleAt ? <span title={fmtDateTime(cj.nextScheduleAt)}>{fmtRelative(cj.nextScheduleAt, now)}</span> : '—'}
          </Stat>
          <Stat label="Last result" small>
            <span className="row" style={{ gap: 8, flexWrap: 'nowrap' }}>{cj.lastResult ? <JobStatusChip status={cj.lastResult} /> : '—'}<ResultStrip jobs={cj.recentJobs} /></span>
          </Stat>
          <Stat label="Last success" small>{cj.lastSuccessfulAt ? <span title={fmtDateTime(cj.lastSuccessfulAt)}>{fmtRelative(cj.lastSuccessfulAt, now)}</span> : 'Never'}</Stat>
          <Stat label="Concurrency" small>{cj.concurrencyPolicy}</Stat>
          <Stat label="History kept" small>
            <span title="Kubernetes deletes older Jobs and their pods beyond these limits.">{cj.successfulJobsHistoryLimit} succeeded · {cj.failedJobsHistoryLimit} failed</span>
          </Stat>
          {d.images[0] && <Stat label="Image" mono small>{d.images[0]}</Stat>}
        </div>
      )}

      {!isCron && (
        <div className="row wrap">
          <Stat label="Ready"><Bump value={`${d.ready} / ${d.desired}`} /></Stat>
          <Stat label="Updated"><Bump value={`${d.updated} / ${d.desired}`} /></Stat>
          <Stat label="Strategy" small>{d.strategy}</Stat>
          <Stat label="Last rollout" small>{d.lastRolloutAt ? fmtRelative(d.lastRolloutAt, now) : '—'}</Stat>
          {d.images[0] && <Stat label="Image" mono small>{d.images[0]}</Stat>}
        </div>
      )}

      <div className="two">
        <div className="col grow" style={{ gap: 0, minWidth: 0 }}>
          <div className="tabs" role="tablist">
            {!isCron && <Link role="tab" aria-selected={tab === 'pods'} className={tab === 'pods' ? 'on' : undefined} to="?tab=pods" replace>Pods {pods.data && <span className="c"><Bump value={podList.length} /></span>}</Link>}
            {isCron && <Link role="tab" aria-selected={tab === 'cronjobs'} className={tab === 'cronjobs' ? 'on' : undefined} to="?tab=cronjobs" replace>Job history {cj && <span className="c"><Bump value={cj.recentJobs.length} /></span>}</Link>}
            <Link role="tab" aria-selected={tab === 'events'} className={tab === 'events' ? 'on' : undefined} to="?tab=events" replace>Events</Link>
            {!isCron && d.cronJobCount > 0 && <Link role="tab" aria-selected={tab === 'cronjobs'} className={tab === 'cronjobs' ? 'on' : undefined} to="?tab=cronjobs" replace>CronJobs <span className="c">{d.cronJobCount}</span></Link>}
          </div>
          {tab === 'cronjobs' && isCron ? (
            <div style={{ paddingTop: 12 }}>
              {cron.error ? <ErrorBanner error={cron.error} onRetry={cron.reload} />
                : !cron.data ? <Loading what="jobs" />
                  : !cj ? <div className="card"><div className="empty">This CronJob was not found.</div></div>
                    : <JobsTable cluster={cluster} namespace={ns} jobs={jobRows([cj])} highlight={sp.get('job')} />}
            </div>
          ) : tab === 'cronjobs' ? (
            <div style={{ paddingTop: 12 }}><CronJobsPanel cluster={cluster} namespace={ns} /></div>
          ) : !logsOk ? (
            <div className="card" style={{ borderTop: 0, borderRadius: '0 0 8px 8px' }}>
              <div className="empty">
                {explainDecision(d.access.logs, { action: 'logs', target: d.name, canBreakGlass: usesGrants })?.text ?? 'Pods are not available.'}
              </div>
            </div>
          ) : tab === 'events' ? (
            <EventsTable events={events.data} error={events.error} loading={events.loading} now={now} reload={events.reload} scope={`${cluster}/${ns}/${kind}/${name}`} />
          ) : (
            <PodsTable
              pods={pods.data} loading={pods.loading && !pods.data} error={pods.error} reload={pods.reload} now={now} selected={selected?.name ?? null}
              onSelect={setSel} onDelete={setDeletePod} wl={d}
            />
          )}
        </div>

        {tab === 'pods' && selected && (
          <PodPanel pod={selected} events={events.data ?? []} wl={d} now={now} onClose={() => setSel('')} onDelete={setDeletePod} />
        )}
      </div>
      {running && (
        <RunCronDialog
          cron={d}
          onClose={(changed) => { setRunning(false); if (changed) { wl.reload(); cron.reload(); } }}
          onViewRunning={() => navigate('?tab=cronjobs&result=running', { replace: true })}
        />
      )}
      {restart && <RestartDialog wl={d} onClose={(changed) => { setRestart(false); if (changed) { wl.reload(); pods.reload(); } }} />}
      {deletePod && <DeletePodDialog wl={d} pod={deletePod} onClose={(changed) => {
        setDeletePod(null);
        if (!changed) return;
        if (selected?.name === deletePod) setSel('');
        pods.reload();
      }} />}
    </>
  );
}

function PodsTable({ pods, loading, error, reload, now, selected, onSelect, onDelete, wl }: {
  pods: Pod[] | undefined; loading: boolean; error: unknown; reload: () => void; now: number; selected: string | null; onSelect: (n: string) => void; onDelete: (n: string) => void; wl: WorkloadDetail;
}) {
  const { debugEnabled } = useAuth().me.settings;
  const rows = useAnimatedList(pods, (p) => p.name, (p) => `${p.phase}|${p.ready}|${p.restarts}|${p.lastTermination}`, `${wl.cluster}/${wl.namespace}/${wl.kind}/${wl.name}`);
  if (loading) return <div className="card" style={{ borderTop: 0 }}><div style={{ padding: 16 }}><div className="skel" style={{ width: '80%', marginBottom: 8 }} /><div className="skel" style={{ width: '60%' }} /></div></div>;
  if (error) return <ErrorBanner error={error} onRetry={reload} />;
  const loc = { cluster: wl.cluster, namespace: wl.namespace, kind: wl.kind, name: wl.name };
  return (
    <div className="card" style={{ borderTop: 0, borderRadius: '0 0 8px 8px' }}>
      <div className="tbl-wrap">
        <table className="tbl nowrap">
          <thead><tr><th>Pod</th><th>Phase</th><th>Ready</th><th>Restarts</th><th>Age</th><th>Node</th><th>Last termination</th><th style={{ textAlign: 'right' }}>Actions</th></tr></thead>
          <tbody>
            {rows.length === 0 && <tr><td colSpan={8} className="empty">No pods.</td></tr>}
            {rows.map(({ item: p, anim }) => (
              <tr key={p.name} className={rowClass(anim, `click${selected === p.name ? ' sel' : ''}`)} onClick={() => onSelect(p.name)}>
                <td className="mono"><button type="button" className="linklike blue mono" style={{ fontWeight: 400 }} onClick={() => onSelect(p.name)} aria-label={`Details of ${p.name}`}>{p.name}</button></td>
                <td><PhaseChip phase={p.phase} /></td>
                <td className="mono"><Bump value={p.ready} /></td>
                <td>{p.restarts > 0 ? <b><Bump value={p.restarts} /></b> : 0}</td>
                <td>{fmtAge(p.createdAt, now)}</td>
                <td className="mono">{p.node}</td>
                <td>{p.lastTermination ? <><span className="chip err">{p.lastTermination}</span> <span className="muted">{fmtRelative(p.lastTerminatedAt, now)}</span></> : <span className="muted">—</span>}</td>
                <td style={{ textAlign: 'right' }} onClick={(e) => e.stopPropagation()}>
                  <span className="row" style={{ justifyContent: 'flex-end' }}>
                    <ActionButton action="logs" decision={wl.access.logs} label="Logs" icon="list" size="sm" to={logsUrl(loc, p.name)} target={wl.name} hint="none" />
                    <ActionButton action="exec" decision={wl.access.exec} label="Exec" icon="term" size="sm" to={execUrl(loc, p.name)} target={wl.name} hint="none" />
                    {debugEnabled && <ActionButton action="exec" decision={wl.access.exec} label="Debug" icon="bug" size="sm" plain to={execUrl(loc, p.name, undefined, true)} target={wl.name} hint="none" />}
                    {wl.podDelete && <ActionButton action="restart" decision={wl.access.restart} label="Delete" icon="trash" size="sm" plain onClick={() => onDelete(p.name)} target={wl.name} hint="none" />}
                  </span>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

function EventsTable({ events, error, loading, now, reload, scope }: { events: K8sEvent[] | undefined; error: unknown; loading: boolean; now: number; reload: () => void; scope: string }) {
  const rows = useAnimatedList(events, eventKey, eventSig, scope);
  if (loading && !events) return <Loading what="events" />;
  if (error) return <ErrorBanner error={error} onRetry={reload} />;
  return (
    <div className="card" style={{ borderTop: 0, borderRadius: '0 0 8px 8px' }}>
      <div className="tbl-wrap">
        <table className="tbl nowrap">
          <thead><tr><th>Type</th><th>Reason</th><th>Message</th><th>Object</th><th>Count</th><th>Last seen</th></tr></thead>
          <tbody>
            {rows.length === 0 && <tr><td colSpan={6} className="empty">No events.</td></tr>}
            {rows.map(({ item: e, key, anim }) => (
              <tr key={key} className={rowClass(anim)}>
                <td><span className={`chip ${e.type === 'Warning' ? 'err' : ''}`}>{e.type}</span></td>
                <td><b>{e.reason}</b></td>
                <td className="wrapcell" style={{ minWidth: 240 }}>{e.message}</td>
                <td className="mono">{e.object}</td>
                <td><Bump value={e.count} /></td>
                <td>{fmtRelative(e.lastSeen, now)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

function PodPanel({ pod, events, wl, now, onClose, onDelete }: { pod: Pod; events: K8sEvent[]; wl: WorkloadDetail; now: number; onClose: () => void; onDelete: (n: string) => void }) {
  const { debugEnabled } = useAuth().me.settings;
  const loc = { cluster: wl.cluster, namespace: wl.namespace, kind: wl.kind, name: wl.name };
  const podEvents = events.filter((e) => e.object.endsWith(pod.name.replace(`${wl.name}-`, '')) || e.object.includes(pod.name)).slice(0, 5);
  return (
    <aside className="card" style={{ width: 380, flex: 'none', maxWidth: '100%' }} aria-label="Pod details">
      <div className="hd">
        <span className="mono" style={{ fontSize: 12, overflow: 'hidden', textOverflow: 'ellipsis' }}>{pod.name}</span>
        <span className="grow" />
        <button type="button" className="btn ghost sm" aria-label="Close panel" onClick={onClose}><Icon name="x" /></button>
      </div>
      <div className="bd col" style={{ gap: 14 }}>
        <div className="col" style={{ gap: 8 }}>
          <span className="lbl">Containers</span>
          {pod.containers.map((c) => (
            <div key={c.name} className="col" style={{ border: '1px solid var(--ln)', borderRadius: 6, padding: '10px 12px' }}>
              <div className="row">
                <b className="mono">{c.name}</b>
                <span className={`chip ${c.ready ? 'ok' : 'warn'}`}><span className={`dot ${c.ready ? 'ok' : 'warn'}`} />{c.state}{c.stateReason ? ` · ${c.stateReason}` : ''}</span>
                <span className="grow" />
                <span className="muted">{c.restartCount} restart{c.restartCount === 1 ? '' : 's'}</span>
              </div>
              {c.lastState && (
                <dl className="kv" style={{ gridTemplateColumns: '90px 1fr' }}>
                  <dt>Last state</dt><dd><span className="chip err">{c.lastState} · {c.lastReason}</span></dd>
                  {c.lastExitCode !== null && (<><dt>Exit code</dt><dd className="mono">{c.lastExitCode}</dd></>)}
                  {c.lastFinishedAt && (<><dt>Finished</dt><dd>{fmtRelative(c.lastFinishedAt, now)}{c.lastStartedAt ? ` · ran ${fmtAge(c.lastStartedAt, Date.parse(c.lastFinishedAt))}` : ''}</dd></>)}
                  {c.memoryLimit && (<><dt>Memory limit</dt><dd className="mono">{c.memoryLimit}</dd></>)}
                </dl>
              )}
            </div>
          ))}
        </div>
        <div className="col" style={{ gap: 8 }}>
          <span className="lbl">Pod events</span>
          {podEvents.length === 0 && <span className="muted">No recent events.</span>}
          <div className="col" style={{ gap: 0 }}>
            {podEvents.map((e, i) => (
              <div key={i} className="row" style={{ alignItems: 'flex-start', padding: '8px 0', borderTop: '1px solid var(--ln)' }}>
                <span className={`chip ${e.type === 'Warning' ? 'err' : ''}`} style={{ marginTop: 1 }}>{e.type}</span>
                <div><b>{e.reason}</b><div className="muted">{e.message}</div></div>
                <span className="muted" style={{ marginLeft: 'auto', whiteSpace: 'nowrap' }}>{fmtRelative(e.lastSeen, now).replace(' ago', '')}</span>
              </div>
            ))}
          </div>
        </div>
        <div className="row">
          <ActionButton action="logs" decision={wl.access.logs} label="Logs" icon="list" to={logsUrl(loc, pod.name)} target={wl.name} hint="none" />
          <ActionButton action="exec" decision={wl.access.exec} label="Exec" icon="term" to={execUrl(loc, pod.name)} target={wl.name} hint="none" />
          {debugEnabled && <ActionButton action="exec" decision={wl.access.exec} label="Debug" icon="bug" plain to={execUrl(loc, pod.name, undefined, true)} target={wl.name} hint="none" />}
          {wl.podDelete && <ActionButton action="restart" decision={wl.access.restart} label="Delete" icon="trash" plain onClick={() => onDelete(pod.name)} target={wl.name} hint="none" />}
        </div>
      </div>
    </aside>
  );
}

