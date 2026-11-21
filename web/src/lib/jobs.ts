import type { CronJobSummary, JobSummary } from '../api/types';

/** How long a manual run counts as just started by its user ("New"). */
export const FRESH_RUN_MS = 5 * 60_000;

/** Seconds from start to finish, or to now while running; null without a start. */
export function jobSeconds(j: JobSummary, now: number): number | null {
  if (!j.startedAt) return null;
  const end = j.finishedAt ? Date.parse(j.finishedAt) : j.status === 'running' ? now : null;
  return end === null ? null : Math.max(0, (end - Date.parse(j.startedAt)) / 1000);
}

/** "BackoffLimitExceeded · 3 attempts"; '' unless failed. */
export function jobFailure(j: JobSummary): string {
  if (j.status !== 'failed') return '';
  const attempts = j.failed > 1 ? `${j.failed} attempts` : '';
  return [j.reason ?? '', attempts].filter(Boolean).join(' · ');
}

/** A manual run the user started in the last few minutes. */
export function isFreshRun(j: JobSummary, username: string, now: number): boolean {
  return j.manual && j.triggeredBy === username && !!j.startedAt && now - Date.parse(j.startedAt) < FRESH_RUN_MS;
}

/** When the CronJob last ran, scheduled or manual: the newest Job's start. */
export function lastRunAt(c: CronJobSummary): string | null {
  return c.recentJobs[0]?.startedAt ?? c.lastScheduleAt;
}

export type JobRow = JobSummary & { cron: CronJobSummary };

/** Every Job of the CronJobs, newest first, each with its CronJob. */
export function jobRows(crons: CronJobSummary[]): JobRow[] {
  return crons
    .flatMap((c) => c.recentJobs.map((j) => ({ ...j, cron: c })))
    .sort((a, b) => Date.parse(b.startedAt ?? '') - Date.parse(a.startedAt ?? ''));
}

export type JobStatusFilter = '' | JobSummary['status'];

export function filterJobs<T extends JobSummary>(jobs: T[], f: { status: JobStatusFilter; cronJob: string }): T[] {
  return jobs.filter((j) => (!f.status || j.status === f.status) && (!f.cronJob || j.cronJob === f.cronJob));
}
