import { useState } from 'react';
import { api } from '../api';
import type { ArgoAppStatus, ArgoDeploy, NamespaceApp } from '../api';
import { useAuth } from '../context/auth';
import { useApi } from '../hooks/useApi';
import { useNow } from '../hooks/useNow';
import { appScopeNote, appSyncTarget, explainDecision, grantedOrClosed } from '../lib/decision';
import { fmtDateTime, fmtRelative } from '../lib/time';
import { HealthChip, SyncChip } from './Badges';
import { ActionButton } from './ActionButton';
import { DiffList } from './DiffView';
import { Dialog } from './Dialog';
import { ErrorBanner, Skeleton } from './Feedback';
import { Icon } from './Icon';
import { RollbackAppDialog } from './OperationDialogs';
import { breakGlassOn } from '../lib/breakGlass';

type Tab = 'status' | 'history' | 'diff';

const short = (rev: string) => (/^[0-9a-f]{40}$/.test(rev) ? rev.slice(0, 7) : rev);

const ROLLBACK_BLOCKED: Record<Exclude<ArgoDeploy['rollbackBlocked'], null | 'current'>, string> = {
  auto_sync: 'This application syncs automatically, so Argo CD does not roll it back. Turn off auto-sync first.',
  multi_source: 'Multi-source applications are rolled back in Argo CD.',
  source_changed: "The application's source changed since this deploy, so it is rolled back in Argo CD.",
};

/** Who started a deploy: the portal user when the audit knows, else Argo CD's initiator. */
function DeployedBy({ h }: { h: ArgoDeploy }) {
  if (h.portalUser) return <>{h.portalUser}{h.portalAction === 'argo.rollback' && <span className="muted"> (rollback)</span>}</>;
  return h.automated ? <span className="muted">automated</span> : <>{h.initiatedBy || '—'}</>;
}

/**
 * An Argo CD application's details: status and the last operation, deploy
 * history (with Roll back for users who hold rollback on it), and what a
 * sync would change (only for users who hold sync on it). Each tab is
 * fetched from Argo CD the first time it is opened.
 */
export function ArgoAppPanel({ app, cluster, ns, argoLink, onClose, onChanged }: {
  app: NamespaceApp; cluster: string; ns: string; argoLink: string; onClose: () => void;
  /** After a rollback ran, so the page refetches the app. */
  onChanged?: () => void;
}) {
  const { me, usesGrants } = useAuth();
  const now = useNow(30_000);
  const [tab, setTab] = useState<Tab>('status');
  const [opened, setOpened] = useState<Tab[]>(['status']);
  if (!opened.includes(tab)) setOpened([...opened, tab]);
  const args = [cluster, ns, app.appNamespace, app.name] as const;
  const status = useApi(() => api.appStatus(...args), [...args], { enabled: opened.includes('status') });
  const history = useApi(() => api.appHistory(...args), [...args], { enabled: opened.includes('history') });
  const showDiff = grantedOrClosed(app.access.sync);
  const diffOk = app.access.sync.allowed;
  const diff = useApi(() => api.appDiff(...args), [...args], { enabled: diffOk && opened.includes('diff') });
  const tabBtn = (id: Tab, label: React.ReactNode) => (
    <button type="button" role="tab" aria-selected={tab === id} className={`tabbtn${tab === id ? ' on' : ''}`} onClick={() => setTab(id)}>{label}</button>
  );
  const changes = diff.data?.resources.length;
  const showRollback = grantedOrClosed(app.access.rollback);
  const [rollingBack, setRollingBack] = useState<ArgoDeploy | null>(null);
  const cols = showRollback ? 5 : 4;
  const scope = appScopeNote(app.access.sync, app.adminOnlyResources);

  return (
    <Dialog
      width={920} onClose={onClose} label={`Argo CD application ${app.name}`}
      title={<span className="row"><span className="mono">{app.name}</span><HealthChip health={app.health} /><SyncChip sync={app.sync} /></span>}
      subtitle={<>Project <span className="mono">{app.project}</span> · <span className="mono">{cluster}</span></>}
      footer={<>
        {argoLink && <a className="btn" href={argoLink} target="_blank" rel="noreferrer"><Icon name="ext" />Open in Argo CD</a>}
        <span className="grow" />
        <button type="button" className="btn" data-autofocus onClick={onClose}>Close</button>
      </>}
    >
      <div className="tabs" role="tablist">
        {tabBtn('status', 'Status')}
        {tabBtn('history', 'History')}
        {showDiff && tabBtn('diff', <>Changes {changes !== undefined && <span className="c">{changes}</span>}</>)}
      </div>
      <div style={{ maxHeight: '60vh', overflow: 'auto' }}>
        {tab === 'status' && scope && (
          <div className="banner yel" style={{ marginTop: 10 }}>
            <Icon name="warn" />
            <span>
              <b>Only an admin can sync this application.</b> {scope.text}
              {scope.shown.length > 0 && (
                <span className="row wrap" style={{ gap: 4, marginTop: 4 }}>
                  {scope.shown.map((r) => <span key={r} className="tag mono">{r}</span>)}
                  {scope.more > 0 && <span className="muted">and {scope.more} more</span>}
                </span>
              )}
            </span>
          </div>
        )}
        {tab === 'status' && (status.error ? <ErrorBanner error={status.error} onRetry={status.reload} /> : !status.data ? <Skeleton rows={4} /> : <StatusTab s={status.data} now={now} />)}
        {tab === 'history' && (history.error ? <ErrorBanner error={history.error} onRetry={history.reload} /> : !history.data ? <Skeleton rows={4} /> : (
          <div className="tbl-wrap">
            <table className="tbl">
              <thead><tr><th>Revision</th><th>Commit</th><th>Deployed</th><th>By</th>{showRollback && <th />}</tr></thead>
              <tbody>
                {history.data.length === 0 && <tr><td colSpan={cols} className="empty">No deploys recorded.</td></tr>}
                {history.data.map((h) => (
                  <tr key={h.id}>
                    <td className="mono" style={{ whiteSpace: 'nowrap' }}>
                      {h.revision ? short(h.revision) : h.revisions.map(short).join(', ')}
                      {h.current && <span className="chip ok" style={{ marginLeft: 6 }}>Current</span>}
                    </td>
                    <td>
                      {h.commit
                        ? <><b style={{ fontWeight: 500 }}>{h.commit.message.split('\n')[0]}</b><div className="muted" style={{ fontSize: 12 }}>{h.commit.author}</div></>
                        : <span className="muted" style={{ fontSize: 12 }}>{h.source}</span>}
                    </td>
                    <td style={{ whiteSpace: 'nowrap' }} title={fmtDateTime(h.deployedAt)}>{fmtRelative(h.deployedAt, now)}</td>
                    <td><DeployedBy h={h} /></td>
                    {showRollback && (
                      <td style={{ textAlign: 'right' }}>
                        {!h.current && (
                          <ActionButton
                            action="rollback" decision={app.access.rollback} label="Roll back" icon="restart" size="sm"
                            target={app.name} request={{ cluster, namespace: app.syncNamespace || ns }}
                            forceDisabled={h.rollbackBlocked && h.rollbackBlocked !== 'current' ? ROLLBACK_BLOCKED[h.rollbackBlocked] : undefined}
                            onClick={() => setRollingBack(h)}
                          />
                        )}
                      </td>
                    )}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ))}
        {tab === 'diff' && (!diffOk
          ? <div className="empty">{explainDecision(app.access.sync, { action: 'sync', target: appSyncTarget(app, ns), canBreakGlass: usesGrants && breakGlassOn(me) })?.text}</div>
          : diff.error ? <ErrorBanner error={diff.error} onRetry={diff.reload} /> : !diff.data ? <Skeleton rows={4} /> : (
            <div style={{ paddingTop: 10 }}><DiffList diff={diff.data} /></div>
          ))}
      </div>
      {rollingBack && (
        <RollbackAppDialog
          app={app} deploy={rollingBack} cluster={cluster} namespace={ns}
          onClose={(changed) => { setRollingBack(null); if (changed) { history.reload(); status.reload(); onChanged?.(); } }}
        />
      )}
    </Dialog>
  );
}

function StatusTab({ s, now }: { s: ArgoAppStatus; now: number }) {
  const op = s.lastOperation;
  return (
    <div className="col" style={{ gap: 12, paddingTop: 10 }}>
      <dl className="kv" style={{ gridTemplateColumns: '110px 1fr' }}>
        <dt>Health</dt><dd><HealthChip health={s.health || 'Unknown'} />{s.healthMessage && <span className="muted"> {s.healthMessage}</span>}</dd>
        <dt>Sync</dt><dd><SyncChip sync={s.syncStatus || 'Unknown'} />{s.syncRevision && <span className="mono muted"> to {short(s.syncRevision)}</span>}</dd>
      </dl>
      {s.conditions.map((c, i) => (
        <div key={i} className={`banner ${/Error/.test(c.type) ? 'red' : 'yel'}`}>
          <Icon name="warn" /><span><b>{c.type}</b> {c.message}</span>
        </div>
      ))}
      <div>
        <b>Last operation</b>
        {!op ? <div className="muted">None yet.</div> : (
          <>
            <div className="row wrap" style={{ gap: 8, margin: '6px 0' }}>
              <span className={`chip ${op.phase === 'Succeeded' ? 'ok' : op.phase === 'Failed' || op.phase === 'Error' ? 'err' : 'info'}`}>{op.phase}</span>
              <span title={fmtDateTime(op.startedAt)}>{fmtRelative(op.startedAt, now)}</span>
              <span className="muted">by {op.automated ? 'automated sync' : op.initiatedBy || 'unknown'}</span>
              {op.revision && <span className="mono muted">{short(op.revision)}</span>}
            </div>
            {op.message && <div style={{ marginBottom: 6 }}>{op.message}</div>}
            {op.resources.length > 0 && (
              <div className="tbl-wrap">
                <table className="tbl">
                  <thead><tr><th>Kind</th><th>Name</th><th>Result</th><th>Message</th></tr></thead>
                  <tbody>
                    {op.resources.map((r, i) => (
                      <tr key={i}>
                        <td><span className="tag">{r.kind}</span>{r.hookType && <span className="muted"> {r.hookType} hook</span>}</td>
                        <td className="mono">{r.namespace ? `${r.namespace}/` : ''}{r.name}</td>
                        <td><span className={`chip ${r.status === 'SyncFailed' || r.hookPhase === 'Failed' ? 'err' : ''}`}>{r.hookPhase || r.status}</span></td>
                        <td style={{ overflowWrap: 'anywhere' }}>{r.message}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </>
        )}
      </div>
    </div>
  );
}
