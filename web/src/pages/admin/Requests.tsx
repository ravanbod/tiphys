import { useEffect, useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { ApiError, api } from '../../api';
import type {
  AccessRequest, Action, ClusterView, EnvironmentOverride, GrantView, ScopeView, UserView,
} from '../../api';
import { ActionChips, Avatar, EnvBadge, isProd } from '../../components/Badges';
import { Empty, ErrorBanner, Loading, Skeleton } from '../../components/Feedback';
import { Icon } from '../../components/Icon';
import { PageHeader } from '../../components/PageHeader';
import { SCOPE_KIND_LABEL, ScopeCell, scopeCluster, scopeName } from '../../components/Scope';
import { Seg } from '../../components/Seg';
import { Select } from '../../components/Select';
import { FieldError, ReadOnlyNote } from '../../components/AdminBits';
import '../../components/adminPages.css';
import { useAuth } from '../../context/auth';
import { useToast } from '../../context/toast';
import { useApi } from '../../hooks/useApi';
import { useLocalStorage } from '../../hooks/useLocalStorage';
import { useMedia } from '../../hooks/useMedia';
import { useNow } from '../../hooks/useNow';
import { scopeEnvironment } from '../../lib/adminEnv';
import { BreakGlassRequests } from './BreakGlassRequests';
import { ACTIONS, APP_SCOPE_HINT, actionOptionLabel, isAppAction } from '../../lib/grantForm';
import { mergeTarget, sameScope } from '../../lib/requestMerge';
import {
  NO_REQUEST_FILTERS, activeRequestFilters, cleanRequestFilters, matchesRequest, requestFiltersKey,
} from '../../lib/requestFilters';
import type { RequestFilters } from '../../lib/requestFilters';
import { groupKey, groupPending, groupRequests, groupStatus, statusSummary } from '../../lib/requestBatches';
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

/** The environments of a group's scopes, each once. */
function groupEnvs(g: AccessRequest[], ctx: Ctx): string[] {
  return [...new Set(g.map((r) => scopeEnvironment(r.scope, ctx.clusters, ctx.overrides)).filter((e): e is string => !!e))];
}

function RequestItem({
  g, ctx, on, now, onPick,
}: { g: AccessRequest[]; ctx: Ctx; on: boolean; now: number; onPick: () => void }) {
  const req = g[0];
  const envs = groupEnvs(g, ctx);
  const name = displayName(req, ctx.users);
  const pending = groupPending(g);
  const when = fmtWhen(pending ? req.createdAt : (req.decidedAt ?? req.createdAt), now);
  return (
    <button type="button" className={`rq-item${on ? ' on' : ''}`} onClick={onPick} aria-current={on ? 'true' : undefined}>
      <span className="row">
        <Avatar name={name} size={24} />
        <b>{name}</b>
        {req.team && <span className="chip info">for team {req.team.name}</span>}
        <span className="grow" />
        {!pending && <StatusChip status={groupStatus(g)} label={g.length > 1 ? statusSummary(g) : undefined} />}
        <span className="muted">{when}</span>
      </span>
      <span className="row wrap" style={{ marginTop: 6, gap: 6 }}>
        <ActionChips actions={req.actions} />
        {g.length > 1 ? (
          <>
            <span className="chip">{g.length} namespaces</span>
            <span className="mono">{g.slice(0, 2).map((r) => r.scope.namespace).join(', ')}{g.length > 2 ? ', …' : ''}</span>
          </>
        ) : <span className="mono">{scopeName(req.scope)}</span>}
        {envs.map((e) => <EnvBadge key={e} env={e} />)}
      </span>
      <div className="quote">
        {fmtHours(req.durationHours)} · “{req.reason}”
      </div>
    </button>
  );
}

function StatusChip({ status, label }: { status: AccessRequest['status']; label?: string }) {
  const tone = status === 'approved' ? 'ok' : status === 'rejected' ? 'err' : status === 'pending' ? 'warn' : '';
  return <span className={`chip ${tone}`}>{label ?? status.charAt(0).toUpperCase() + status.slice(1)}</span>;
}

/**
 * The requester's existing grants: direct ones and those through their teams
 * (expired ones are left out); for a team's request, the team's grants.
 */
function CurrentAccess({ req, user, ready }: { req: AccessRequest; user: UserView | undefined; ready: boolean }) {
  const teamKey = (user?.teams ?? []).map((t) => t.id).join(',');
  const { data, error, loading, reload } = useApi<GrantView[]>(
    async () => {
      const lists = await Promise.all(req.team
        ? [api.grants({ holderKind: 'team', holderId: req.team.id })]
        : [
          api.grants({ holderKind: 'user', holderId: req.userId }),
          ...(user?.teams ?? []).map((t) => api.grants({ holderKind: 'team', holderId: t.id })),
        ]);
      return lists.flat().filter((g) => !g.expired);
    },
    [req.userId, req.team?.id, teamKey],
    { enabled: ready },
  );
  const first = req.username.split(/[\s._-]+/)[0];
  const who = req.team ? `Team ${req.team.name}` : first ? `${first.charAt(0).toUpperCase()}${first.slice(1)}` : 'User';
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

function RequestSummary({ g, ctx, now }: { g: AccessRequest[]; ctx: Ctx; now: number }) {
  const req = g[0];
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
        <StatusChip status={groupStatus(g)} label={g.length > 1 ? statusSummary(g) : undefined} />
      </div>
      <div className="bd col" style={{ gap: 14 }}>
        <dl className="kv" style={{ gridTemplateColumns: '90px 1fr' }}>
          {req.team && (
            <>
              <dt>For</dt>
              <dd>
                <Link to={`/teams`}>Team {req.team.name}</Link>
                <span className="muted"> · asked by its manager; approval grants the team, so every member gets it</span>
              </dd>
            </>
          )}
          <dt>Scope</dt>
          <dd>
            {g.length > 1 ? (
              <>
                <span className="tag">{g.length} namespaces</span>
                <span className="muted">on {req.scope.cluster}, filed together and decided together</span>
                {groupEnvs(g, ctx).map((e) => <EnvBadge key={e} env={e} />)}
              </>
            ) : <ScopeLine scope={req.scope} env={env} />}
          </dd>
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

function DecidedCard({ g }: { g: AccessRequest[] }) {
  const req = g[0];
  const grantsLink = `/grants?holder=${req.team ? `team:${req.team.id}` : `user:${req.userId}`}`;
  return (
    <div className="card">
      <div className="hd">Decision</div>
      <div className="bd col" style={{ gap: 12 }}>
        <dl className="kv" style={{ gridTemplateColumns: '90px 1fr' }}>
          <dt>Outcome</dt>
          <dd><StatusChip status={groupStatus(g)} label={g.length > 1 ? statusSummary(g) : undefined} /></dd>
          {req.decidedBy && (<><dt>By</dt><dd>{req.decidedBy}</dd></>)}
          {req.decidedAt && (<><dt>When</dt><dd>{fmtDateTime(req.decidedAt)}</dd></>)}
          {req.status === 'expired' && (<><dt>Note</dt><dd>Not decided within 7 days.</dd></>)}
          {req.decisionNote && (<><dt>Note</dt><dd style={{ display: 'block', whiteSpace: 'pre-wrap' }}>{req.decisionNote}</dd></>)}
          {g.length === 1 && req.resultingGrantId !== null && (
            <>
              <dt>Grant</dt>
              <dd><Link to={grantsLink}>Grant #{req.resultingGrantId}</Link></dd>
            </>
          )}
        </dl>
        {g.length > 1 && (
          <div className="pickbox" role="list" aria-label="Namespaces">
            {g.map((r) => (
              <div key={r.id} role="listitem" className="row" style={{ padding: '6px 10px', gap: 8 }}>
                <span className="mono">{r.scope.namespace}</span>
                <span className="grow" />
                {r.resultingGrantId !== null && <Link to={grantsLink}>Grant #{r.resultingGrantId}</Link>}
                <StatusChip status={r.status} />
              </div>
            ))}
          </div>
        )}
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
function GrantOutcome({ grants, target, actions, newEnd, separate, onSeparate, scope, team }: {
  grants: GrantView[]; target: GrantView | null; actions: Action[]; newEnd: number | null; separate: boolean; onSeparate: (v: boolean) => void; scope: ScopeView;
  team: AccessRequest['team'];
}) {
  const longer = grants.find((g) => g.holder.kind === (team ? 'team' : 'user') && !g.expired && sameScope(g.scope, scope));
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
          Creates a new grant{team ? ` for team ${team.name}` : ''}{!target && longer ? ` (grant #${longer.id} on this scope lasts longer than this access, so it stays as it is)` : ''}.
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

/**
 * Approve or reject a request, or a batch: its namespaces are listed with
 * what approving each does, and unticked ones are rejected in the same
 * decision (all or nothing on the server).
 */
function DecisionCard({
  g, ctx, mobile, onDone,
}: { g: AccessRequest[]; ctx: Ctx; mobile: boolean; onDone: () => void }) {
  const req = g[0];
  const batch = g.length > 1 && req.batchId !== null;
  const [chosen, setChosen] = useState<Set<number>>(() => new Set(g.map((r) => r.id)));
  const rejecting = g.length - chosen.size;
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
  // The holder approval grants: the requester, or the team for a team's request.
  const own = useApi(
    () => (req.team ? api.grants({ holderKind: 'team', holderId: req.team.id }) : api.grants({ holderKind: 'user', holderId: req.userId })),
    [req.userId, req.team?.id],
  );
  const newEnd = dur === 'perm' ? null : now + Number(dur) * 3600_000;
  const targetOf = (scope: ScopeView) => (own.data ? mergeTarget(own.data, scope, newEnd, now, req.team ? 'team' : 'user') : null);
  const target = batch ? null : targetOf(req.scope);
  const anyTarget = batch && g.some((r) => targetOf(r.scope));
  const prodChosen = g.filter((r) => chosen.has(r.id) && isProd(scopeEnvironment(r.scope, ctx.clusters, ctx.overrides))).length;
  const toggleNs = (id: number) => setChosen((cur) => {
    const n = new Set(cur);
    if (n.has(id)) n.delete(id);
    else n.add(id);
    return n;
  });
  const whose = req.team ? `team ${req.team.name}’s request (by ${req.username})` : `${req.username}’s request`;

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
    if (batch && rejecting > 0 && !note.trim()) {
      setNoteErr('A note is required when rejecting.');
      return;
    }
    setBusy('approve');
    setError(null);
    setNoteErr(undefined);
    const body: ApproveBody = {};
    if (note.trim()) body.note = note.trim();
    if (durChanged) body.durationHours = dur === 'perm' ? null : Number(dur);
    if (actionsChanged) body.actions = actions;
    if ((target || anyTarget) && separate) body.separate = true;
    try {
      if (batch) {
        await api.decideRequestBatch(req.batchId!, { approve: [...chosen], ...body });
        toast(rejecting ? `Approved ${chosen.size} and rejected ${rejecting} of ${whose}.` : `Approved all ${g.length} of ${whose}.`);
        onDone();
        return;
      }
      const res = await api.approveRequest(req.id, body);
      if (actionsChanged && (res.actions.length !== actions.length || res.actions.some((a) => !actions.includes(a)))) {
        toast('Approved, but the server granted other actions than you selected. Review the grant under Grants.', 'err');
      } else if (target && res.resultingGrantId === target.id) {
        toast(`Approved ${whose}: added to grant #${target.id}.`);
      } else {
        toast(`Approved ${whose}${changed ? ' with changes' : ''}.`);
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
      if (batch) await api.decideRequestBatch(req.batchId!, { approve: [], note: note.trim() });
      else await api.rejectRequest(req.id, { note: note.trim() });
      toast(`Rejected ${whose}.`);
      onDone();
    } catch (e) {
      fail(e);
    } finally {
      setBusy(null);
    }
  };

  const outcome = batch ? (
    <div className="f" role="group" aria-label="Namespaces to approve">
      <span>Namespaces <span className="hint">Untick to reject; the rest are approved</span></span>
      <div className="pickbox">
        {g.map((r) => {
          const t = targetOf(r.scope);
          const env = scopeEnvironment(r.scope, ctx.clusters, ctx.overrides);
          const on = chosen.has(r.id);
          return (
            <label key={r.id} className="chk" style={{ minHeight: mobile ? 44 : undefined }}>
              <input type="checkbox" checked={on} onChange={() => toggleNs(r.id)} />
              <span className="mono">{r.scope.namespace}</span>
              {env && <EnvBadge env={env} />}
              <span className="grow" />
              <span className="muted">{!on ? 'rejected' : !own.data ? '' : t && !separate ? `adds to grant #${t.id}` : 'new grant'}</span>
            </label>
          );
        })}
      </div>
      {anyTarget && (
        <label className="chk">
          <input type="checkbox" checked={separate} onChange={(e) => setSeparate(e.target.checked)} />
          Create separate grants instead of adding to existing ones
        </label>
      )}
    </div>
  ) : own.data && (
    <GrantOutcome grants={own.data} target={target} actions={actions} newEnd={newEnd} separate={separate} onSeparate={setSeparate} scope={req.scope} team={req.team} />
  );

  const narrowFields = (
    <div className="row wrap" style={{ gap: 24, alignItems: 'flex-end' }}>
      <div className="f" role="group" aria-label="Actions to grant">
        Actions
        <div className="row wrap" style={{ gap: 14 }}>
          {ACTIONS.map((a) => {
            const off = isAppAction(a) && req.scope.kind === 'workloads' && !actions.includes(a);
            return (
              <label key={a} className="chk" style={{ minHeight: mobile ? 44 : undefined }} title={off ? APP_SCOPE_HINT : undefined}>
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
        <Select label="Duration" style={{ height: mobile ? 44 : 30 }} value={dur} onChange={setDur} options={durations} />
      </label>
    </div>
  );

  const noMoreActions = actions.length === 0 || (batch && chosen.size === 0);
  const approveLabel = batch
    ? (rejecting ? `Approve ${chosen.size}, reject ${rejecting}` : `Approve all ${g.length}`) + (changed ? ' with changes' : '')
    : changed ? 'Approve with changes' : 'Approve';
  const buttons = (
    <>
      <button type="button" className="btn" disabled={busy !== null} onClick={reject}>
        {busy === 'reject' ? 'Rejecting…' : batch ? 'Reject all' : 'Reject'}
      </button>
      <button type="button" className="btn pri" disabled={busy !== null || noMoreActions} onClick={approve}>
        <Icon name="check" />
        {busy === 'approve' ? 'Approving…' : mobile && !batch ? 'Approve' : approveLabel}
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
          {batch && prodChosen > 0 && <span className="muted">{prodChosen} of the approved {prodChosen === 1 ? 'is' : 'are'} in prod</span>}
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
          <span>Note to requester <span className="hint">{batch ? 'Required when rejecting any' : 'Required when rejecting'}</span></span>
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
        <div className="row" style={{ justifyContent: 'flex-end' }}>
          {batch && prodChosen > 0 && <span className="muted">{prodChosen} of the approved {prodChosen === 1 ? 'is' : 'are'} in prod</span>}
          {buttons}
        </div>
      </div>
    </div>
  );
}

const uniq = (xs: (string | null | undefined)[]) => [...new Set(xs.filter((x): x is string => !!x))].sort();

/** Search and pickers above the request list; `shown`/`total` are for the open tab. */
function RequestFilterBar({ f, set, clusters, envs, shown, total }: {
  f: RequestFilters;
  set: (patch: Partial<RequestFilters>) => void;
  clusters: string[];
  envs: string[];
  shown: number;
  total: number;
}) {
  return (
    <div className="rq-filters">
      <label className="search">
        <Icon name="search" />
        <input value={f.q} onChange={(e) => set({ q: e.target.value })} placeholder="Search people, teams, namespaces, reasons" aria-label="Search requests" />
      </label>
      <div className="rq-pickers">
        <Select label="Cluster" value={f.cluster} onChange={(v) => set({ cluster: v })} options={[{ value: '', label: 'Any cluster' }, ...clusters.map((c) => ({ value: c, label: c }))]} />
        <Select label="Environment" value={f.env} onChange={(v) => set({ env: v })} options={[{ value: '', label: 'Any env' }, ...envs.map((e) => ({ value: e, label: e }))]} />
        <Select label="Action" value={f.action} onChange={(v) => set({ action: v })} options={[{ value: '', label: 'Any action' }, ...ACTIONS.map((a) => ({ value: a, label: a }))]} />
        <Select<RequestFilters['for']> label="Requested for" value={f.for} onChange={(v) => set({ for: v })}
          options={[{ value: '', label: 'Anyone' }, { value: 'user', label: 'People' }, { value: 'team', label: 'Teams' }]} />
      </div>
      {activeRequestFilters(f) > 0 && (
        <div className="row muted" aria-live="polite">
          Showing {shown} of {total}
          <span className="grow" />
          <button type="button" className="linklike" onClick={() => set(NO_REQUEST_FILTERS)}>Clear filters</button>
        </div>
      )}
    </div>
  );
}

export function Requests() {
  const { isAdmin, me } = useAuth();
  const mobile = useMedia('(max-width:640px)');
  const now = useNow(60_000);
  const reqs = useApi(() => api.accessRequests(), []);
  const users = useApi(() => api.users(), []);
  const clusters = useApi(() => api.clusters(), []);
  const overrides = useApi(() => api.environmentOverrides(), []);
  const [tab, setTab] = useState<'pending' | 'decided'>('pending');
  const [selId, setSelId] = useState<string | null>(null);
  const [detailOpen, setDetailOpen] = useState(false);
  // Remembered per admin across visits and tabs; the tab itself always opens on Pending.
  const [stored, setStored] = useLocalStorage<unknown>(requestFiltersKey(me.id), null);
  const filters = cleanRequestFilters(stored);
  const setFilters = (patch: Partial<RequestFilters>) => {
    setStored({ ...filters, ...patch });
    setSelId(null);
  };

  const ctx: Ctx = { users: users.data, clusters: clusters.data, overrides: overrides.data };

  // A batch is one entry, decided as a whole.
  const { pending, decided } = useMemo(() => {
    const all = groupRequests(reqs.data ?? []);
    const t = (g: AccessRequest[]) => Date.parse(g[0].createdAt);
    const d = (g: AccessRequest[]) => Date.parse(g[0].decidedAt ?? g[0].createdAt);
    return {
      pending: all.filter(groupPending).sort((a, b) => t(b) - t(a)),
      decided: all.filter((g) => !groupPending(g)).sort((a, b) => d(b) - d(a)),
    };
  }, [reqs.data]);

  const envOf = (r: AccessRequest) => scopeEnvironment(r.scope, clusters.data, overrides.data);
  const all = tab === 'pending' ? pending : decided;
  const list = all.filter((g) => g.some((r) => matchesRequest(r, filters, envOf(r), displayName(r, users.data))));
  const clusterOptions = uniq([...(clusters.data ?? []).map((c) => c.name), ...(reqs.data ?? []).map((r) => r.scope.cluster), filters.cluster]);
  const envOptions = uniq([...(clusters.data ?? []).map((c) => c.environment), ...(overrides.data ?? []).map((o) => o.environment), ...(reqs.data ?? []).map(envOf), filters.env]);
  const selected = list.find((g) => groupKey(g) === selId) ?? (mobile ? undefined : list[0]);

  // Leave the mobile detail view when its request disappears from the list (decided, or tab changed).
  useEffect(() => {
    if (detailOpen && !selected) setDetailOpen(false);
  }, [detailOpen, selected]);

  const pick = (id: string) => {
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
          <StatusChip status={groupStatus(selected)} label={selected.length > 1 ? statusSummary(selected) : undefined} />
        </div>
        <RequestSummary g={selected} ctx={ctx} now={now} />
        {groupPending(selected) && isAdmin ? (
          <DecisionCard key={groupKey(selected)} g={selected} ctx={ctx} mobile onDone={done} />
        ) : !groupPending(selected) ? (
          <DecidedCard g={selected} />
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
            <RequestFilterBar f={filters} set={setFilters} clusters={clusterOptions} envs={envOptions} shown={list.length} total={all.length} />
            {list.length === 0 ? (
              <Empty>
                {all.length > 0 ? 'No requests match these filters.' : tab === 'pending' ? 'No pending requests.' : 'No decided requests yet.'}
              </Empty>
            ) : (
              <div role="list">
                {list.map((g) => (
                  <div key={groupKey(g)} role="listitem">
                    <RequestItem g={g} ctx={ctx} now={now} on={!mobile && selected !== undefined && groupKey(selected) === groupKey(g)} onPick={() => pick(groupKey(g))} />
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
                <RequestSummary g={selected} ctx={ctx} now={now} />
                {groupPending(selected) ? (
                  isAdmin && <DecisionCard key={groupKey(selected)} g={selected} ctx={ctx} mobile={false} onDone={done} />
                ) : (
                  <DecidedCard g={selected} />
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
