import { useEffect, useRef, useState } from 'react';
import { ApiError, api, errorText, watchOperation } from '../api';
import type { ArgoDeploy, ArgoResourceRef, ArgoSyncProgress, DecisionInfo, JobSummary, NamespaceApp, OperationDone, Pod, RestartProgress, WorkloadSummary } from '../api';
import { useAuth } from '../context/auth';
import { breakGlassOn } from '../lib/breakGlass';
import { appScopeNote, explainDecision } from '../lib/decision';
import { useApi } from '../hooks/useApi';
import { describeCron } from '../lib/cron';
import { fmtDateTime, fmtTime } from '../lib/time';
import { BreakGlassMark, EnvBadge, JobStatusChip, KindTag, isProd } from './Badges';
import { DiffList } from './DiffView';
import { Skeleton } from './Feedback';
import { Dialog } from './Dialog';
import { Icon } from './Icon';

function BgNote({ info }: { info: DecisionInfo | undefined }) {
  if (!info?.breakGlassId) return null;
  return (
    <div className="row">
      <BreakGlassMark />
      <span className="muted">{info.closesAt ? `ends ${fmtTime(info.closesAt)} · ` : ''}outside time window</span>
    </div>
  );
}

function DoneCard({ done, failTitle, okTitle }: { done: OperationDone; failTitle: string; okTitle: string }) {
  const ok = done.outcome === 'succeeded';
  return (
    <div className="card" style={{ borderColor: ok ? 'var(--grn)' : 'var(--red)' }} role="status">
      <div className="bd col" style={{ gap: 8 }}>
        <div className="row">
          <span className={`chip ${ok ? 'ok' : 'err'}`}>{ok ? 'Succeeded' : done.outcome === 'timed_out' ? 'Timed out' : 'Failed'}</span>
          <b>{ok ? okTitle : failTitle}</b>
        </div>
        {done.message && (ok ? <div className="muted">{done.message}</div> : (
          <details>
            <summary style={{ cursor: 'pointer', color: 'var(--ac)', fontWeight: 500 }}>Kubernetes details</summary>
            <div className="muted mono" style={{ marginTop: 6, whiteSpace: 'pre-wrap' }}>{done.message}</div>
          </details>
        ))}
      </div>
    </div>
  );
}

function ProgressBar({ label, v, max }: { label: string; v: number; max: number }) {
  const pct = max > 0 ? Math.min(100, Math.round((v / max) * 100)) : 0;
  return (
    <div className="prog">
      <div className="row"><span>{label}</span><span className="grow" /><span className="mono">{v} / {max}</span></div>
      <div className="bar" role="progressbar" aria-label={label} aria-valuenow={v} aria-valuemin={0} aria-valuemax={max}><i style={{ width: `${pct}%` }} /></div>
    </div>
  );
}

export function RestartDialog({ wl, onClose }: { wl: WorkloadSummary; onClose: (changed: boolean) => void }) {
  const info = wl.access.restart;
  const prod = isProd(wl.environment);
  const [typed, setTyped] = useState('');
  const [phase, setPhase] = useState<'confirm' | 'progress'>('confirm');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const [prog, setProg] = useState<RestartProgress>({ desired: wl.desired, updated: 0, ready: 0, available: 0 });
  const [done, setDone] = useState<OperationDone | null>(null);
  const [pods, setPods] = useState<Pod[]>([]);
  const stop = useRef<(() => void) | null>(null);
  const ref = { cluster: wl.cluster, namespace: wl.namespace, kind: wl.kind, name: wl.name };

  useEffect(() => () => stop.current?.(), []);
  useEffect(() => {
    if (phase !== 'progress' || done) return;
    let alive = true;
    const load = () => api.pods(ref).then((p) => alive && setPods(p)).catch(() => undefined);
    void load();
    const t = window.setInterval(load, 3000);
    return () => { alive = false; window.clearInterval(t); };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [phase, done]);

  const canGo = !prod || typed === wl.name;

  async function go() {
    if (!canGo || busy) return;
    setBusy(true);
    setErr(null);
    try {
      const { operationId } = await api.restart(ref);
      setPhase('progress');
      stop.current = watchOperation<RestartProgress>(operationId, {
        onProgress: setProg,
        onDone: (d) => { setDone(d); setBusy(false); },
        onError: () => { setDone({ outcome: 'failed', message: 'Lost the progress stream. Check the pods list for the current state.' }); setBusy(false); },
      });
    } catch (e) {
      setErr(e);
      setBusy(false);
    }
  }

  if (phase === 'progress') {
    return (
      <Dialog
        onClose={() => onClose(true)} tone={info?.breakGlassId ? 'bg' : undefined} title={done ? `Restart of ${wl.name}` : `Restarting ${wl.name}`}
        subtitle={<span className="row"><span className="mono" style={{ color: 'var(--tx)' }}>{wl.cluster} / {wl.name}</span><EnvBadge env={wl.environment} /></span>}
        footer={<button type="button" className="btn" data-autofocus onClick={() => onClose(true)}>Close</button>}
      >
        <div className="col" style={{ gap: 10 }} aria-live="polite">
          <ProgressBar label="Updated" v={prog.updated} max={prog.desired} />
          <ProgressBar label="Ready" v={prog.ready} max={prog.desired} />
          <ProgressBar label="Available" v={prog.available} max={prog.desired} />
        </div>
        {pods.length > 0 && !done && (
          <div className="col" style={{ gap: 0, border: '1px solid var(--ln)', borderRadius: 6 }}>
            {pods.map((p, i) => {
              const ready = p.ready.split('/')[0] === p.ready.split('/')[1] && p.phase === 'Running';
              return (
                <div key={p.name} className="row" style={{ padding: '8px 12px', borderTop: i ? '1px solid var(--ln)' : undefined }}>
                  <span className={`dot ${ready ? 'ok' : 'info'}`} />
                  <span className="mono">{p.name}</span>
                  <span className="grow" />
                  <span className={`chip ${ready ? 'ok' : 'info'}`}>{ready ? 'Ready' : 'Starting'}</span>
                </div>
              );
            })}
          </div>
        )}
        {done && <DoneCard done={done} okTitle={`${wl.name} restarted`} failTitle={`${wl.name} did not become ready`} />}
      </Dialog>
    );
  }

  return (
    <Dialog
      onClose={() => onClose(false)} tone={info?.breakGlassId ? 'bg' : prod ? 'prod' : undefined} title={`Restart ${wl.name}?`}
      subtitle="Every pod in this workload is replaced, one after another."
      footer={
        <>
          <button type="button" className="btn" onClick={() => onClose(false)}>Cancel</button>
          <button type="button" className={`btn act${canGo && !busy ? '' : ' dis'}`} aria-disabled={!canGo || busy} onClick={() => void go()}>
            <Icon name="restart" />Restart
          </button>
        </>
      }
    >
      <BgNote info={info} />
      <dl className="kv" style={{ gridTemplateColumns: '90px 1fr' }}>
        <dt>Kind</dt><dd><KindTag kind={wl.kind} /></dd>
        <dt>Name</dt><dd className="mono">{wl.name}</dd>
        <dt>Namespace</dt><dd className="mono">{wl.namespace}</dd>
        <dt>Cluster</dt><dd className="mono">{wl.cluster}</dd>
        <dt>Environment</dt><dd><EnvBadge env={wl.environment} /></dd>
      </dl>
      {prod && <div className="banner prod"><Icon name="warn" /><span>This is production. Capacity drops briefly during the rollout.</span></div>}
      {prod && (
        <label className="f">
          <span>Type <b className="mono" style={{ color: 'var(--tx)' }}>{wl.name}</b> to confirm</span>
          <input className="input mono" value={typed} onChange={(e) => setTyped(e.target.value)} aria-label="Type the workload name to confirm" autoComplete="off" spellCheck={false} data-autofocus />
        </label>
      )}
      {err != null && <div className="banner red" role="alert">{errText(err)}</div>}
    </Dialog>
  );
}

/**
 * Confirms and follows a sync of a whole Argo CD application. The portal
 * always syncs to the app's target revision without pruning; production
 * asks for the app's name, like a restart.
 */
export function SyncAppDialog({ app, cluster, namespace, onClose }: { app: NamespaceApp; cluster: string; namespace: string; onClose: (changed: boolean) => void }) {
  const info = app.access.sync;
  const prod = isProd(info.environment);
  const [typed, setTyped] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const [started, setStarted] = useState(false);
  const [prog, setProg] = useState<ArgoSyncProgress | null>(null);
  const [done, setDone] = useState<OperationDone | null>(null);
  const stop = useRef<(() => void) | null>(null);
  useEffect(() => () => stop.current?.(), []);
  const canGo = !prod || typed === app.name;
  const already = err instanceof ApiError && err.code === 'already_running';
  const diff = useApi(() => api.appDiff(cluster, namespace, app.appNamespace, app.name), [cluster, namespace, app.appNamespace, app.name]);

  async function go() {
    if (!canGo || busy) return;
    setBusy(true);
    setErr(null);
    try {
      const { operationId } = await api.syncApp(cluster, namespace, app.appNamespace, app.name);
      setStarted(true);
      stop.current = watchOperation<ArgoSyncProgress>(operationId, {
        onProgress: setProg,
        onDone: (d) => { setDone(d); setBusy(false); },
        onError: () => { setDone({ outcome: 'failed', message: 'Lost the progress stream. Check the application in Argo CD.' }); setBusy(false); },
      });
    } catch (e) {
      setErr(e);
      setBusy(false);
    }
  }

  const subtitle = <span className="row"><span className="mono" style={{ color: 'var(--tx)' }}>{cluster} / {app.name}</span><EnvBadge env={info.environment} /></span>;
  if (started) {
    return (
      <ArgoProgressDialog what="Sync" verb="synced" name={app.name} info={info} subtitle={subtitle} prog={prog} done={done} onClose={() => onClose(true)} />
    );
  }

  return (
    <Dialog
      width={720} onClose={() => onClose(false)} tone={info.breakGlassId ? 'bg' : prod ? 'prod' : undefined} title={`Sync ${app.name}?`}
      subtitle="Applies the application's target revision from Git, like Sync in Argo CD. Nothing is pruned: resources removed from Git stay in the cluster."
      footer={
        <>
          <button type="button" className="btn" onClick={() => onClose(false)}>Cancel</button>
          <button type="button" className={`btn act${canGo && !busy && !already ? '' : ' dis'}`} aria-disabled={!canGo || busy || already} onClick={() => void go()} data-autofocus={!prod || undefined}>
            <Icon name="refresh" />Sync
          </button>
        </>
      }
    >
      <BgNote info={info} />
      <dl className="kv" style={{ gridTemplateColumns: '90px 1fr' }}>
        <dt>Application</dt><dd className="mono">{app.name}</dd>
        <dt>Project</dt><dd className="mono">{app.project}</dd>
        <dt>Status</dt><dd>{app.sync} · {app.health}</dd>
        <dt>Cluster</dt><dd className="mono">{cluster}</dd>
        <dt>Environment</dt><dd><EnvBadge env={info.environment} /></dd>
      </dl>
      <div className="col" style={{ gap: 6 }}>
        <b>What changes{diff.data && ` (${diff.data.resources.length})`}</b>
        {diff.error ? <span className="muted">The changes could not be loaded: {errorText(diff.error)}</span>
          : !diff.data ? <Skeleton rows={2} />
            : <div style={{ maxHeight: 280, overflow: 'auto' }}><DiffList diff={diff.data} /></div>}
      </div>
      {prod && <div className="banner prod"><Icon name="warn" /><span>This changes production and is recorded in the audit log.</span></div>}
      {prod && (
        <label className="f">
          <span>Type <b className="mono" style={{ color: 'var(--tx)' }}>{app.name}</b> to confirm</span>
          <input className="input mono" value={typed} onChange={(e) => setTyped(e.target.value)} aria-label="Type the application name to confirm" autoComplete="off" spellCheck={false} data-autofocus />
        </label>
      )}
      {already && <div className="banner yel" role="alert"><Icon name="warn" /><span>Another operation on this application is already running in Argo CD. Try again when it has finished.</span></div>}
      {err != null && !already && <div className="banner red" role="alert">{errText(err)}</div>}
    </Dialog>
  );
}

/** Follows a sync or rollback of an Argo CD application until Argo CD reports it finished. */
function ArgoProgressDialog({ what, verb, name, info, subtitle, prog, done, onClose }: {
  what: 'Sync' | 'Rollback'; verb: string; name: string; info: DecisionInfo; subtitle: React.ReactNode;
  prog: ArgoSyncProgress | null; done: OperationDone | null; onClose: () => void;
}) {
  return (
    <Dialog
      onClose={onClose} tone={info.breakGlassId ? 'bg' : undefined} title={done ? `${what} of ${name}` : `${what === 'Sync' ? 'Syncing' : 'Rolling back'} ${name}`}
      subtitle={subtitle}
      footer={<button type="button" className="btn" data-autofocus onClick={onClose}>Close</button>}
    >
      <div aria-live="polite" className="col" style={{ gap: 10 }}>
        {!prog || prog.phase === 'Pending'
          ? <span className="muted">Waiting for Argo CD to start the {what.toLowerCase()}…</span>
          : (
            <>
              <dl className="kv" style={{ gridTemplateColumns: '90px 1fr' }}>
                <dt>Phase</dt><dd>{prog.phase}</dd>
                {prog.message && <><dt>Message</dt><dd>{prog.message}</dd></>}
                {prog.failed > 0 && <><dt>Failed</dt><dd className="mono">{prog.failed}</dd></>}
              </dl>
              {prog.total > 0 && <ProgressBar label="Resources synced" v={prog.synced} max={prog.total} />}
            </>
          )}
        {done && <DoneCard done={done} okTitle={`${name} ${verb}`} failTitle={`${what} of ${name} failed`} />}
      </div>
    </Dialog>
  );
}

const shortRev = (rev: string) => (/^[0-9a-f]{40}$/.test(rev) ? rev.slice(0, 7) : rev);
const refName = (r: ArgoResourceRef) => `${r.kind}${r.group ? `.${r.group}` : ''} ${r.namespace ? `${r.namespace}/` : ''}${r.name}`;

/**
 * Confirms and follows a rollback of an Argo CD application to an earlier
 * deploy, like Rollback in Argo CD. The plan is decided again with what the
 * revision deploys, so it can deny what the app's own access allowed.
 */
export function RollbackAppDialog({ app, deploy, cluster, namespace, onClose }: {
  app: NamespaceApp; deploy: ArgoDeploy; cluster: string; namespace: string; onClose: (changed: boolean) => void;
}) {
  const { usesGrants, me } = useAuth();
  const args = [cluster, namespace, app.appNamespace, app.name, deploy.id] as const;
  const plan = useApi(() => api.appRollbackPlan(...args), [...args]);
  const info = plan.data?.decision ?? app.access.rollback;
  const prod = isProd(info.environment);
  const [typed, setTyped] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const [started, setStarted] = useState(false);
  const [prog, setProg] = useState<ArgoSyncProgress | null>(null);
  const [done, setDone] = useState<OperationDone | null>(null);
  const stop = useRef<(() => void) | null>(null);
  useEffect(() => () => stop.current?.(), []);
  const allowed = !!plan.data?.decision.allowed;
  const canGo = allowed && (!prod || typed === app.name);
  const already = err instanceof ApiError && err.code === 'already_running';
  const rev = deploy.revision ? shortRev(deploy.revision) : `#${deploy.id}`;

  async function go() {
    if (!canGo || busy) return;
    setBusy(true);
    setErr(null);
    try {
      const { operationId } = await api.rollbackApp(...args);
      setStarted(true);
      stop.current = watchOperation<ArgoSyncProgress>(operationId, {
        onProgress: setProg,
        onDone: (d) => { setDone(d); setBusy(false); },
        onError: () => { setDone({ outcome: 'failed', message: 'Lost the progress stream. Check the application in Argo CD.' }); setBusy(false); },
      });
    } catch (e) {
      setErr(e);
      setBusy(false);
    }
  }

  const subtitle = <span className="row"><span className="mono" style={{ color: 'var(--tx)' }}>{cluster} / {app.name} → {rev}</span><EnvBadge env={info.environment} /></span>;
  if (started) {
    return <ArgoProgressDialog what="Rollback" verb={`rolled back to ${rev}`} name={app.name} info={info} subtitle={subtitle} prog={prog} done={done} onClose={() => onClose(true)} />;
  }
  const p = plan.data;
  const denial = p && !p.decision.allowed
    ? explainDecision(p.decision, { action: 'rollback', target: p.decisionNamespace === namespace ? app.name : `${app.name} (namespace ${p.decisionNamespace})`, canBreakGlass: usesGrants && breakGlassOn(me) })
    : null;
  const scope = p ? appScopeNote(p.decision, p.adminOnlyResources) : null;

  return (
    <Dialog
      width={720} onClose={() => onClose(false)} tone={info.breakGlassId ? 'bg' : prod ? 'prod' : undefined} title={`Roll back ${app.name} to ${rev}?`}
      subtitle="Applies the manifests of this earlier deploy, like Rollback in Argo CD. Nothing is pruned, and the application stays OutOfSync with Git until the next sync."
      footer={
        <>
          <button type="button" className="btn" onClick={() => onClose(false)}>Cancel</button>
          <button type="button" className={`btn act${canGo && !busy && !already ? '' : ' dis'}`} aria-disabled={!canGo || busy || already} onClick={() => void go()} data-autofocus={!prod || undefined}>
            <Icon name="restart" />Roll back
          </button>
        </>
      }
    >
      <BgNote info={info} />
      <dl className="kv" style={{ gridTemplateColumns: '90px 1fr' }}>
        <dt>Application</dt><dd className="mono">{app.name}</dd>
        <dt>Revision</dt><dd><span className="mono">{deploy.revision || deploy.revisions.join(', ')}</span></dd>
        {deploy.commit && <><dt>Commit</dt><dd>{deploy.commit.message.split('\n')[0]} <span className="muted">· {deploy.commit.author}</span></dd></>}
        <dt>Deployed</dt><dd>{fmtDateTime(deploy.deployedAt)}</dd>
        <dt>Cluster</dt><dd className="mono">{cluster}</dd>
        <dt>Environment</dt><dd><EnvBadge env={info.environment} /></dd>
      </dl>
      {plan.error ? <div className="banner red" role="alert">{errText(plan.error)}</div>
        : !p ? <Skeleton rows={2} />
          : (
            <div className="col" style={{ gap: 6 }}>
              {denial && (
                <div className="banner yel" role="alert">
                  <Icon name="warn" />
                  <span>
                    {denial.text} {scope?.text}
                    {scope && scope.shown.length > 0 && (
                      <span className="row wrap" style={{ gap: 4, marginTop: 4 }}>
                        {scope.shown.map((r) => <span key={r} className="tag mono">{r}</span>)}
                        {scope.more > 0 && <span className="muted">and {scope.more} more</span>}
                      </span>
                    )}
                  </span>
                </div>
              )}
              <b>Added by this revision ({p.added.length})</b>
              {p.added.length === 0 ? <span className="muted">None: it deploys the resources the application manages now.</span> : (
                <ul className="mono" style={{ margin: 0, paddingLeft: 18, maxHeight: 140, overflow: 'auto' }}>{p.added.map((r) => <li key={refName(r)}>{refName(r)}</li>)}</ul>
              )}
              {p.kept.length > 0 && (
                <>
                  <b>Not in this revision ({p.kept.length})</b>
                  <span className="muted">They stay in the cluster, since a rollback never prunes.</span>
                  <ul className="mono" style={{ margin: 0, paddingLeft: 18, maxHeight: 140, overflow: 'auto' }}>{p.kept.map((r) => <li key={refName(r)}>{refName(r)}</li>)}</ul>
                </>
              )}
              {p.truncated && <span className="muted">Only the first 200 of each are listed.</span>}
            </div>
          )}
      {prod && allowed && <div className="banner prod"><Icon name="warn" /><span>This changes production and is recorded in the audit log.</span></div>}
      {prod && allowed && (
        <label className="f">
          <span>Type <b className="mono" style={{ color: 'var(--tx)' }}>{app.name}</b> to confirm</span>
          <input className="input mono" value={typed} onChange={(e) => setTyped(e.target.value)} aria-label="Type the application name to confirm" autoComplete="off" spellCheck={false} data-autofocus />
        </label>
      )}
      {already && <div className="banner yel" role="alert"><Icon name="warn" /><span>Another operation on this application is already running in Argo CD. Try again when it has finished.</span></div>}
      {err != null && !already && <div className="banner red" role="alert">{errText(err)}</div>}
    </Dialog>
  );
}

function errText(e: unknown): string {
  if (e instanceof ApiError && e.decision) return e.message || 'Access denied';
  return errorText(e);
}

/** Confirms and follows a manual run of a CronJob (from its row, its page or the CronJobs list). */
export function RunCronDialog({ cron, onClose, onViewRunning }: { cron: WorkloadSummary; onClose: (changed: boolean) => void; onViewRunning?: () => void }) {
  const info = cron.access['run-job'];
  const prod = isProd(cron.environment);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const [job, setJob] = useState<JobSummary | null>(null);
  const [started, setStarted] = useState(false);
  const [done, setDone] = useState<OperationDone | null>(null);
  const stop = useRef<(() => void) | null>(null);
  useEffect(() => () => stop.current?.(), []);
  const already = err instanceof ApiError && err.code === 'already_running';

  async function go() {
    if (busy) return;
    setBusy(true);
    setErr(null);
    try {
      const r = await api.runCronJob(cron.cluster, cron.namespace, cron.name);
      setStarted(true);
      stop.current = watchOperation<JobSummary>(r.operationId, {
        onProgress: setJob,
        onDone: (d) => { setDone(d); setBusy(false); },
        onError: () => { setDone({ outcome: 'failed', message: 'Lost the progress stream. Check Recent jobs for the current state.' }); setBusy(false); },
      });
    } catch (e) {
      setErr(e);
      setBusy(false);
    }
  }

  if (started) {
    return (
      <Dialog
        onClose={() => onClose(true)} tone={info?.breakGlassId ? 'bg' : undefined} title={done ? `Run of ${cron.name}` : `Running ${cron.name}`}
        subtitle={<span className="row"><span className="mono" style={{ color: 'var(--tx)' }}>{cron.cluster} / {cron.namespace}</span><EnvBadge env={cron.environment} /></span>}
        footer={<button type="button" className="btn" data-autofocus onClick={() => onClose(true)}>Close</button>}
      >
        <div aria-live="polite" className="col">
          {job && (
            <dl className="kv" style={{ gridTemplateColumns: '90px 1fr' }}>
              <dt>Job</dt><dd className="mono">{job.name}</dd>
              <dt>Status</dt><dd><JobStatusChip status={job.status} /></dd>
              <dt>Pods</dt><dd className="mono">{job.succeeded}/{job.completions} done · {job.active} active</dd>
            </dl>
          )}
          {!job && !done && <span className="muted">Waiting for the Job to start…</span>}
          {done && <DoneCard done={done} okTitle={`${cron.name} finished`} failTitle={`${cron.name} failed`} />}
        </div>
      </Dialog>
    );
  }

  return (
    <Dialog
      onClose={() => onClose(false)} tone={info?.breakGlassId ? 'bg' : prod ? 'prod' : undefined} title={`Run ${cron.name} now?`}
      subtitle="Creates one new Job from this CronJob's template. The schedule does not change."
      footer={
        <>
          <button type="button" className="btn" onClick={() => onClose(false)}>Cancel</button>
          <button type="button" className={`btn act${busy || already ? ' dis' : ''}`} aria-disabled={busy || already} onClick={() => void go()} data-autofocus>
            <Icon name="bolt" />Run now
          </button>
        </>
      }
    >
      <BgNote info={info} />
      <dl className="kv" style={{ gridTemplateColumns: '90px 1fr' }}>
        <dt>CronJob</dt><dd className="mono">{cron.name}</dd>
        <dt>Schedule</dt><dd><span className="mono">{cron.schedule}</span><span className="muted">{describeCron(cron.schedule ?? '')}</span></dd>
        <dt>Namespace</dt><dd className="mono">{cron.namespace}</dd>
        <dt>Cluster</dt><dd className="mono">{cron.cluster}</dd>
        <dt>Environment</dt><dd><EnvBadge env={cron.environment} /></dd>
      </dl>
      {cron.suspended && <div className="banner yel"><Icon name="warn" /><span><b>This CronJob is suspended.</b> Running it now still creates a Job; the schedule stays suspended.</span></div>}
      {prod && <div className="banner prod"><Icon name="warn" /><span>This runs against production data and is recorded in the audit log.</span></div>}
      {already && (
        <div className="banner yel" role="alert">
          <Icon name="warn" />
          <span>
            A run is already in progress.{' '}
            {onViewRunning && <button type="button" className="linklike blue" onClick={() => { onClose(true); onViewRunning(); }}>View running job</button>}
          </span>
        </div>
      )}
      {err != null && !already && <div className="banner red" role="alert">{errText(err)}</div>}
    </Dialog>
  );
}
