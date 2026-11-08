import { useState } from 'react';
import { Link } from 'react-router-dom';
import { api } from '../api';
import type { AccessRequest, ManagedTeamView, TeamMember } from '../api';
import { AdminErrorBanner, MiniGrantRow } from '../components/AdminPieces';
import { ActionChips } from '../components/Badges';
import { Empty, ErrorBanner, Loading } from '../components/Feedback';
import { Icon } from '../components/Icon';
import { PageHeader } from '../components/PageHeader';
import { BatchScope, GroupStatus } from '../components/RequestGroup';
import { ScopeCell, scopeCluster } from '../components/Scope';
import { TeamPeopleCard } from '../components/TeamPeople';
import { useApi } from '../hooks/useApi';
import { useNow } from '../hooks/useNow';
import { groupKey, groupRequests } from '../lib/requestBatches';
import { fmtHours, fmtWhen } from '../lib/time';
import { useAdminMutation } from '../lib/useAdminMutation';

/** Teams the user manages: an admin made them a manager, so they add and remove members themselves. */
export function MyTeams() {
  const teams = useApi(() => api.myTeams(), []);
  const candidates = useApi(() => api.myTeamCandidates(), []);
  const [selId, setSelId] = useState<number | null>(null);
  const list = teams.data;
  const selected = list?.find((t) => t.id === selId) ?? list?.[0] ?? null;

  return (
    <>
      <PageHeader title="My teams" subtitle="Teams you manage: the people you add receive the team's grants" />
      {teams.loading && !list && <Loading what="teams" />}
      {!!teams.error && !list && <ErrorBanner error={teams.error} onRetry={teams.reload} />}
      {list && list.length === 0 && <Empty>You do not manage any team. An admin chooses each team&apos;s managers.</Empty>}
      {list && list.length > 1 && (
        <div className="row wrap" style={{ gap: 6, marginTop: 12 }} role="group" aria-label="Team">
          {list.map((t) => (
            <button
              key={t.id}
              type="button"
              className={`btn sm${selected?.id === t.id ? ' pri' : ''}`}
              aria-pressed={selected?.id === t.id}
              onClick={() => setSelId(t.id)}
            >
              {t.name}
            </button>
          ))}
        </div>
      )}
      {selected && (
        <ManagedTeam
          key={selected.id}
          team={selected}
          candidates={candidates.data ?? []}
          candidatesError={!candidates.data && !!candidates.error}
          onChanged={() => teams.reload()}
        />
      )}
    </>
  );
}

function ManagedTeam({
  team, candidates, candidatesError, onChanged,
}: {
  team: ManagedTeamView;
  candidates: TeamMember[];
  candidatesError: boolean;
  onChanged: () => void;
}) {
  const m = useAdminMutation();
  const change = async (fn: () => Promise<unknown>, msg: string) => {
    const r = await m.run(fn, msg);
    if (r.ok) onChanged();
    return r.ok;
  };

  return (
    <div className="col" style={{ gap: 16, marginTop: 12 }}>
      <div className="row wrap">
        <h2 style={{ margin: 0, fontSize: 16 }}>{team.name}</h2>
        <span className="muted">
          {team.members.length} member{team.members.length === 1 ? '' : 's'} · {team.grants.length} grant{team.grants.length === 1 ? '' : 's'}
        </span>
        <span className="grow" />
        <Link to={`/access?request=1&team=${team.id}`} className="btn sm pri">
          <Icon name="plus" />
          Request access for this team
        </Link>
      </div>
      {team.description && <div className="muted" style={{ marginTop: -8 }}>{team.description}</div>}
      <AdminErrorBanner error={m.error} />

      <div className="two" style={{ gap: 16 }}>
        <TeamPeopleCard
          title="Members"
          people={team.members}
          candidates={candidates}
          editable
          busy={m.busy}
          noMatchText="No enabled Keycloak user matches. Pick a name from the list; an admin adds local users."
          candidatesError={candidatesError}
          emptyText="No members yet."
          onAdd={(u) => change(() => api.addMyTeamMember(team.id, u.id), `${u.username} added to ${team.name}`)}
          onRemove={(u) =>
            void change(() => api.removeMyTeamMember(team.id, u.id), `${u.displayName || u.username} removed from ${team.name}`)
          }
        />

        <div className="card grow" style={{ flex: 1, minWidth: 0 }}>
          <div className="hd">What members receive</div>
          <div className="bd" style={{ paddingTop: 4, paddingBottom: 4 }}>
            {team.grants.length === 0 && <div className="muted" style={{ padding: '10px 0' }}>This team holds no grants yet.</div>}
            {team.grants.map((g, i) => <MiniGrantRow key={g.id} grant={g} borderTop={i > 0} />)}
          </div>
        </div>
      </div>
      <TeamRequests requests={team.requests} />
      <div className="muted" style={{ fontSize: 12 }}>
        Every change is audited under your name and admins are notified. An admin decides the team&apos;s requests; approving one grants
        the team, so every member gets it.
      </div>
    </div>
  );
}

/** The team's access requests, by any of its managers. */
function TeamRequests({ requests }: { requests: AccessRequest[] }) {
  const now = useNow(60_000);
  return (
    <div className="card">
      <div className="hd">Access requests for the team</div>
      {requests.length === 0 ? (
        <div className="bd muted">No requests yet.</div>
      ) : (
        <div className="tbl-wrap">
          <table className="tbl nowrap">
            <thead><tr><th>Requested</th><th>By</th><th>Scope</th><th>Actions</th><th>Duration</th><th>Status</th></tr></thead>
            <tbody>
              {groupRequests(requests).map((g) => {
                const r = g[0];
                return (
                  <tr key={groupKey(g)} className={g.every((x) => x.status === 'expired') ? 'off' : undefined}>
                    <td>{fmtWhen(r.createdAt, now)}</td>
                    <td className="mono">{r.username}</td>
                    <td>{g.length > 1 ? <BatchScope g={g} /> : <><ScopeCell scope={r.scope} /> <span className="muted">{scopeCluster(r.scope)}</span></>}</td>
                    <td><div style={{ minWidth: 200 }}><ActionChips actions={r.actions} /></div></td>
                    <td>{fmtHours(r.durationHours)}</td>
                    <td><GroupStatus g={g} /></td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
