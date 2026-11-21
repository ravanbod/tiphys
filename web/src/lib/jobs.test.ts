import { describe, expect, it } from 'vitest';
import type { CronJobSummary, JobSummary } from '../api/types';
import { filterJobs, isFreshRun, jobFailure, jobRows, jobSeconds, lastRunAt } from './jobs';

const NOW = Date.parse('2026-11-20T12:00:00Z');
const job = (over: Partial<JobSummary> = {}): JobSummary => ({
  name: 'report-1', cronJob: 'report', triggeredBy: null, manual: false, status: 'succeeded', active: 0, succeeded: 1, failed: 0, completions: 1,
  startedAt: '2026-11-20T11:00:00Z', finishedAt: '2026-11-20T11:02:30Z', reason: null, message: null, podNames: [], ...over,
});
const cron = (name: string, jobs: JobSummary[], lastScheduleAt: string | null = null) => ({ name, recentJobs: jobs, lastScheduleAt }) as CronJobSummary;

describe('jobs', () => {
  it('measures a run, or a running job until now', () => {
    expect(jobSeconds(job(), NOW)).toBe(150);
    expect(jobSeconds(job({ status: 'running', finishedAt: null }), NOW)).toBe(3600);
    expect(jobSeconds(job({ startedAt: null }), NOW)).toBeNull();
    expect(jobSeconds(job({ status: 'failed', finishedAt: null }), NOW)).toBeNull();
  });
  it('explains failures', () => {
    expect(jobFailure(job())).toBe('');
    expect(jobFailure(job({ status: 'failed', failed: 3, reason: 'BackoffLimitExceeded' }))).toBe('BackoffLimitExceeded · 3 attempts');
    expect(jobFailure(job({ status: 'failed', failed: 1, reason: 'DeadlineExceeded' }))).toBe('DeadlineExceeded');
    expect(jobFailure(job({ status: 'failed', failed: 2 }))).toBe('2 attempts');
  });
  it('marks only the user\'s own recent manual run as fresh', () => {
    const mine = job({ manual: true, triggeredBy: 'sara', startedAt: '2026-11-20T11:58:00Z' });
    expect(isFreshRun(mine, 'sara', NOW)).toBe(true);
    expect(isFreshRun(mine, 'nima', NOW)).toBe(false);
    expect(isFreshRun({ ...mine, startedAt: '2026-11-20T11:50:00Z' }, 'sara', NOW)).toBe(false);
    expect(isFreshRun({ ...mine, manual: false }, 'sara', NOW)).toBe(false);
  });
  it('takes the last run from the newest job, manual or not', () => {
    expect(lastRunAt(cron('a', [job({ startedAt: '2026-11-20T11:59:00Z' })], '2026-11-20T10:00:00Z'))).toBe('2026-11-20T11:59:00Z');
    expect(lastRunAt(cron('a', [], '2026-11-20T10:00:00Z'))).toBe('2026-11-20T10:00:00Z');
  });
  it('merges and filters the jobs of several CronJobs', () => {
    const rows = jobRows([
      cron('a', [job({ name: 'a-2', cronJob: 'a', startedAt: '2026-11-20T11:00:00Z' })]),
      cron('b', [job({ name: 'b-1', cronJob: 'b', status: 'failed', startedAt: '2026-11-20T11:30:00Z' })]),
    ]);
    expect(rows.map((r) => `${r.name}:${r.cron.name}`)).toEqual(['b-1:b', 'a-2:a']);
    expect(filterJobs(rows, { status: 'failed', cronJob: '' }).map((r) => r.name)).toEqual(['b-1']);
    expect(filterJobs(rows, { status: '', cronJob: 'a' }).map((r) => r.name)).toEqual(['a-2']);
  });
});
