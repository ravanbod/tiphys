import { Fragment, useEffect, useMemo, useRef, useState } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { api } from '../api';
import type { AppSummary, NamespaceSummary } from '../api';
import { ActionChips, EnvBadge, HealthChip, KindTag, SyncChip } from '../components/Badges';
import { Bump } from '../components/Bump';
import { ErrorBanner, Loading } from '../components/Feedback';
import { Icon } from '../components/Icon';
import { PageHeader } from '../components/PageHeader';
import { Seg } from '../components/Seg';
import { useAuth } from '../context/auth';
import { useLive } from '../context/live';
import { rowClass, useAnimatedList } from '../hooks/useAnimatedList';
import { useApi } from '../hooks/useApi';
import { starKey, useStars } from '../hooks/useStars';
import { useLocalStorage } from '../hooks/useLocalStorage';
import { logsUrl, namespaceUrl, workloadUrl } from '../lib/routes';
import { ARGO_UI } from '../lib/features';

function WorkloadRows({ cluster, namespace, cols }: { cluster: string; namespace: string; cols: number }) {
  const { me } = useAuth();
  const list = useApi(() => api.workloads(cluster, namespace), [cluster, namespace, me.breakGlass?.id]);
  useLive({ cluster, namespace }, (changes) => {
    if (changes.length === 0 || changes.some((c) => c.type !== 'event')) list.reload();
  });
  const rows = useAnimatedList(list.data, (w) => `${w.kind}/${w.name}`, (w) => `${w.ready}/${w.desired}|${w.suspended}`, `${cluster}/${namespace}`);
  if (list.loading && !list.data) {
    return <tr className="sub"><td /><td colSpan={cols - 1}><div className="skel" style={{ width: 240 }} /></td></tr>;
  }
  if (list.error) {
    return (
      <tr className="sub">
        <td />
        <td colSpan={cols - 1}>
          <ErrorBanner error={list.error} onRetry={list.reload} />
        </td>
      </tr>
    );
  }
  if (rows.length === 0) return <tr className="sub"><td /><td colSpan={cols - 1} className="muted">No workloads you can use here.</td></tr>;
  return (
    <>
      {rows.map(({ item: w, key, anim }) => {
        const loc = { cluster, namespace, kind: w.kind, name: w.name };
        return (
          <tr className={rowClass(anim, 'sub')} key={key}>
            <td />
            <td colSpan={2}><span className="row" style={{ paddingLeft: 24 }}><KindTag kind={w.kind} /><span className="mono">{w.name}</span></span></td>
            <td colSpan={Math.max(1, cols - 5)}><span className="mono muted">{w.kind === 'CronJob' ? (w.suspended ? 'suspended' : w.schedule) : <><Bump value={`${w.ready}/${w.desired}`} /> ready</>}</span></td>
            <td colSpan={2} style={{ textAlign: 'right' }}>
              <Link className="btn ghost sm" to={logsUrl(loc)}>Logs</Link>
              <Link className="btn ghost sm" to={workloadUrl(loc)}>Open</Link>
            </td>
          </tr>
        );
      })}
    </>
  );
}

/** The first Argo app of a namespace, cut to fit, with "+N" for the others (all in the tooltip). */
function AppNames({ apps }: { apps: string[] }) {
  if (apps.length === 0) return <span className="mono">—</span>;
  return (
    <span className="row" style={{ gap: 6, flexWrap: 'nowrap' }} title={apps.join('\n')}>
      <span className="mono" style={{ maxWidth: 240, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{apps[0]}</span>
      {apps.length > 1 && <span className="muted" style={{ whiteSpace: 'nowrap' }}>+{apps.length - 1}</span>}
    </span>
  );
}

const NO_APPS: AppSummary[] = [];
const NO_NS: NamespaceSummary[] = [];

export function Workloads() {
  const [sp, setSp] = useSearchParams();
  const { me } = useAuth();
  const [storedGroup, setGroup] = useLocalStorage<'app' | 'ns'>('tiphys.group', 'app');
  // Without the Argo UI the namespace view (read from the clusters) is the only one.
  const group: 'app' | 'ns' = ARGO_UI ? storedGroup : 'ns';
  const q = sp.get('q') ?? '';
  const [cluster, setCluster] = useState('');
  const [env, setEnv] = useState('');
  const [project, setProject] = useState('');
  const [open, setOpen] = useState<Set<string>>(new Set());
  const { stars, toggle, isStarred } = useStars();
  const apps = useApi(() => api.apps(), [me.breakGlass?.id], { enabled: group === 'app' });
  const nss = useApi(() => api.namespaces(), [me.breakGlass?.id], { enabled: group === 'ns' });
  const filterRef = useRef<HTMLInputElement>(null);

  const expand = sp.get('expand');
  useEffect(() => {
    if (expand) setOpen((o) => new Set(o).add(expand));
  }, [expand]);

  const setQ = (v: string) => {
    const n = new URLSearchParams(sp);
    if (v) n.set('q', v); else n.delete('q');
    setSp(n, { replace: true });
  };

  const rows = group === 'app' ? (apps.data ?? NO_APPS) : NO_APPS;
  const nsRows = group === 'ns' ? (nss.data ?? NO_NS) : NO_NS;
  const clusters = useMemo(() => [...new Set([...rows.map((r) => r.cluster), ...nsRows.map((r) => r.cluster)])].sort(), [rows, nsRows]);
  const envs = useMemo(() => [...new Set([...rows.map((r) => r.environment), ...nsRows.map((r) => r.environment)])].sort(), [rows, nsRows]);
  const projects = useMemo(() => [...new Set([...rows.map((r) => r.project), ...nsRows.flatMap((r) => r.argoProjects)])].sort(), [rows, nsRows]);

  const match = (name: string, ns: string) => !q || name.toLowerCase().includes(q.toLowerCase()) || ns.toLowerCase().includes(q.toLowerCase());
  const fApps = rows.filter((a) => (!cluster || a.cluster === cluster) && (!env || a.environment === env) && (!project || a.project === project) && match(a.name, a.namespace));
  const fNs = nsRows.filter((n) => (!cluster || n.cluster === cluster) && (!env || n.environment === env) && (!project || n.argoProjects.includes(project)) && (match(n.namespace, n.namespace) || n.apps.some((a) => match(a, ''))));

  const toggleOpen = (k: string) => setOpen((o) => { const n = new Set(o); if (n.has(k)) n.delete(k); else n.add(k); return n; });
  const loading = group === 'app' ? apps.loading && !apps.data : nss.loading && !nss.data;
  const error = group === 'app' ? apps.error : nss.error;
  const total = group === 'app' ? rows.length : nsRows.length;
  const shown = group === 'app' ? fApps.length : fNs.length;
  const clusterCount = new Set((group === 'app' ? rows : nsRows).map((r) => r.cluster)).size;

  const appKey = (a: AppSummary) => starKey(a.cluster, a.namespace, a.name);
  const starred = fApps.filter((a) => isStarred(appKey(a)));
  const others = fApps.filter((a) => !isStarred(appKey(a)));
  void stars;

  const renderApp = (a: AppSummary) => {
    const k = appKey(a);
    const isOpen = open.has(k);
    const on = isStarred(k);
    return (
      <Fragment key={k}>
        <tr id={`app-${k}`} className={expand === k ? 'sel' : undefined}>
          <td>
            <button type="button" className="btn ghost sm" aria-pressed={on} aria-label={`${on ? 'Unstar' : 'Star'} ${a.name}`}
              style={{ color: on ? 'var(--prod)' : 'var(--tx3)', padding: '0 4px' }}
              onClick={() => toggle({ key: k, name: a.name, cluster: a.cluster, namespace: a.namespace, environment: a.environment })}>
              <Icon name="star" style={on ? { fill: 'currentColor' } : undefined} />
            </button>
          </td>
          <td>
            <button type="button" className="row linkbtn" aria-expanded={isOpen} onClick={() => toggleOpen(k)}>
              <Icon name={isOpen ? 'chevd' : 'chev'} />
              <b className="mono" style={{ fontSize: 13, fontWeight: on ? 600 : 500 }}>{a.name}</b>
            </button>
          </td>
          <td>{a.project}</td><td className="mono">{a.cluster}</td><td><EnvBadge env={a.environment} /></td><td className="mono">{a.namespace}</td>
          <td><HealthChip health={a.health} /></td><td><SyncChip sync={a.sync} /></td>
          <td style={{ textAlign: 'right' }}>{a.workloadCount ?? '—'}</td>
        </tr>
        {isOpen && <WorkloadRows cluster={a.cluster} namespace={a.namespace} cols={9} />}
      </Fragment>
    );
  };

  return (
    <>
      <PageHeader title="Workloads" subtitle={loading || error ? ' ' : `${total} ${group === 'app' ? 'applications' : 'namespaces'} you can access across ${clusterCount} cluster${clusterCount === 1 ? '' : 's'}`}>
        {ARGO_UI && <Seg label="Group by" value={group} onChange={setGroup} options={[{ value: 'app', label: 'By Argo app' }, { value: 'ns', label: 'By namespace' }]} />}
      </PageHeader>

      <div className="row wrap">
        <select className="sel" aria-label="Cluster" value={cluster} onChange={(e) => setCluster(e.target.value)}>
          <option value="">All clusters</option>{clusters.map((c) => <option key={c}>{c}</option>)}
        </select>
        <select className="sel" aria-label="Environment" value={env} onChange={(e) => setEnv(e.target.value)}>
          <option value="">All environments</option>{envs.map((c) => <option key={c}>{c}</option>)}
        </select>
        {ARGO_UI && (
          <select className="sel" aria-label="Argo project" value={project} onChange={(e) => setProject(e.target.value)}>
            <option value="">All Argo projects</option>{projects.map((c) => <option key={c}>{c}</option>)}
          </select>
        )}
        <label className="search" style={{ width: 280, height: 28, background: 'var(--sf)' }}>
          <Icon name="search" />
          <input ref={filterRef} value={q} onChange={(e) => setQ(e.target.value)} placeholder="Filter by name" aria-label="Filter by name" />
        </label>
        <span className="grow" />
        {!loading && <span className="muted" aria-live="polite">Showing {shown} of {total}</span>}
      </div>

      {loading ? <Loading what="workloads" /> : error ? <ErrorBanner error={error} onRetry={group === 'app' ? apps.reload : nss.reload} /> : (
        <div className="card">
          <div className="tbl-wrap">
            {group === 'app' ? (
              <table className="tbl">
                <thead><tr><th style={{ width: 44 }} /><th>Application</th><th>Argo project</th><th>Cluster</th><th>Env</th><th>Namespace</th><th>Health</th><th>Sync</th><th style={{ textAlign: 'right' }}>Workloads</th></tr></thead>
                <tbody>
                  {fApps.length === 0 && <tr><td colSpan={9} className="empty">{total === 0 ? 'You have no access yet. Ask for it under My access.' : 'No applications match these filters.'}</td></tr>}
                  {starred.length > 0 && <tr className="sec"><td colSpan={9}>Starred</td></tr>}
                  {starred.map(renderApp)}
                  {others.length > 0 && <tr className="sec"><td colSpan={9}>All applications</td></tr>}
                  {others.map(renderApp)}
                </tbody>
              </table>
            ) : (
              <table className="tbl">
                <thead><tr><th style={{ width: 44 }} /><th>Namespace</th>{ARGO_UI && <th>Argo projects</th>}<th>Cluster</th><th>Env</th><th>Applications</th><th>You can</th></tr></thead>
                <tbody>
                  {fNs.length === 0 && <tr><td colSpan={ARGO_UI ? 7 : 6} className="empty">No namespaces match these filters.</td></tr>}
                  {fNs.map((n: NamespaceSummary) => {
                    const k = `${n.cluster}/${n.namespace}`;
                    const isOpen = open.has(k);
                    return (
                      <Fragment key={k}>
                        <tr>
                          <td />
                          <td><span className="row"><button type="button" className="row linkbtn" aria-expanded={isOpen} aria-label={`${isOpen ? 'Hide' : 'Show'} workloads of ${n.namespace}`} onClick={() => toggleOpen(k)}><Icon name={isOpen ? 'chevd' : 'chev'} /></button><Link className="mono" to={namespaceUrl(n.cluster, n.namespace)} title="Open namespace"><b style={{ fontSize: 13, fontWeight: 500 }}>{n.namespace}</b></Link></span></td>
                          {ARGO_UI && <td>{n.argoProjects.join(', ') || '—'}</td>}<td className="mono">{n.cluster}</td><td><EnvBadge env={n.environment} /></td>
                          <td><AppNames apps={n.apps} /></td><td><ActionChips actions={n.access} nowrap /></td>
                        </tr>
                        {isOpen && <WorkloadRows cluster={n.cluster} namespace={n.namespace} cols={ARGO_UI ? 7 : 6} />}
                      </Fragment>
                    );
                  })}
                </tbody>
              </table>
            )}
          </div>
        </div>
      )}
      {stars.length === 0 && group === 'app' && !loading && !error && total > 0 && <span className="muted" style={{ fontSize: 12 }}>Tip: star an application to pin it to the top and to the sidebar. Press / to search.</span>}
    </>
  );
}
