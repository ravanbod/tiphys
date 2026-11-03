import { useEffect, useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { ApiError, api } from '../../api';
import type {
  AccessRequest, Action, ClusterView, EnvironmentOverride, GrantView, ScopeView, UserView,
} from '../../api';
import { ActionChips, Avatar, EnvBadge } from '../../components/Badges';
import { Empty, ErrorBanner, Loading, Skeleton } from '../../components/Feedback';
import { Icon } from '../../components/Icon';
import { PageHeader } from '../../components/PageHeader';
import { SCOPE_KIND_LABEL, ScopeCell, scopeCluster, scopeName } from '../../components/Scope';
import { Seg } from '../../components/Seg';
import { FieldError, ReadOnlyNote } from '../../components/AdminBits';
import '../../components/adminPages.css';
import { useAuth } from '../../context/auth';
import { useToast } from '../../context/toast';
import { useApi } from '../../hooks/useApi';
import { useMedia } from '../../hooks/useMedia';
import { useNow } from '../../hooks/useNow';
import { scopeEnvironment } from '../../lib/adminEnv';
import { BreakGlassRequests } from './BreakGlassRequests';
import { ACTIONS, SYNC_SCOPE_HINT, actionOptionLabel } from '../../lib/grantForm';
import { mergeTarget, sameScope } from '../../lib/requestMerge';
import { fmtDateTime, fmtHours, fmtWhen } from '../../lib/time';

const DURATION_CHOICES = [1, 2, 4, 8, 24, 72, 168, 720];

const durKey = (h: number | null): string => (h === null ? 'perm' : String(h));

/** Durations an admin can pick: the requested one and everything shorter (permanent requests may be narrowed to any). */
function durationOptions(requested: number | null): { value: string; label: string }[] {
  const asked = fmtHours(requested);
  const out: { value: string; label: string }[] = [];
  for (const h of DURATION_CHOICES) {
    if (requested !== null && h >= requested) continue;
    out.push({ value: String(h), label: `${fmtHours(h)} (narrowed from ${asked})` });
  }
  out.push({ value: durKey(requested), label: `${asked} (as requested)` });
  return out;
}

interface Ctx {
  users: UserView[] | undefined;
  clusters: ClusterView[] | undefined;
  overrides: EnvironmentOverride[] | undefined;
}

function displayName(r: AccessRequest, users: UserView[] | undefined): string {
  return users?.find((u) => u.id === r.userId)?.displayName || r.username;
}

function ScopeLine({ scope, env }: { scope: ScopeView; env: string | null }) {
  const names =
    scope.kind === 'workloads' ? (scope.workloads ?? []).map((w) => w.name) : [scopeName(scope)];
  const where =
    scope.kind === 'argo_project'
      ? 'all clusters'
      : scope.kind === 'namespace'
        ? `on ${scope.cluster}`
        : `in namespace ${scope.namespace} · ${scope.cluster}`;
  return (
    <>
      <span className="tag">{SCOPE_KIND_LABEL[scope.kind]}</span>
      {names.map((n) => (
        <span key={n} className="mono">{n}</span>
      ))}
      <span className="muted">{where}</span>
      {env && <EnvBadge env={env} />}
    </>
  );
}

function RequestItem({
  req, ctx, on, now, onPick,
}: { req: AccessRequest; ctx: Ctx; on: boolean; now: number; onPick: () => void }) {
  const env = scopeEnvironment(req.scope, ctx.clusters, ctx.overrides);
  const name = displayName(req, ctx.users);
  const when = fmtWhen(req.status === 'pending' ? req.createdAt : (req.decidedAt ?? req.createdAt), now);
  return (
    <button type="button" className={`rq-item${on ? ' on' : ''}`} onClick={onPick} aria-current={on ? 'true' : undefined}>
      <span className="row">
        <Avatar name={name} size={24} />
        <b>{name}</b>
        <span className="grow" />
        {req.status !== 'pending' && <StatusChip status={req.status} />}
        <span className="muted">{when}</span>
      </span>
      <span className="row wrap" style={{ marginTop: 6, gap: 6 }}>
        <ActionChips actions={req.actions} />
        <span className="mono">{scopeName(req.scope)}</span>
        {env && <EnvBadge env={env} />}
      </span>
      <div className="quote">
        {fmtHours(req.durationHours)} · “{req.reason}”
      </div>
    </button>
  );
}

function StatusChip({ status }: { status: AccessRequest['status'] }) {
  const tone = status === 'approved' ? 'ok' : status === 'rejected' ? 'err' : status === 'pending' ? 'warn' : '';
  const label = status.charAt(0).toUpperCase() + status.slice(1);
  return <span className={`chip ${tone}`}>{label}</span>;
}

/** The requester's existing grants: direct ones and those through their teams (expired ones are left out). */
function CurrentAccess({ req, user, ready }: { req: AccessRequest; user: UserView | undefined; ready: boolean }) {
  const teamKey = (user?.teams ?? []).map((t) => t.id).join(',');
  const { data, error, loading, reload } = useApi<GrantView[]>(
    async () => {
      const lists = await Promise.all([
        api.grants({ holderKind: 'user', holderId: req.userId }),
        ...(user?.teams ?? []).map((t) => api.grants({ holderKind: 'team', holderId: t.id })),
      ]);
      return lists.flat().filter((g) => !g.expired);
    },
    [req.userId, teamKey],
    { enabled: ready },
  );
  const first = req.username.split(/[\s._-]+/)[0];
  const who = first ? `${first.charAt(0).toUpperCase()}${first.slice(1)}` : 'User';
  return (
    <div className="col" style={{ gap: 6 }}>
      <span className="lbl">{who}’s current access</span>
      {error ? (
        <ErrorBanner error={error} onRetry={reload} />
      ) : loading || !ready ? (
        <div className="card"><Skeleton rows={2} /></div>
      ) : !data || data.length === 0 ? (
        <div className="muted">No grants yet.</div>
      ) : (
        <div className="card">
          <div className="tbl-wrap">
          <table className="tbl">
            <tbody>
              {data.map((g) => (
                <tr key={g.id}>
                  <td><ScopeCell scope={g.scope} /></td>
                  <td className="mono">{scopeCluster(g.scope)}</td>
                  <td><ActionChips actions={g.actions} /></td>
                  <td className="muted">{g.holder.kind === 'team' ? `via team ${g.holder.name}` : 'Direct'}</td>
                </tr>
              ))}
            </tbody>
          </table>
          </div>
        </div>
      )}
    </div>
  );
}

function RequestSummary({ req, ctx, now }: { req: AccessRequest; ctx: Ctx; now: number }) {
  const user = ctx.users?.find((u) => u.id === req.userId);
  const name = displayName(req, ctx.users);
  const env = scopeEnvironment(req.scope, ctx.clusters, ctx.overrides);
  const teams = (user?.teams ?? []).map((t) => `Team ${t.name}`).join(', ');
  return (
    <div className="card">
      <div className="hd">
        <Avatar name={name} size={28} />
        <div>
          <div>{name}</div>
          <small>
            {[req.username, teams, `requested ${fmtWhen(req.createdAt, now).toLowerCase()}`].filter(Boolean).join(' · ')}
          </small>
        </div>
        <span className="grow" />
        <StatusChip status={req.status} />
      </div>
      <div className="bd col" style={{ gap: 14 }}>
        <dl className="kv" style={{ gridTemplateColumns: '90px 1fr' }}>
          <dt>Scope</dt>
          <dd><ScopeLine scope={req.scope} env={env} /></dd>
          <dt>Actions</dt>
          <dd><ActionChips actions={req.actions} /></dd>
          <dt>Duration</dt>
          <dd>{fmtHours(req.durationHours)}</dd>
          <dt>Reason</dt>
          <dd style={{ display: 'block', lineHeight: 1.5, whiteSpace: 'pre-wrap' }}>{req.reason}</dd>
        </dl>
        <CurrentAccess req={req} user={user} ready={ctx.users !== undefined} />
      </div>
    </div>
  );
}

function DecidedCard({ req }: { req: AccessRequest }) {
  return (
    <div className="card">
      <div className="hd">Decision</div>
      <div className="bd">
        <dl className="kv" style={{ gridTemplateColumns: '90px 1fr' }}>
          <dt>Outcome</dt>
          <dd><StatusChip status={req.status} /></dd>
          {req.decidedBy && (<><dt>By</dt><dd>{req.decidedBy}</dd></>)}
          {req.decidedAt && (<><dt>When</dt><dd>{fmtDateTime(req.decidedAt)}</dd></>)}
          {req.status === 'expired' && (<><dt>Note</dt><dd>Not decided within 7 days.</dd></>)}
          {req.decisionNote && (<><dt>Note</dt><dd style={{ display: 'block', whiteSpace: 'pre-wrap' }}>{req.decisionNote}</dd></>)}
          {req.resultingGrantId !== null && (
            <>
              <dt>Grant</dt>
              <dd><Link to="/grants">Grant #{req.resultingGrantId}</Link></dd>
            </>
          )}
        </dl>
      </div>
    </div>
  );
}

type ApproveBody = Parameters<typeof api.approveRequest>[1];

const fmtExpiry = (iso: string | null) => (iso === null ? 'permanent' : `until ${fmtDateTime(iso)}`);

/**
 * What approving does with the requester's grants: add to their own grant on
 * the same scope when it ends no later than the approved access (that grant
 * then takes the new expiry), else create a new one.
 */
function GrantOutcome({ grants, target, actions, newEnd, separate, onSeparate, scope }: {
  grants: GrantView[]; target: GrantView | null; actions: Action[]; newEnd: number | null; separate: boolean; onSeparate: (v: boolean) => void; scope: ScopeView;
}) {
  const longer = grants.find((g) => g.holder.kind === 'user' && !g.expired && sameScope(g.scope, scope));
  const added = actions.filter((a) => !target?.actions.includes(a));
  return (
    <div className="f" role="group" aria-label="Resulting grant">
      Grant
      {target && !separate ? (
        <div className="row wrap" style={{ gap: 6 }}>
          Adds to <b>grant #{target.id}</b>
          <ActionChips actions={target.actions} />
          {added.length > 0 && <>+ <ActionChips actions={added} /></>}
          <span className="muted">· {fmtExpiry(target.expiresAt)} → {fmtExpiry(newEnd === null ? null : new Date(newEnd).toISOString())}</span>
        </div>
      ) : (
        <div className="muted">
          Creates a new grant{!target && longer ? ` (grant #${longer.id} on this scope lasts longer than this access, so it stays as it is)` : ''}.
        </div>
      )}
      {target && (
        <label className="chk">
          <input type="checkbox" checked={separate} onChange={(e) => onSeparate(e.target.checked)} />
          Create a separate grant instead
        </label>
      )}
    </div>
  );
}

function DecisionCard({
  req, mobile, onDone,
}: { req: AccessRequest; mobile: boolean; onDone: () => void }) {
  const toast = useToast();
  const [actions, setActions] = useState<Action[]>(req.actions);
  const [dur, setDur] = useState(durKey(req.durationHours));
  const [note, setNote] = useState('');
  const [busy, setBusy] = useState<'approve' | 'reject' | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [noteErr, setNoteErr] = useState<string | undefined>();
  const [narrow, setNarrow] = useState(false);
  const [separate, setSeparate] = useState(false);
  const durations = useMemo(() => durationOptions(req.durationHours), [req.durationHours]);
  const now = useNow(60_000);
  const own = useApi(() => api.grants({ holderKind: 'user', holderId: req.userId }), [req.userId]);
  const newEnd = dur === 'perm' ? null : now + Number(dur) * 3600_000;
  const target = own.data ? mergeTarget(own.data, req.scope, newEnd, now) : null;

  const actionsChanged = actions.length !== req.actions.length || actions.some((a) => !req.actions.includes(a));
  const durChanged = dur !== durKey(req.durationHours);
  const changed = actionsChanged || durChanged;

  // Any action may be granted, requested or not; sync never fits a workloads scope.
  const toggle = (a: Action) =>
    setActions((cur) => ACTIONS.filter((x) => (x === a ? !cur.includes(a) : cur.includes(x))));

  const fail = (e: unknown) => {
    setError(e);
    if (e instanceof ApiError && e.fields.note) setNoteErr(e.fields.note);
  };

  const approve = async () => {
    setBusy('approve');
    setError(null);
    setNoteErr(undefined);
    const body: ApproveBody = {};
    if (note.trim()) body.note = note.trim();
    if (durChanged) body.durationHours = dur === 'perm' ? null : Number(dur);
    if (actionsChanged) body.actions = actions;
    if (target && separate) body.separate = true;
    try {
      const res = await api.approveRequest(req.id, body);
      if (actionsChanged && (res.actions.length !== actions.length || res.actions.some((a) => !actions.includes(a)))) {
        toast('Approved, but the server granted other actions than you selected. Review the grant under Grants.', 'err');
      } else if (target && res.resultingGrantId === target.id) {
        toast(`Approved ${req.username}’s request: added to grant #${target.id}.`);
      } else {
        toast(`Approved ${req.username}’s request${changed ? ' with changes' : ''}.`);
      }
      onDone();
    } catch (e) {
      fail(e);
    } finally {
      setBusy(null);
    }
  };

  const reject = async () => {
    if (!note.trim()) {
      setNoteErr('A note is required when rejecting.');
      return;
    }
    setBusy('reject');
    setError(null);
    setNoteErr(undefined);
    try {
      await api.rejectRequest(req.id, { note: note.trim() });
      toast(`Rejected ${req.username}’s request.`);
      onDone();
    } catch (e) {
      fail(e);
    } finally {
      setBusy(null);
    }
  };

  const outcome = own.data && (
    <GrantOutcome grants={own.data} target={target} actions={actions} newEnd={newEnd} separate={separate} onSeparate={setSeparate} scope={req.scope} />
  );

  const narrowFields = (
    <div className="row wrap" style={{ gap: 24, alignItems: 'flex-end' }}>
      <div className="f" role="group" aria-label="Actions to grant">
        Actions
        <div className="row wrap" style={{ gap: 14 }}>
          {ACTIONS.map((a) => {
            const off = a === 'sync' && req.scope.kind === 'workloads' && !actions.includes(a);
            return (
              <label key={a} className="chk" style={{ minHeight: mobile ? 44 : undefined }} title={off ? SYNC_SCOPE_HINT : undefined}>
                <input type="checkbox" checked={actions.includes(a)} disabled={off} onChange={() => toggle(a)} />
                {actionOptionLabel(a)}
                {!req.actions.includes(a) && actions.includes(a) && <span className="chip info">added</span>}
              </label>
            );
          })}
        </div>
      </div>
      <label className="f">
        Duration
        <select className="sel" style={{ height: mobile ? 44 : 30 }} value={dur} onChange={(e) => setDur(e.target.value)}>
          {durations.map((d) => (
            <option key={d.value} value={d.value}>{d.label}</option>
          ))}
        </select>
      </label>
    </div>
  );

  const noMoreActions = actions.length === 0;
  const approveLabel = changed ? 'Approve with changes' : 'Approve';
  const buttons = (
    <>
      <button type="button" className="btn" disabled={busy !== null} onClick={reject}>
        {busy === 'reject' ? 'Rejecting…' : 'Reject'}
      </button>
      <button type="button" className="btn pri" disabled={busy !== null || noMoreActions} onClick={approve}>
        <Icon name="check" />
        {busy === 'approve' ? 'Approving…' : mobile ? 'Approve' : approveLabel}
      </button>
    </>
  );

  if (mobile) {
    return (
      <>
        <button
          type="button"
          className="row linklike blue"
          style={{ minHeight: 44 }}
          aria-expanded={narrow}
          onClick={() => setNarrow((v) => !v)}
        >
          Change actions or duration{changed ? ' (changed)' : ''}
          <Icon name={narrow ? 'chevd' : 'chev'} />
        </button>
        {narrow && <div className="card"><div className="bd col">{narrowFields}</div></div>}
        {outcome && <div className="card"><div className="bd col">{outcome}</div></div>}
        {error !== null && <ErrorBanner error={error} />}
        <div className="rq-bar">
          <label className="f">
            <span>Note <span className="hint">Required to reject</span></span>
            <input
              className="input"
              value={note}
              aria-invalid={noteErr ? true : undefined}
              onChange={(e) => { setNote(e.target.value); setNoteErr(undefined); }}
            />
            <FieldError msg={noteErr} />
          </label>
          <div className="row" style={{ gap: 10 }}>{buttons}</div>
        </div>
      </>
    );
  }

  return (
    <div className="card">
      <div className="hd">Decision <small>Change the actions or shorten the duration before approving if needed</small></div>
      <div className="bd col" style={{ gap: 14 }}>
        {narrowFields}
        {outcome}
        <label className="f">
          <span>Note to requester <span className="hint">Required when rejecting</span></span>
          <textarea
            className="input"
            rows={2}
            value={note}
            aria-invalid={noteErr ? true : undefined}
            onChange={(e) => { setNote(e.target.value); setNoteErr(undefined); }}
          />
          <FieldError msg={noteErr} />
        </label>
        {error !== null && <ErrorBanner error={error} />}
        <div className="row" style={{ justifyContent: 'flex-end' }}>{buttons}</div>
      </div>
    </div>
  );
}

export function Requests() {
  const { isAdmin } = useAuth();
  const mobile = useMedia('(max-width:640px)');
  const now = useNow(60_000);
  const reqs = useApi(() => api.accessRequests(), []);
  const users = useApi(() => api.users(), []);
  const clusters = useApi(() => api.clusters(), []);
  const overrides = useApi(() => api.environmentOverrides(), []);
  const [tab, setTab] = useState<'pending' | 'decided'>('pending');
  const [selId, setSelId] = useState<number | null>(null);
  const [detailOpen, setDetailOpen] = useState(false);

  const ctx: Ctx = { users: users.data, clusters: clusters.data, overrides: overrides.data };

  const { pending, decided } = useMemo(() => {
    const all = reqs.data ?? [];
    const t = (r: AccessRequest) => Date.parse(r.createdAt);
    const d = (r: AccessRequest) => Date.parse(r.decidedAt ?? r.createdAt);
    return {
      pending: all.filter((r) => r.status === 'pending').sort((a, b) => t(b) - t(a)),
      decided: all.filter((r) => r.status !== 'pending').sort((a, b) => d(b) - d(a)),
    };
  }, [reqs.data]);

  const list = tab === 'pending' ? pending : decided;
  const selected = list.find((r) => r.id === selId) ?? (mobile ? undefined : list[0]);

  // Leave the mobile detail view when its request disappears from the list (decided, or tab changed).
  useEffect(() => {
    if (detailOpen && !selected) setDetailOpen(false);
  }, [detailOpen, selected]);

  const pick = (id: number) => {
    setSelId(id);
    setDetailOpen(true);
  };
  const done = () => {
    setDetailOpen(false);
    setSelId(null);
    reqs.reload();
  };

  const showDetail = !mobile || (detailOpen && selected !== undefined);
  const showList = !mobile || !showDetail;

  const header = (
    <PageHeader
      title="Access requests"
      subtitle={`${pending.length} pending · newest first`}
    >
      {!isAdmin && <ReadOnlyNote />}
    </PageHeader>
  );

  if (reqs.error) return <>{header}<ErrorBanner error={reqs.error} onRetry={reqs.reload} /></>;
  if (reqs.loading && !reqs.data) return <>{header}<Loading what="requests" /></>;

  if (mobile && showDetail && selected) {
    return (
      <>
        <div className="rq-back">
          <button type="button" className="btn ghost" aria-label="Back to requests" onClick={() => setDetailOpen(false)}>
            <Icon name="chev" style={{ transform: 'rotate(180deg)' }} />
          </button>
          <b style={{ fontSize: 15 }}>Access request</b>
          <span className="grow" />
          <StatusChip status={selected.status} />
        </div>
        <RequestSummary req={selected} ctx={ctx} now={now} />
        {selected.status === 'pending' && isAdmin ? (
          <DecisionCard key={selected.id} req={selected} mobile onDone={done} />
        ) : selected.status !== 'pending' ? (
          <DecidedCard req={selected} />
        ) : null}
      </>
    );
  }

  return (
    <>
      {header}
      <BreakGlassRequests />
      <div className="row" style={{ alignItems: 'flex-start', gap: 16, flexWrap: mobile ? 'wrap' : 'nowrap' }}>
        {showList && (
          <div className="card" style={{ width: mobile ? '100%' : 430, flex: 'none', maxWidth: '100%' }}>
            <div className="hd">
              <Seg
                label="Request status"
                value={tab}
                onChange={(v) => { setTab(v); setSelId(null); }}
                options={[
                  { value: 'pending', label: `Pending ${pending.length}` },
                  { value: 'decided', label: 'Decided' },
                ]}
              />
            </div>
            {list.length === 0 ? (
              <Empty>{tab === 'pending' ? 'No pending requests.' : 'No decided requests yet.'}</Empty>
            ) : (
              <div role="list">
                {list.map((r) => (
                  <div key={r.id} role="listitem">
                    <RequestItem req={r} ctx={ctx} now={now} on={!mobile && selected?.id === r.id} onPick={() => pick(r.id)} />
                  </div>
                ))}
              </div>
            )}
          </div>
        )}
        {!mobile && (
          <div className="col" style={{ flex: 1, minWidth: 0, gap: 16 }}>
            {selected ? (
              <>
                <RequestSummary req={selected} ctx={ctx} now={now} />
                {selected.status === 'pending' ? (
                  isAdmin && <DecisionCard key={selected.id} req={selected} mobile={false} onDone={done} />
                ) : (
                  <DecidedCard req={selected} />
                )}
              </>
            ) : (
              <div className="card"><Empty>Select a request to review.</Empty></div>
            )}
          </div>
        )}
      </div>
    </>
  );
}
