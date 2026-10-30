import { useParams } from 'react-router-dom';
import { CronJobsPanel } from '../components/CronJobsPanel';
import { Crumbs, PageHeader } from '../components/PageHeader';
import { namespaceUrl } from '../lib/routes';

export function CronJobsPage() {
  const { cluster = '', ns = '' } = useParams();
  return (
    <>
      <Crumbs items={[{ label: cluster, to: '/' }, { label: ns, to: namespaceUrl(cluster, ns), mono: true }, { label: 'CronJobs' }]} />
      <PageHeader title="CronJobs" subtitle={<>In namespace <span className="mono">{ns}</span> on <span className="mono">{cluster}</span></>} />
      <CronJobsPanel cluster={cluster} namespace={ns} />
    </>
  );
}
