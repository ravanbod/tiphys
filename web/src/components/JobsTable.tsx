import { Fragment, useState } from 'react';
import { Link } from 'react-router-dom';
import { api } from '../api';
import type { Pod } from '../api';
import { useAuth } from '../context/auth';
import { rowClass, useAnimatedList } from '../hooks/useAnimatedList';
import { useApi } from '../hooks/useApi';
import { useNow } from '../hooks/useNow';
import { useUrlState } from '../hooks/useUrlState';
import { filterJobs, isFreshRun, jobFailure, jobSeconds } from '../lib/jobs';
import type { JobRow, JobStatusFilter } from '../lib/jobs';
import { execUrl, logsUrl, workloadUrl } from '../lib/routes';
import { fmtAge, fmtDateTime, fmtRelative, fmtSpan } from '../lib/time';
import { ActionButton } from './ActionButton';
import { JobStatusChip, PhaseChip } from './Badges';
import { ErrorBanner } from './Feedback';
import { Icon } from './Icon';
import { Select } from './Select';

const STATUSES: { value: JobStatusFilter; label: string }[] = [
  { value: '', label: 'All results' }, { value: 'running', label: 'Running' }, { value: 'failed', label: 'Failed' }, { value: 'succeeded', label: 'Succeeded' },
];

/**
 * Jobs of one or more CronJobs, newest first, with filters. A row opens to
 * the Job's pods; `highlight` marks a Job (e.g. the running one).
 */
export function JobsTable({ cluster, namespace, jobs, showCronJob, highlight }: {
  cluster: string; namespace: string; jobs: JobRow[]; showCronJob?: boolean; highlight?: string | null;
}) {
  const { me } = useAuth();
  const now = useNow(30000);
  // In the URL, so a link can open the running or failed jobs.
  const [f, setF] = useUrlState({ result: '', cron: '' });
  const status = f.result as JobStatusFilter;
  const cronJob = f.cron;
  const [open, setOpen] = useState<ReadonlySet<string>>(new Set());
  const crons = [...new Set(jobs.map((j) => j.cronJob))].sort();
  const shown = filterJobs(jobs, { status, cronJob });
  const rows = useAnimatedList(shown, (j) => j.name, (j) => `${j.status}|${j.succeeded}|${j.failed}|${j.podNames.join(',')}`, `${cluster}/${namespace}|${status}|${cronJob}`);
  const cols = 7;
  const toggle = (name: string) => setOpen((s) => {
    const n = new Set(s);
    if (!n.delete(name)) n.add(name);
    return n;
  });

  return (
    <div className="col" style={{ gap: 8 }}>
      <div className="row wrap">
        <Select label="Result" value={status} onChange={(v) => setF({ result: v })} options={STATUSES} />
        {showCronJob && crons.length > 1 && (
          <Select label="CronJob" mono value={cronJob} onChange={(v) => setF({ cron: v })} options={[{ value: '', label: 'All CronJobs' }, ...crons.map((c) => ({ value: c, label: c }))]} />
        )}
        <span className="grow" />
        <span className="muted" aria-live="polite">{shown.length === jobs.length ? `${jobs.length} job${jobs.length === 1 ? '' : 's'}` : `Showing ${shown.length} of ${jobs.length}`} · newest first</span>
      </div>
      <div className="card">
        <div className="tbl-wrap">
          <table className="tbl nowrap">
            <thead>
              <tr>
                <th style={{ width: 28 }} /><th>{showCronJob ? 'CronJob / Job' : 'Job'}</th><th>Triggered by</th><th>Started</th><th>Duration</th><th>Result</th>
                <th style={{ textAlign: 'right' }} />
              </tr>
            </thead>
            <tbody>
              {rows.length === 0 && <tr><td colSpan={cols} className="empty">{jobs.length === 0 ? 'No jobs. The cluster keeps only the most recent runs.' : 'No jobs match these filters.'}</td></tr>}
              {rows.map(({ item: j, anim }) => {
                const fresh = isFreshRun(j, me.username, now);
                const isOpen = open.has(j.name);
                const secs = jobSeconds(j, now);
                const fail = jobFailure(j);
                const loc = { cluster, namespace, kind: 'CronJob', name: j.cronJob };
                const gone = j.podNames.length === 0;
                return (
                  <Fragment key={j.name}>
                    <tr className={rowClass(anim, `click${j.name === highlight || fresh ? ' sel' : ''}`)} onClick={() => toggle(j.name)}>
                      <td>
                        <button type="button" className="btn ghost sm" aria-expanded={isOpen} aria-label={`${isOpen ? 'Hide' : 'Show'} pods of ${j.name}`} onClick={(e) => { e.stopPropagation(); toggle(j.name); }}>
                          <Icon name={isOpen ? 'chevd' : 'chev'} size={12} />
                        </button>
                      </td>
                      <td>
                        {showCronJob && <div onClick={(e) => e.stopPropagation()}><Link className="mono" to={workloadUrl(loc)}>{j.cronJob}</Link></div>}
                        <span className={showCronJob ? 'mono muted' : 'mono'} style={showCronJob ? { fontSize: 12 } : undefined}>{j.name}</span>
                        {fresh && <span className="chip info" style={{ marginLeft: 6 }}>New</span>}
                      </td>
                      <td>{j.manual ? `${j.triggeredBy ?? 'Unknown'} · manual` : 'Schedule'}</td>
                      <td title={fmtDateTime(j.startedAt)}>{fmtRelative(j.startedAt, now)}</td>
                      <td>{secs === null ? <span className="muted">—</span> : j.status === 'running' ? <span className="muted">running {fmtSpan(secs)}</span> : fmtSpan(secs)}</td>
                      <td>
                        <JobStatusChip status={j.status} />
                        {fail && <div className="muted" style={{ fontSize: 12, marginTop: 2 }} title={j.message ?? undefined}>{fail}</div>}
                      </td>
                      <td style={{ textAlign: 'right' }} onClick={(e) => e.stopPropagation()}>
                        {gone
                          ? <span className="muted" title="The cluster removed this Job's pods (TTL or history limit), so its logs are gone.">Pods removed</span>
                          : <ActionButton action="logs" decision={j.cron.access.logs} label="Logs" icon="list" size="sm" to={logsUrl(loc, j.podNames[0])} target={j.cronJob} hint="none" />}
                      </td>
                    </tr>
                    {isOpen && (
                      <tr className="sub">
                        <td />
                        <td colSpan={cols - 1} style={{ padding: '8px 12px 12px 0' }}>
                          <JobPods job={j} />
                        </td>
                      </tr>
                    )}
                  </Fragment>
                );
              })}
            </tbody>
          </table>
        </div>
      </div>
    </div>
  );
}

/** The pods of one Job, the latest attempt first. */
function JobPods({ job }: { job: JobRow }) {
  const { debugEnabled } = useAuth().me.settings;
  const c = job.cron;
  const loc = { cluster: c.cluster, namespace: c.namespace, kind: 'CronJob', name: c.name };
  const logsOk = c.access.logs.allowed;
  // Reloads when the job changes (a new attempt, a pod finishing).
  const pods = useApi(() => api.pods(loc), [c.cluster, c.namespace, c.name, job.status, job.active, job.failed, job.podNames.join(',')], { enabled: logsOk });
  const now = useNow(30000);
  if (!logsOk) return <span className="muted">Viewing pods needs logs access to {c.name}.</span>;
  if (pods.error) return <ErrorBanner error={pods.error} onRetry={pods.reload} />;
  if (!pods.data) return <div className="skel" style={{ width: 240 }} />;
  const mine = pods.data.filter((p) => p.job === job.name).sort((a, b) => Date.parse(b.createdAt) - Date.parse(a.createdAt));
  if (mine.length === 0) return <span className="muted">{job.status === 'running' ? 'Waiting for the pod to start…' : 'The cluster removed this Job\'s pods (TTL or history limit), so its logs are gone.'}</span>;
  return (
    <table className="tbl nowrap" style={{ background: 'var(--sf)', border: '1px solid var(--ln)', borderRadius: 6 }}>
      <thead><tr><th>Pod</th><th>Phase</th><th>Exit</th><th>Started</th><th>Node</th><th style={{ textAlign: 'right' }} /></tr></thead>
      <tbody>
        {mine.map((p, i) => (
          <tr key={p.name}>
            <td><span className="mono">{p.name}</span>{mine.length > 1 && <span className="muted" style={{ marginLeft: 6 }}>{i === 0 ? 'latest attempt' : `attempt ${mine.length - i}`}</span>}</td>
            <td><PhaseChip phase={p.phase} /></td>
            <td><PodExit pod={p} /></td>
            <td title={fmtDateTime(p.createdAt)}>{fmtAge(p.createdAt, now)} ago</td>
            <td className="mono">{p.node || '—'}</td>
            <td style={{ textAlign: 'right' }}>
              <span className="row" style={{ justifyContent: 'flex-end' }}>
                <ActionButton action="logs" decision={c.access.logs} label="Logs" icon="list" size="sm" to={logsUrl(loc, p.name)} target={c.name} hint="none" />
                {/* Exec and Debug only into a pod that still runs. */}
                {p.phase === 'Running' && <ActionButton action="exec" decision={c.access.exec} label="Exec" icon="term" size="sm" to={execUrl(loc, p.name)} target={c.name} hint="none" />}
                {p.phase === 'Running' && debugEnabled && <ActionButton action="exec" decision={c.access.exec} label="Debug" icon="bug" size="sm" plain to={execUrl(loc, p.name, undefined, true)} target={c.name} hint="none" />}
              </span>
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

/** The main container's exit: "0 · Completed", "137 · OOMKilled". */
function PodExit({ pod }: { pod: Pod }) {
  const ct = pod.containers.find((x) => !x.init && x.state === 'terminated' && x.exitCode !== null && x.exitCode !== 0)
    ?? pod.containers.find((x) => !x.init && x.state === 'terminated');
  if (!ct || ct.exitCode === null) return <span className="muted">—</span>;
  const text = `${ct.exitCode}${ct.stateReason ? ` · ${ct.stateReason}` : ''}`;
  return ct.exitCode === 0 ? <span className="mono">{text}</span> : <span className="chip err mono">{text}</span>;
}
