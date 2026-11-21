import { useEffect, useRef, useState } from 'react';
import { Link } from 'react-router-dom';
import { ApiError, api, errorText, watchOperation } from '../api';
import type { ArgoDeploy, ArgoPlanResource, ArgoResourceRef, ArgoSyncProgress, DecisionInfo, JobSummary, NamespaceApp, OperationDone, Pod, RestartProgress, WorkloadSummary } from '../api';
import { useAuth } from '../context/auth';
import { breakGlassOn } from '../lib/breakGlass';
import { appScopeNote, explainDecision } from '../lib/decision';
import { useApi } from '../hooks/useApi';
import { describeCron } from '../lib/cron';
import { jobFailure } from '../lib/jobs';
import { logsUrl } from '../lib/routes';
import {
  PRUNE_POLICIES, SYNC_OPTIONS, countsText, initialForm, otherOptions, planCounts, prunePolicy, refKey, retryValid, syncInput, toPrune, toggleOption, withPrunePolicy,
} from '../lib/syncPlan';
import type { PrunePolicy, SyncForm } from '../lib/syncPlan';
import { fmtDateTime, fmtTime } from '../lib/time';
import { BreakGlassMark, EnvBadge, JobStatusChip, KindTag, isProd } from './Badges';
import { PlanList } from './DiffView';
import { Skeleton } from './Feedback';
import { Dialog } from './Dialog';
import { Icon } from './Icon';
import { Select } from './Select';

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

/** Confirms deleting one pod of a workload; its controller replaces it. Needs restart. */
export function DeletePodDialog({ wl, pod, onClose }: { wl: WorkloadSummary; pod: string; onClose: (changed: boolean) => void }) {
  const info = wl.access.restart;
  const prod = isProd(wl.environment);
  const [typed, setTyped] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const canGo = !prod || typed === wl.name;

  async function go() {
    if (!canGo || busy) return;
    setBusy(true);
    setErr(null);
    try {
      await api.deletePod({ cluster: wl.cluster, namespace: wl.namespace, kind: wl.kind, name: wl.name }, pod);
      onClose(true);
    } catch (e) {
      setErr(e);
      setBusy(false);
    }
  }

  return (
    <Dialog
      onClose={() => onClose(false)} tone={info?.breakGlassId ? 'bg' : prod ? 'prod' : undefined} title="Delete pod?"
      subtitle={`${wl.kind === 'StatefulSet' ? 'The StatefulSet recreates it under the same name' : `The ${wl.kind} replaces it with a new pod`}. Containers get their usual shutdown grace period.`}
      footer={
        <>
          <button type="button" className="btn" onClick={() => onClose(false)}>Cancel</button>
          <button type="button" className={`btn act${canGo && !busy ? '' : ' dis'}`} aria-disabled={!canGo || busy} onClick={() => void go()}>
            <Icon name="trash" />Delete pod
          </button>
        </>
      }
    >
      <BgNote info={info} />
      <dl className="kv" style={{ gridTemplateColumns: '90px 1fr' }}>
        <dt>Pod</dt><dd className="mono">{pod}</dd>
        <dt>Workload</dt><dd className="row"><KindTag kind={wl.kind} /><span className="mono">{wl.name}</span></dd>
        <dt>Namespace</dt><dd className="mono">{wl.namespace}</dd>
        <dt>Cluster</dt><dd className="mono">{wl.cluster}</dd>
        <dt>Environment</dt><dd><EnvBadge env={wl.environment} /></dd>
      </dl>
      {wl.kind !== 'DaemonSet' && wl.desired <= 1 && (
        <div className="banner yel"><Icon name="warn" /><span>This workload runs a single pod: it is unavailable until the new pod is ready. A restart replaces it without downtime.</span></div>
      )}
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
 * Confirms and follows a sync of a whole Argo CD application with the
 * options of Argo CD's sync dialog. The plan shows what the sync would do
 * (what prune deletes included) and is loaded again for another revision,
 * which needs rollback too; production asks for the app's name, like a
 * restart, but for a dry run.
 */
export function SyncAppDialog({ app, cluster, namespace, onClose }: { app: NamespaceApp; cluster: string; namespace: string; onClose: (changed: boolean) => void }) {
  const { usesGrants, me } = useAuth();
  const [revInput, setRevInput] = useState('');
  const [revision, setRevision] = useState('');
  const args = [cluster, namespace, app.appNamespace, app.name] as const;
  const plan = useApi(() => api.appSyncPlan(...args, revision), [...args, revision]);
  const [form, setForm] = useState<SyncForm | null>(null);
  const [typed, setTyped] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const [started, setStarted] = useState(false);
  const [prog, setProg] = useState<ArgoSyncProgress | null>(null);
  const [done, setDone] = useState<OperationDone | null>(null);
  const stop = useRef<(() => void) | null>(null);
  useEffect(() => () => stop.current?.(), []);
  // The form starts from the app's own settings; another revision keeps the
  // options but selects all its resources again.
  useEffect(() => {
    const p = plan.data;
    if (p) setForm((f) => (f ? { ...f, selected: null } : initialForm(p)));
  }, [plan.data]);

  const p = plan.loading || plan.error ? undefined : plan.data;
  const info = p?.decision ?? app.access.sync;
  const prod = isProd(info.environment);
  const set = (patch: Partial<SyncForm>) => setForm((f) => (f ? { ...f, ...patch } : f));
  const pruned = p && form ? toPrune(p, form) : [];
  const counts = p && form ? planCounts(p, form) : null;
  const otherRev = !!p?.revision;
  const autoSyncBlocked = !!p && otherRev && p.autoSync && !form?.dryRun;
  const nothing = !!form && form.selected !== null && form.selected.length === 0;
  const ready = !!p && !!form && p.decision.allowed && !autoSyncBlocked && !nothing && retryValid(form.retry);
  const canGo = ready && (!prod || form?.dryRun || typed === app.name);
  const already = err instanceof ApiError && err.code === 'already_running';
  const what = form?.dryRun ? 'Dry run' : 'Sync';

  async function go() {
    if (!canGo || busy || !p || !form) return;
    setBusy(true);
    setErr(null);
    try {
      const { operationId } = await api.syncApp(...args, syncInput(p, form));
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

  const subtitle = <span className="row"><span className="mono" style={{ color: 'var(--tx)' }}>{cluster} / {app.name}{otherRev ? ` → ${shortRev(p!.revision!)}` : ''}</span><EnvBadge env={info.environment} /></span>;
  if (started) {
    return (
      <ArgoProgressDialog what={what} verb={form?.dryRun ? 'dry-run finished' : 'synced'} name={app.name} info={info} subtitle={subtitle} prog={prog} done={done} onClose={() => onClose(true)} />
    );
  }
  const denial = p && !p.decision.allowed
    ? explainDecision(p.decision, { action: p.decisionAction, target: p.decisionNamespace === namespace ? app.name : `${app.name} (namespace ${p.decisionNamespace})`, canBreakGlass: usesGrants && breakGlassOn(me) })
    : null;
  const scope = p ? appScopeNote(p.decision, p.adminOnlyResources) : null;
  const isSelected = (r: ArgoPlanResource) => !form || form.selected === null || form.selected.includes(refKey(r));
  const toggle = (r: ArgoPlanResource) => {
    if (!p || !form) return;
    const all = form.selected ?? p.resources.map(refKey);
    const k = refKey(r);
    const next = all.includes(k) ? all.filter((x) => x !== k) : [...all, k];
    set({ selected: next.length === p.resources.length ? null : next });
  };
  const applyRevision = () => setRevision(revInput.trim());

  return (
    <Dialog
      width={780} onClose={() => onClose(false)} tone={info.breakGlassId ? 'bg' : prod ? 'prod' : undefined} title={`Sync ${app.name}?`}
      subtitle={otherRev
        ? 'Applies another revision than the target, like Sync to a revision in Argo CD. The application stays OutOfSync with its target until the next sync.'
        : "Applies the application's target revision, like Sync in Argo CD."}
      footer={
        <>
          <button type="button" className="btn" onClick={() => onClose(false)}>Cancel</button>
          <button type="button" className={`btn act${canGo && !busy && !already ? '' : ' dis'}`} aria-disabled={!canGo || busy || already} onClick={() => void go()} data-autofocus={!prod || undefined}>
            <Icon name="refresh" />{form?.dryRun ? 'Dry run' : pruned.length ? `Sync and prune ${pruned.length}` : 'Sync'}
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
      <form className="row" style={{ gap: 8, alignItems: 'flex-end' }} onSubmit={(e) => { e.preventDefault(); applyRevision(); }}>
        <label className="f grow">
          <span>Revision <span className="hint">empty for the target{plan.data ? `: ${plan.data.targetRevision}` : ''}</span></span>
          <input className="input mono" value={revInput} onChange={(e) => setRevInput(e.target.value)} placeholder={plan.data?.targetRevision || 'HEAD'}
            aria-label="Revision" autoComplete="off" spellCheck={false} disabled={app.multiSource} />
        </label>
        <button type="submit" className={`btn${revInput.trim() === revision ? ' dis' : ''}`} aria-disabled={revInput.trim() === revision}>Show plan</button>
      </form>
      {form && <SyncOptionsForm form={form} set={set} />}
      <div className="col" style={{ gap: 6 }}>
        <div className="row">
          <b>What the {form?.dryRun ? 'dry run checks' : 'sync does'}</b>
          {counts && <span className="muted">{countsText(counts, otherRev)}</span>}
          <span className="grow" />
          {p && form?.selected !== null && <button type="button" className="btn sm" onClick={() => set({ selected: null })}>Select all</button>}
        </div>
        {plan.error ? <div className="banner red" role="alert">{errText(plan.error)}</div>
          : !p ? <Skeleton rows={2} />
            : (
              <>
                {denial && (
                  <div className="banner yel" role="alert">
                    <Icon name="warn" />
                    <span>
                      {denial.text} {p.decisionAction === 'rollback' && 'Syncing to another revision than the target also needs rollback.'} {scope?.text}
                      {scope && scope.shown.length > 0 && (
                        <span className="row wrap" style={{ gap: 4, marginTop: 4 }}>
                          {scope.shown.map((r) => <span key={r} className="tag mono">{r}</span>)}
                          {scope.more > 0 && <span className="muted">and {scope.more} more</span>}
                        </span>
                      )}
                    </span>
                  </div>
                )}
                {otherRev && <span className="muted">Argo CD shows changes against the target revision only, so this lists what the revision deploys and what it lacks.</span>}
                {p.resources.length === 0 ? <div className="empty">The application has no resources.</div>
                  : <div style={{ maxHeight: 280, overflow: 'auto' }}><PlanList resources={p.resources} prune={!!form?.prune} isSelected={isSelected} onToggle={toggle} /></div>}
                {p.truncated && <span className="muted">Only the first 200 resources are listed.</span>}
              </>
            )}
      </div>
      {pruned.length > 0 && (
        <div className="banner red" role="status">
          <Icon name="trash" />
          <span>
            {form?.dryRun ? 'A real sync would delete' : 'Deletes'} {pruned.length === 1 ? 'this resource' : `these ${pruned.length} resources`} from the cluster:
            <span className="row wrap" style={{ gap: 4, marginTop: 4 }}>{pruned.map((r) => <span key={refKey(r)} className="tag mono">{refName(r)}</span>)}</span>
          </span>
        </div>
      )}
      {form && !form.dryRun && (form.force || form.options.includes('Replace=true')) && (
        <div className="banner yel"><Icon name="warn" /><span>{form.force ? 'Force' : 'Replace'} may delete and recreate resources, which restarts their pods and drops their state.</span></div>
      )}
      {autoSyncBlocked && <div className="banner yel" role="alert"><Icon name="warn" /><span>This application syncs automatically, so Argo CD syncs it to another revision only as a dry run.</span></div>}
      {prod && !form?.dryRun && <div className="banner prod"><Icon name="warn" /><span>This changes production and is recorded in the audit log.</span></div>}
      {prod && !form?.dryRun && ready && (
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

/** The options of Argo CD's sync dialog. */
function SyncOptionsForm({ form, set }: { form: SyncForm; set: (patch: Partial<SyncForm>) => void }) {
  const others = otherOptions(form.options);
  const r = form.retry;
  const setRetry = (patch: Partial<SyncForm['retry']>) => set({ retry: { ...r, ...patch }, retryTouched: true });
  return (
    <fieldset className="col" style={{ gap: 8, border: '1px solid var(--ln)', borderRadius: 6, padding: '8px 12px', margin: 0 }}>
      <legend className="muted" style={{ padding: '0 4px' }}>Options</legend>
      <div className="row wrap" style={{ gap: '6px 16px' }}>
        <label className="chk"><input type="checkbox" checked={form.prune} onChange={(e) => set({ prune: e.target.checked })} /> Prune</label>
        <label className="chk"><input type="checkbox" checked={form.dryRun} onChange={(e) => set({ dryRun: e.target.checked })} /> Dry run</label>
        <label className="chk"><input type="checkbox" checked={form.applyOnly} onChange={(e) => set({ applyOnly: e.target.checked })} /> Apply only <span className="muted">(skip hooks)</span></label>
        <label className="chk"><input type="checkbox" checked={form.force} onChange={(e) => set({ force: e.target.checked })} /> Force</label>
      </div>
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(220px, 1fr))', gap: '6px 16px' }}>
        {SYNC_OPTIONS.map((o) => (
          <label key={o.value} className="chk" title={o.hint}>
            <input type="checkbox" checked={form.options.includes(o.value)} onChange={() => set({ options: toggleOption(form.options, o.value) })} /> {o.label}
          </label>
        ))}
      </div>
      <div className="row wrap" style={{ gap: '6px 16px' }}>
        <span className="row" style={{ gap: 8 }}>
          <span>Prune propagation</span>
          <Select<PrunePolicy | ''> value={prunePolicy(form.options)} label="Prune propagation policy" onChange={(v) => set({ options: withPrunePolicy(form.options, v) })}
            options={[{ value: '', label: 'default' }, ...PRUNE_POLICIES.map((v) => ({ value: v, label: v }))]} />
        </span>
        {others.length > 0 && (
          <span className="row wrap" style={{ gap: 4 }}>
            <span className="muted">The application's own:</span>
            {others.map((o) => (
              <label key={o} className="chk tag mono"><input type="checkbox" checked onChange={() => set({ options: toggleOption(form.options, o) })} />{o}</label>
            ))}
          </span>
        )}
      </div>
      <div className="row wrap" style={{ gap: 8 }}>
        <label className="chk"><input type="checkbox" checked={r.on} onChange={(e) => setRetry({ on: e.target.checked })} /> Retry</label>
        {r.on && (
          <>
            <label className="row" style={{ gap: 4 }}>limit <input className="input mono" style={{ width: 56 }} type="number" min={1} max={10} value={r.limit} onChange={(e) => setRetry({ limit: Number(e.target.value) })} /></label>
            <label className="row" style={{ gap: 4 }}>backoff <input className="input mono" style={{ width: 64 }} value={r.duration} onChange={(e) => setRetry({ duration: e.target.value.trim() })} /></label>
            <label className="row" style={{ gap: 4 }}>× <input className="input mono" style={{ width: 48 }} type="number" min={1} max={10} value={r.factor} onChange={(e) => setRetry({ factor: Number(e.target.value) })} /></label>
            <label className="row" style={{ gap: 4 }}>up to <input className="input mono" style={{ width: 64 }} value={r.maxDuration} onChange={(e) => setRetry({ maxDuration: e.target.value.trim() })} /></label>
            {!retryValid(r) && <span className="muted" role="alert">Limit and factor 1–10, durations like 5s or 3m.</span>}
          </>
        )}
      </div>
    </fieldset>
  );
}

/** Follows a sync or rollback of an Argo CD application until Argo CD reports it finished. */
function ArgoProgressDialog({ what, verb, name, info, subtitle, prog, done, onClose }: {
  what: 'Sync' | 'Rollback' | 'Dry run'; verb: string; name: string; info: DecisionInfo; subtitle: React.ReactNode;
  prog: ArgoSyncProgress | null; done: OperationDone | null; onClose: () => void;
}) {
  const running = { Sync: 'Syncing', Rollback: 'Rolling back', 'Dry run': 'Dry run of' }[what];
  return (
    <Dialog
      onClose={onClose} tone={info.breakGlassId ? 'bg' : undefined} title={done ? `${what} of ${name}` : `${running} ${name}`}
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
  const [prune, setPrune] = useState(false);
  const [dryRun, setDryRun] = useState(false);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const [started, setStarted] = useState(false);
  const [prog, setProg] = useState<ArgoSyncProgress | null>(null);
  const [done, setDone] = useState<OperationDone | null>(null);
  const stop = useRef<(() => void) | null>(null);
  useEffect(() => () => stop.current?.(), []);
  const allowed = !!plan.data?.decision.allowed;
  const canGo = allowed && (!prod || dryRun || typed === app.name);
  const pruned = prune ? plan.data?.kept ?? [] : [];
  const already = err instanceof ApiError && err.code === 'already_running';
  const rev = deploy.revision ? shortRev(deploy.revision) : `#${deploy.id}`;

  async function go() {
    if (!canGo || busy) return;
    setBusy(true);
    setErr(null);
    try {
      const { operationId } = await api.rollbackApp(...args, { prune, dryRun });
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
    return <ArgoProgressDialog what={dryRun ? 'Dry run' : 'Rollback'} verb={dryRun ? 'dry-run finished' : `rolled back to ${rev}`} name={app.name} info={info} subtitle={subtitle} prog={prog} done={done} onClose={() => onClose(true)} />;
  }
  const p = plan.data;
  const denial = p && !p.decision.allowed
    ? explainDecision(p.decision, { action: 'rollback', target: p.decisionNamespace === namespace ? app.name : `${app.name} (namespace ${p.decisionNamespace})`, canBreakGlass: usesGrants && breakGlassOn(me) })
    : null;
  const scope = p ? appScopeNote(p.decision, p.adminOnlyResources) : null;

  return (
    <Dialog
      width={720} onClose={() => onClose(false)} tone={info.breakGlassId ? 'bg' : prod ? 'prod' : undefined} title={`Roll back ${app.name} to ${rev}?`}
      subtitle="Applies the manifests of this earlier deploy, like Rollback in Argo CD. The application stays OutOfSync with Git until the next sync."
      footer={
        <>
          <button type="button" className="btn" onClick={() => onClose(false)}>Cancel</button>
          <button type="button" className={`btn act${canGo && !busy && !already ? '' : ' dis'}`} aria-disabled={!canGo || busy || already} onClick={() => void go()} data-autofocus={!prod || undefined}>
            <Icon name="restart" />{dryRun ? 'Dry run' : pruned.length ? `Roll back and prune ${pruned.length}` : 'Roll back'}
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
      <div className="row" style={{ gap: 16 }}>
        <label className="chk"><input type="checkbox" checked={prune} onChange={(e) => setPrune(e.target.checked)} /> Prune</label>
        <label className="chk"><input type="checkbox" checked={dryRun} onChange={(e) => setDryRun(e.target.checked)} /> Dry run</label>
      </div>
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
                  {prune
                    ? <span className="banner red" role="status"><Icon name="trash" />{dryRun ? 'A real rollback would delete them' : 'They are deleted from the cluster'}, but for those annotated Prune=false.</span>
                    : <span className="muted">They stay in the cluster, since the rollback does not prune.</span>}
                  <ul className="mono" style={{ margin: 0, paddingLeft: 18, maxHeight: 140, overflow: 'auto' }}>{p.kept.map((r) => <li key={refName(r)}>{refName(r)}</li>)}</ul>
                </>
              )}
              {p.truncated && <span className="muted">Only the first 200 of each are listed.</span>}
            </div>
          )}
      {prod && allowed && !dryRun && <div className="banner prod"><Icon name="warn" /><span>This changes production and is recorded in the audit log.</span></div>}
      {prod && allowed && !dryRun && (
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
    // The latest attempt's logs, as soon as its pod exists.
    const pod = job?.podNames[0];
    const failed = !!done && done.outcome !== 'succeeded';
    const logsTo = pod && cron.access.logs.allowed ? logsUrl({ cluster: cron.cluster, namespace: cron.namespace, kind: 'CronJob', name: cron.name }, pod) : null;
    return (
      <Dialog
        onClose={() => onClose(true)} tone={info?.breakGlassId ? 'bg' : undefined} title={done ? `Run of ${cron.name}` : `Running ${cron.name}`}
        subtitle={<span className="row"><span className="mono" style={{ color: 'var(--tx)' }}>{cron.cluster} / {cron.namespace}</span><EnvBadge env={cron.environment} /></span>}
        footer={
          <>
            {logsTo && <Link className={`btn${failed ? ' act' : ''}`} to={logsTo} onClick={() => onClose(true)}><Icon name="list" />{done ? 'View logs' : 'Follow logs'}</Link>}
            <button type="button" className="btn" data-autofocus onClick={() => onClose(true)}>Close</button>
          </>
        }
      >
        <div aria-live="polite" className="col">
          {job && (
            <dl className="kv" style={{ gridTemplateColumns: '90px 1fr' }}>
              <dt>Job</dt><dd className="mono">{job.name}</dd>
              <dt>Status</dt><dd><JobStatusChip status={job.status} /></dd>
              <dt>Pods</dt><dd className="mono">{job.succeeded}/{job.completions} done · {job.active} active{job.failed > 0 ? ` · ${job.failed} failed` : ''}</dd>
              {jobFailure(job) && (<><dt>Reason</dt><dd title={job.message ?? undefined}>{jobFailure(job)}</dd></>)}
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
