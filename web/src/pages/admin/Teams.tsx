import { useId, useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { api } from '../../api';
import type { AuditEvent, GrantView, TeamMember, TeamView, UserView } from '../../api';
import {
  AdminErrorBanner, ConfirmAdminDialog, FieldError, MiniGrantRow, ReadOnlyNote, UsersTeamsTabs,
} from '../../components/AdminPieces';
import { Dialog } from '../../components/Dialog';
import { Empty, ErrorBanner, Loading, Skeleton } from '../../components/Feedback';
import { Icon } from '../../components/Icon';
import { PageHeader } from '../../components/PageHeader';
import { TeamPeopleCard } from '../../components/TeamPeople';
import { useAuth } from '../../context/auth';
import { useApi } from '../../hooks/useApi';
import { useNow } from '../../hooks/useNow';
import { adminFieldErrors } from '../../lib/adminErrorText';
import { eventTitle } from '../../lib/auditLabels';
import { fmtWhen } from '../../lib/time';
import { useAdminMutation } from '../../lib/useAdminMutation';

export function Teams() {
  const { isAdmin } = useAuth();
  const teams = useApi(() => api.teams(), []);
  const users = useApi(() => api.users(), []);
  const [selId, setSelId] = useState<number | null>(null);
  const [dialog, setDialog] = useState<'create' | 'rename' | 'delete' | null>(null);

  const list = teams.data;
  // Default to the first team once loaded; fall back if the selected one vanished.
  const selected = list?.find((t) => t.id === selId) ?? list?.[0] ?? null;

  return (
    <>
      <PageHeader title="Users & teams" subtitle="Teams carry grants; people inherit them">
        {isAdmin ? (
          <button type="button" className="btn pri" onClick={() => setDialog('create')}>
            <Icon name="plus" />
            New team
          </button>
        ) : (
          <ReadOnlyNote />
        )}
      </PageHeader>
      <UsersTeamsTabs users={users.data?.length} teams={list?.length} />

      {teams.loading && !list && <Loading what="teams" />}
      {!!teams.error && !list && <ErrorBanner error={teams.error} onRetry={teams.reload} />}

      {list && (
        <div className="two" style={{ marginTop: 12 }}>
          <div className="card" style={{ width: 400, flex: 'none' }}>
            <div className="tbl-wrap">
              <table className="tbl" style={{ minWidth: 0 }}>
                <thead>
                  <tr>
                    <th>Team</th>
                    <th>Members</th>
                  </tr>
                </thead>
                <tbody>
                  {list.map((t) => (
                    <tr key={t.id} className={`click${selected?.id === t.id ? ' sel' : ''}`} onClick={() => setSelId(t.id)}>
                      <td>
                        <button
                          type="button"
                          className="linklike"
                          style={{ color: 'inherit' }}
                          aria-pressed={selected?.id === t.id}
                          onClick={() => setSelId(t.id)}
                        >
                          {t.name}
                        </button>
                      </td>
                      <td>{t.members.length}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            {list.length === 0 && <Empty>No teams yet.</Empty>}
          </div>

          {selected && (
            <TeamDetail
              key={selected.id}
              team={selected}
              users={users.data}
              usersError={users.error}
              isAdmin={isAdmin}
              onChanged={() => teams.reload()}
              onRename={() => setDialog('rename')}
              onDelete={() => setDialog('delete')}
            />
          )}
        </div>
      )}

      {dialog === 'create' && (
        <TeamFormDialog
          onClose={() => setDialog(null)}
          onDone={(t) => {
            setDialog(null);
            setSelId(t.id);
            teams.reload();
          }}
        />
      )}
      {dialog === 'rename' && selected && (
        <TeamFormDialog
          team={selected}
          onClose={() => setDialog(null)}
          onDone={() => {
            setDialog(null);
            teams.reload();
          }}
        />
      )}
      {dialog === 'delete' && selected && (
        <DeleteTeamDialog
          team={selected}
          onClose={() => setDialog(null)}
          onDone={() => {
            setDialog(null);
            setSelId(null);
            teams.reload();
          }}
        />
      )}
    </>
  );
}

function TeamDetail({
  team, users, usersError, isAdmin, onChanged, onRename, onDelete,
}: {
  team: TeamView;
  users: UserView[] | undefined;
  usersError: unknown;
  isAdmin: boolean;
  onChanged: () => void;
  onRename: () => void;
  onDelete: () => void;
}) {
  const now = useNow(60_000);
  const m = useAdminMutation();
  const grants = useApi<GrantView[]>(() => api.grants({ holderKind: 'team', holderId: team.id }), [team.id]);

  // Anyone enabled may be a member; managers must log in through Keycloak.
  const members = useMemo(() => (users ?? []).filter((u) => !u.disabled), [users]);
  const managers = useMemo(() => members.filter((u) => !u.local), [members]);

  const change = async (fn: () => Promise<unknown>, msg: string) => {
    const r = await m.run(fn, msg);
    if (r.ok) onChanged();
    return r.ok;
  };
  const ids = (xs: TeamMember[]) => xs.map((x) => x.id);
  const name = (u: TeamMember) => u.displayName || u.username;

  return (
    <div className="col grow" style={{ flex: 1, minWidth: 0, gap: 16 }}>
      <div className="row wrap">
        <h2 style={{ margin: 0, fontSize: 16 }}>{team.name}</h2>
        <span className="muted">
          {team.members.length} member{team.members.length === 1 ? '' : 's'}
          {grants.data ? ` · ${grants.data.length} grant${grants.data.length === 1 ? '' : 's'}` : ''}
        </span>
        <span className="grow" />
        {isAdmin && (
          <>
            <button type="button" className="btn sm" onClick={onRename}>
              Rename
            </button>
            <button type="button" className="btn dng sm" onClick={onDelete}>
              Delete team
            </button>
          </>
        )}
      </div>
      {team.description && <div className="muted" style={{ marginTop: -8 }}>{team.description}</div>}
      <AdminErrorBanner error={m.error} />

      <div className="two" style={{ gap: 16 }}>
        <div className="col" style={{ flex: 1, minWidth: 0, gap: 16 }}>
          <TeamPeopleCard
            title="Members"
            people={team.members}
            candidates={members}
            editable={isAdmin}
            busy={m.busy}
            noMatchText="No enabled user matches. Pick a name from the list."
            candidatesError={!users && !!usersError}
            emptyText="No members yet."
            onAdd={(u) => change(() => api.setTeamMembers(team.id, [...ids(team.members), u.id]), `${u.username} added to ${team.name}`)}
            onRemove={(u) =>
              void change(
                () => api.setTeamMembers(team.id, ids(team.members).filter((id) => id !== u.id)),
                `${name(u)} removed from ${team.name}`,
              )
            }
          />
          <div className="col" style={{ gap: 8 }}>
            <TeamPeopleCard
              title="Managers"
              people={team.managers}
              candidates={managers}
              editable={isAdmin}
              busy={m.busy}
              noMatchText="No enabled Keycloak user matches. Local users cannot manage a team."
              candidatesError={!users && !!usersError}
              emptyText="No managers. Only admins change the members."
              onAdd={(u) => change(() => api.setTeamManagers(team.id, [...ids(team.managers), u.id]), `${u.username} now manages ${team.name}`)}
              onRemove={(u) =>
                void change(
                  () => api.setTeamManagers(team.id, ids(team.managers).filter((id) => id !== u.id)),
                  `${name(u)} no longer manages ${team.name}`,
                )
              }
            />
            <div className="muted" style={{ fontSize: 12 }}>
              Managers add and remove members themselves on My teams, so the people they add receive this team&apos;s grants.
              Every change is audited and admins are notified.
            </div>
          </div>
        </div>

        <div className="card grow" style={{ flex: 1, minWidth: 0 }}>
          <div className="hd">
            Grants
            <span className="grow" />
            <Link to={`/grants?holder=team:${team.id}`} className="btn sm">
              Open in Grants
            </Link>
          </div>
          <div className="bd" style={{ paddingTop: 4, paddingBottom: 4 }}>
            {grants.loading && !grants.data && <Skeleton rows={3} />}
            {!!grants.error && !grants.data && <ErrorBanner error={grants.error} onRetry={grants.reload} title="Could not load grants" />}
            {grants.data && grants.data.length === 0 && <div className="muted" style={{ padding: '10px 0' }}>No grants for this team.</div>}
            {grants.data?.map((g, i) => <MiniGrantRow key={g.id} grant={g} borderTop={i > 0} />)}
          </div>
        </div>
      </div>

      <ChangeHistory team={team} now={now} />
    </div>
  );
}

/** The team's last admin changes (audit details.entity = team, details.id = its id). */
function ChangeHistory({ team, now }: { team: TeamView; now: number }) {
  const hist = useApi<AuditEvent[]>(
    async () => (await api.audit({ eventType: 'admin.change', entity: 'team', entityId: String(team.id), limit: 10 })).items,
    // renames and member/manager changes refresh the list
    [team.id, team.name, team.members.length, team.managers.length],
  );

  return (
    <div className="card">
      <div className="hd">Change history</div>
      {hist.loading && !hist.data && <Skeleton rows={2} />}
      {!!hist.error && !hist.data && <div className="bd muted">Change history is not available for your role.</div>}
      {hist.data && hist.data.length === 0 && <div className="bd muted">No recorded changes</div>}
      {hist.data && hist.data.length > 0 && (
        <div className="tbl-wrap">
          <table className="tbl" style={{ minWidth: 0 }}>
            <tbody>
              {hist.data.map((e) => (
                <tr key={e.id}>
                  <td className="mono muted" style={{ width: 150 }}>
                    {fmtWhen(e.at, now)}
                  </td>
                  <td>
                    <b>{e.username}</b> {eventTitle(e)}
                    {e.reason ? <span className="muted"> · {e.reason}</span> : null}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

function TeamFormDialog({ team, onClose, onDone }: { team?: TeamView; onClose: () => void; onDone: (t: TeamView) => void }) {
  const formId = useId();
  const m = useAdminMutation();
  const [name, setName] = useState(team?.name ?? '');
  const [description, setDescription] = useState(team?.description ?? '');
  const [localErr, setLocalErr] = useState('');
  const fields = adminFieldErrors(m.error);
  const nameErr = localErr || fields.name;

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    const n = name.trim();
    if (!n) {
      setLocalErr('Name is required');
      return;
    }
    setLocalErr('');
    const r = await m.run(
      () => (team ? api.patchTeam(team.id, { name: n, description }) : api.createTeam({ name: n, description })),
      team ? `Team renamed to ${n}` : `Team ${n} created`,
    );
    if (r.ok) onDone(r.value);
  };

  return (
    <Dialog
      title={team ? `Rename ${team.name}` : 'New team'}
      width={440}
      onClose={m.busy ? () => {} : onClose}
      footer={
        <>
          <button type="button" className="btn" onClick={onClose} disabled={m.busy}>
            Cancel
          </button>
          <button type="submit" form={formId} className="btn pri" disabled={m.busy}>
            {m.busy ? 'Saving…' : team ? 'Save' : 'Create team'}
          </button>
        </>
      }
    >
      <form id={formId} className="col" onSubmit={(e) => void submit(e)}>
        <label className="f">
          Name
          <input
            className="input"
            data-autofocus
            value={name}
            onChange={(e) => setName(e.target.value)}
            aria-invalid={!!nameErr}
            aria-describedby={nameErr ? `${formId}-n` : undefined}
          />
          <FieldError id={`${formId}-n`} text={nameErr} />
        </label>
        <label className="f">
          Description <span className="hint">(optional)</span>
          <input className="input" value={description} onChange={(e) => setDescription(e.target.value)} />
          <FieldError id={`${formId}-d`} text={fields.description} />
        </label>
        <AdminErrorBanner error={Object.keys(fields).length ? null : m.error} />
      </form>
    </Dialog>
  );
}

function DeleteTeamDialog({ team, onClose, onDone }: { team: TeamView; onClose: () => void; onDone: () => void }) {
  const m = useAdminMutation();
  const go = async () => {
    const r = await m.run(() => api.deleteTeam(team.id), `Team ${team.name} deleted`);
    if (r.ok) onDone();
  };
  return (
    <ConfirmAdminDialog
      title={`Delete team ${team.name}?`}
      confirmLabel="Delete team"
      danger
      busy={m.busy}
      error={m.error}
      onConfirm={() => void go()}
      onClose={onClose}
    >
      <div className="banner prod" style={{ fontSize: 12 }}>
        Its {team.members.length} member{team.members.length === 1 ? '' : 's'} lose any access they only had through this team.
      </div>
    </ConfirmAdminDialog>
  );
}
