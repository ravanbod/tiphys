import { useState } from 'react';
import { Link } from 'react-router-dom';
import { api } from '../api';
import type { CronJobSummary, JobSummary } from '../api';
import { useAuth } from '../context/auth';
import { useLive } from '../context/live';
import { rowClass, useAnimatedList } from '../hooks/useAnimatedList';
import { useApi } from '../hooks/useApi';
import { useNow } from '../hooks/useNow';
import { describeCron } from '../lib/cron';
import { logsUrl } from '../lib/routes';
import { fmtDay, fmtRelative, fmtTime } from '../lib/time';
import { ActionButton } from './ActionButton';
import { JobStatusChip } from './Badges';
import { Bump } from './Bump';
import { ErrorBanner, Loading } from './Feedback';
import { RunCronDialog } from './OperationDialogs';

/**
 * The namespace's CronJobs with Run now, and their recent jobs. With `only`,
 * just that CronJob and its jobs, without the run button (its page has one).
 */
export function CronJobsPanel({ cluster, namespace, flatTop, only }: { cluster: string; namespace: string; flatTop?: boolean; only?: string }) {
  const { me } = useAuth();
  const now = useNow(30000);
  const list = useApi(() => api.cronJobs(cluster, namespace), [cluster, namespace, me.breakGlass?.id]);
  const [run, setRun] = useState<CronJobSummary | null>(null);
  const [hl, setHl] = useState<string | null>(null);
  useLive({ cluster, namespace, ...(only ? { kind: 'CronJob', name: only } : {}) }, (changes) => {
    if (changes.length === 0 || changes.some((c) => c.kind === 'CronJob')) list.reload();
  });
  const crons = (list.data ?? []).filter((c) => !only || c.name === only);
  const jobs: (JobSummary & { cronObj: CronJobSummary })[] = crons
    .flatMap((c) => c.recentJobs.map((j) => ({ ...j, cronObj: c })))
    .sort((a, b) => Date.parse(b.startedAt ?? '') - Date.parse(a.startedAt ?? ''));
  const jobRows = useAnimatedList(list.data ? jobs : undefined, (j) => j.name, (j) => `${j.status}|${j.succeeded}`, `${cluster}/${namespace}/${only ?? ''}`);

  if (list.loading && !list.data) return <Loading what="cron jobs" />;
  if (list.error) return <ErrorBanner error={list.error} onRetry={list.reload} />;
  const cols = only ? 6 : 7;
  const newest = jobs.find((j) => j.manual && j.status === 'running');

  return (
    <>
      <div className="card" style={flatTop ? { borderTop: 0, borderRadius: '0 0 8px 8px' } : undefined}>
        <div className="tbl-wrap">
          <table className="tbl nowrap">
            <thead><tr><th>CronJob</th><th>Schedule</th><th>Suspended</th><th>Last run</th><th>Result</th><th>Active</th>{!only && <th style={{ textAlign: 'right' }} />}</tr></thead>
            <tbody>
              {crons.length === 0 && <tr><td colSpan={cols} className="empty">{only ? 'This CronJob was not found.' : 'No CronJobs you can use in this namespace.'}</td></tr>}
              {crons.map((c) => {
                const forbidRunning = c.concurrencyPolicy === 'Forbid' && c.activeJobs > 0;
                const runningJob = c.recentJobs.find((j) => j.status === 'running');
                return (
                  <tr key={c.name} className={hl === c.name ? 'sel' : undefined}>
                    <td className="mono">{c.name}</td>
                    <td><span className="mono">{c.schedule}</span><div className="muted" style={{ fontSize: 12 }}>{describeCron(c.schedule ?? '')}</div></td>
                    <td>{c.suspended ? <span className="chip warn">Suspended</span> : 'No'}</td>
                    <td>{c.lastScheduleAt ? <>{fmtTime(c.lastScheduleAt)} · {fmtRelative(c.lastScheduleAt, now)}{Date.parse(c.lastScheduleAt) < now - 24 * 3600_000 && <span className="muted"> ({fmtDay(c.lastScheduleAt)})</span>}</> : <span className="muted">Never</span>}</td>
                    <td>{c.lastResult ? <JobStatusChip status={c.lastResult} /> : <span className="muted">—</span>}</td>
                    <td><Bump value={c.activeJobs} /></td>
                    {!only && (
                      <td style={{ textAlign: 'right' }}>
                        {forbidRunning ? (
                          <div className="col" style={{ alignItems: 'flex-end', gap: 2 }}>
                            <span className="btn dis sm" role="button" aria-disabled="true" tabIndex={0}>Run now</span>
                            <span className="muted" style={{ fontSize: 11.5 }}>
                              A run is already in progress.{' '}
                              <a href="#recent-jobs" onClick={() => setHl(runningJob?.name ?? null)}>View running job</a>
                            </span>
                          </div>
                        ) : (
                          <ActionButton action="run-job" decision={c.access['run-job']} label="Run now" icon="bolt" size="sm" target={c.name} hint="inline" request={{ cluster, namespace, workload: { kind: 'CronJob', name: c.name } }} onClick={() => setRun(c)} />
                        )}
                      </td>
                    )}
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      </div>

      <div className="col" style={{ gap: 8, marginTop: 8 }} id="recent-jobs">
        <div className="row"><b>Recent jobs</b><span className="muted">newest first</span></div>
        <div className="card">
          <div className="tbl-wrap">
            <table className="tbl nowrap">
              <thead><tr><th>Job</th><th>Triggered by</th><th>Started</th><th>Status</th><th>Pods</th><th style={{ textAlign: 'right' }} /></tr></thead>
              <tbody>
                {jobRows.length === 0 && <tr><td colSpan={6} className="empty">No recent jobs.</td></tr>}
                {jobRows.map(({ item: j, anim }) => (
                  <tr key={j.name} className={rowClass(anim, j.name === hl || j.name === newest?.name ? 'sel' : '')}>
                    <td><span className="mono">{j.name}</span>{j.name === newest?.name && <span className="chip info" style={{ marginLeft: 6 }}>New</span>}</td>
                    <td>{j.manual ? `${j.triggeredBy ?? 'Unknown'} · manual` : 'Schedule'}</td>
                    <td>{fmtRelative(j.startedAt, now)}</td>
                    <td><JobStatusChip status={j.status} /></td>
                    <td className="mono">{j.succeeded}/{j.completions}</td>
                    <td style={{ textAlign: 'right' }}>
                      {j.podNames[0] && (
                        <Link className="btn sm" to={logsUrl({ cluster, namespace, kind: 'CronJob', name: j.cronJob }, j.podNames[0])}>Logs</Link>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      </div>
      {run && (
        <RunCronDialog
          cron={run}
          onClose={(changed) => { setRun(null); if (changed) list.reload(); }}
          onViewRunning={() => setHl(run.recentJobs.find((j) => j.status === 'running')?.name ?? null)}
        />
      )}
    </>
  );
}
