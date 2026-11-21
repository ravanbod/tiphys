import { useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import type { CronJobSummary, JobSummary, WorkloadSummary } from '../api';
import { rowClass, useAnimatedList } from '../hooks/useAnimatedList';
import { useCronJobs } from '../hooks/useCronJobs';
import { useNow } from '../hooks/useNow';
import { describeCron } from '../lib/cron';
import { lastRunAt } from '../lib/jobs';
import { cronJobsUrl, workloadUrl } from '../lib/routes';
import { fmtDateTime, fmtRelative } from '../lib/time';
import { ActionButton } from './ActionButton';
import { JobStatusChip } from './Badges';
import { Bump } from './Bump';
import { ErrorBanner, Loading } from './Feedback';
import { RunCronDialog } from './OperationDialogs';

/** The last results, oldest to newest, one dot per Job. */
export function ResultStrip({ jobs, max = 10 }: { jobs: JobSummary[]; max?: number }) {
  const shown = jobs.slice(0, max).reverse();
  if (shown.length === 0) return null;
  return (
    <span className="row" style={{ gap: 3, flexWrap: 'nowrap' }} role="img" aria-label={`Last ${shown.length} results, newest last: ${shown.map((j) => j.status).join(', ')}`}>
      {shown.map((j) => <span key={j.name} className={`dot ${RESULT_TONE[j.status]}`} title={`${j.name}: ${j.status}`} />)}
    </span>
  );
}

/** "Every day at 02:00 (Europe/Berlin)"; the controller's zone is usually UTC. */
export function ScheduleText({ cron }: { cron: Pick<CronJobSummary, 'schedule' | 'timeZone'> }) {
  const d = describeCron(cron.schedule ?? '');
  return (
    <>
      <span className="mono">{cron.schedule}</span>
      <div className="muted" style={{ fontSize: 12 }} title={cron.timeZone ? undefined : 'No time zone is set: the schedule runs in the CronJob controller\'s time zone, usually UTC.'}>
        {d}{d && ' '}({cron.timeZone ?? 'UTC'})
      </div>
    </>
  );
}

const RESULT_TONE = { succeeded: 'ok', failed: 'err', running: 'info' } as const;

/** A CronJob row's status in workload lists: last result, schedule, last run. */
export function CronStatus({ w, cron, now }: { w: WorkloadSummary; cron: CronJobSummary | undefined; now: number }) {
  const last = cron && lastRunAt(cron);
  return (
    <span className="row" style={{ gap: 6, flexWrap: 'nowrap' }}>
      {cron?.lastResult && <span className={`dot ${RESULT_TONE[cron.lastResult]}`} role="img" aria-label={`Last run ${cron.lastResult}`} title={`Last run ${cron.lastResult}`} />}
      <span className="mono muted">{w.suspended ? 'suspended' : w.schedule}</span>
      {last && <span className="muted" title={fmtDateTime(last)}>· ran {fmtRelative(last, now)}</span>}
    </span>
  );
}

/** The namespace's CronJobs with Run now; their jobs are on the Job history page. */
export function CronJobsPanel({ cluster, namespace }: { cluster: string; namespace: string }) {
  const now = useNow(30000);
  const list = useCronJobs(cluster, namespace);
  const [run, setRun] = useState<CronJobSummary | null>(null);
  const navigate = useNavigate();
  const crons = list.data ?? [];
  const rows = useAnimatedList(list.data, (c) => c.name, (c) => `${c.lastResult}|${c.activeJobs}|${c.suspended}`, `${cluster}/${namespace}`);

  if (list.loading && !list.data) return <Loading what="cron jobs" />;
  if (list.error) return <ErrorBanner error={list.error} onRetry={list.reload} />;
  const running = (c: CronJobSummary) => `${workloadUrl({ cluster, namespace, kind: 'CronJob', name: c.name })}?result=running`;

  return (
    <>
      <div className="card">
        <div className="tbl-wrap">
          <table className="tbl nowrap">
            <thead><tr><th>CronJob</th><th>Schedule</th><th>Next run</th><th>Last run</th><th>Result</th><th>Active</th><th style={{ textAlign: 'right' }} /></tr></thead>
            <tbody>
              {crons.length === 0 && <tr><td colSpan={7} className="empty">No CronJobs you can use in this namespace.</td></tr>}
              {rows.map(({ item: c, anim }) => {
                const forbidRunning = c.concurrencyPolicy === 'Forbid' && c.activeJobs > 0;
                const last = lastRunAt(c);
                return (
                  <tr key={c.name} className={rowClass(anim)}>
                    <td><Link className="mono" to={workloadUrl({ cluster, namespace, kind: 'CronJob', name: c.name })}>{c.name}</Link></td>
                    <td><ScheduleText cron={c} /></td>
                    <td>{c.suspended ? <span className="chip warn">Suspended</span> : c.nextScheduleAt ? <span title={fmtDateTime(c.nextScheduleAt)}>{fmtRelative(c.nextScheduleAt, now)}</span> : <span className="muted">—</span>}</td>
                    <td>{last ? <span title={fmtDateTime(last)}>{fmtRelative(last, now)}</span> : <span className="muted">Never</span>}</td>
                    <td>
                      <span className="row" style={{ gap: 8, flexWrap: 'nowrap' }}>
                        {c.lastResult ? <JobStatusChip status={c.lastResult} /> : <span className="muted">—</span>}
                        <ResultStrip jobs={c.recentJobs} />
                      </span>
                    </td>
                    <td><Bump value={c.activeJobs} /></td>
                    <td style={{ textAlign: 'right' }}>
                      {forbidRunning ? (
                        <div className="col" style={{ alignItems: 'flex-end', gap: 2 }}>
                          <span className="btn dis sm" role="button" aria-disabled="true" tabIndex={0}>Run now</span>
                          <span className="muted" style={{ fontSize: 11.5 }}>
                            A run is already in progress. <Link to={running(c)}>View running job</Link>
                          </span>
                        </div>
                      ) : (
                        <ActionButton action="run-job" decision={c.access['run-job']} label="Run now" icon="bolt" size="sm" target={c.name} hint="inline" request={{ cluster, namespace, workload: { kind: 'CronJob', name: c.name } }} onClick={() => setRun(c)} />
                      )}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      </div>
      {crons.length > 0 && (
        <div className="row" style={{ marginTop: 8 }}>
          <Link to={cronJobsUrl(cluster, namespace)}>Job history of this namespace</Link>
        </div>
      )}
      {run && (
        <RunCronDialog
          cron={run}
          onClose={(changed) => { setRun(null); if (changed) list.reload(); }}
          onViewRunning={() => navigate(running(run))}
        />
      )}
    </>
  );
}
