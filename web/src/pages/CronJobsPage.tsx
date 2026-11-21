import { useParams, useSearchParams } from 'react-router-dom';
import { ErrorBanner, Loading } from '../components/Feedback';
import { JobsTable } from '../components/JobsTable';
import { Crumbs, PageHeader } from '../components/PageHeader';
import { useCronJobs } from '../hooks/useCronJobs';
import { jobRows } from '../lib/jobs';
import { namespaceUrl } from '../lib/routes';

/** Every run of the namespace's CronJobs the cluster still keeps. */
export function CronJobsPage() {
  const { cluster = '', ns = '' } = useParams();
  const [sp] = useSearchParams();
  const list = useCronJobs(cluster, ns);
  return (
    <>
      <Crumbs items={[{ label: cluster, to: '/' }, { label: ns, to: namespaceUrl(cluster, ns), mono: true }, { label: 'Job history' }]} />
      <PageHeader title="Job history" subtitle={<>Runs of the CronJobs in <span className="mono">{ns}</span> on <span className="mono">{cluster}</span></>} />
      {list.loading && !list.data ? <Loading what="jobs" />
        : list.error ? <ErrorBanner error={list.error} onRetry={list.reload} />
          : list.data && list.data.length === 0 ? <div className="card"><div className="empty">No CronJobs you can use in this namespace.</div></div>
            : <JobsTable cluster={cluster} namespace={ns} jobs={jobRows(list.data ?? [])} showCronJob highlight={sp.get('job')} />}
    </>
  );
}
