import { useId, useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { api } from '../../api';
import type { AuditEvent, GrantView, TeamView, UserView } from '../../api';
import {
  AdminErrorBanner, ConfirmAdminDialog, FieldError, MiniGrantRow, ReadOnlyNote, UsersTeamsTabs,
} from '../../components/AdminPieces';
import { Avatar } from '../../components/Badges';
import { Dialog } from '../../components/Dialog';
import { Empty, ErrorBanner, Loading, Skeleton } from '../../components/Feedback';
import { Icon } from '../../components/Icon';
import { PageHeader } from '../../components/PageHeader';
import { useAuth } from '../../context/auth';
import { useApi } from '../../hooks/useApi';
import { useNow } from '../../hooks/useNow';
import { adminFieldErrors } from '../../lib/adminErrorText';
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
  const [pick, setPick] = useState('');
  const pickId = useId();
  const listId = useId();

  const memberIds = useMemo(() => new Set(team.members.map((x) => x.id)), [team.members]);
  const candidates = useMemo(
    () => (users ?? []).filter((u) => !memberIds.has(u.id) && !u.disabled),
    [users, memberIds],
  );

  const setMembers = async (ids: number[], msg: string) => {
    const r = await m.run(() => api.setTeamMembers(team.id, ids), msg);
    if (r.ok) onChanged();
    return r.ok;
  };

  const addByText = async () => {
    const q = pick.trim().toLowerCase();
    if (!q) return;
    // Datalist options read "Display name (username)"; also accept a bare username or name.
    const match =
      candidates.find((u) => `${u.displayName || u.username} (${u.username})`.toLowerCase() === q) ??
      candidates.find((u) => u.username.toLowerCase() === q || u.displayName.toLowerCase() === q);
    if (!match) return;
    const ok = await setMembers([...team.members.map((x) => x.id), match.id], `${match.username} added to ${team.name}`);
    if (ok) setPick('');
  };

  const remove = async (id: number, name: string) => {
    await setMembers(
      team.members.filter((x) => x.id !== id).map((x) => x.id),
      `${name} removed from ${team.name}`,
    );
  };

  const noMatch = pick.trim() !== '' && !candidates.some((u) => {
    const q = pick.trim().toLowerCase();
    return `${u.displayName || u.username} (${u.username})`.toLowerCase() === q || u.username.toLowerCase() === q || u.displayName.toLowerCase() === q;
  });

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
        <div className="card grow" style={{ flex: 1, minWidth: 0 }}>
          <div className="hd">
            Members
            <span className="grow" />
            {isAdmin && (
              <form
                className="row"
                style={{ gap: 6 }}
                onSubmit={(e) => {
                  e.preventDefault();
                  void addByText();
                }}
              >
                <label className="search" style={{ width: 200, height: 26, background: 'var(--sf)' }}>
                  <Icon name="plus" />
                  <input
                    id={pickId}
                    list={listId}
                    placeholder="Add by name"
                    aria-label="Add member"
                    value={pick}
                    disabled={m.busy}
                    onChange={(e) => setPick(e.target.value)}
                  />
                </label>
                <datalist id={listId}>
                  {candidates.map((u) => (
                    <option key={u.id} value={`${u.displayName || u.username} (${u.username})`} />
                  ))}
                </datalist>
                <button type="submit" className="btn sm" disabled={m.busy || !pick.trim() || noMatch}>
                  Add
                </button>
              </form>
            )}
          </div>
          {isAdmin && noMatch && (
            <div style={{ padding: '6px 16px' }}>
              <FieldError id={`${pickId}-e`} text="No enabled user matches. Pick a name from the list." />
            </div>
          )}
          {isAdmin && !users && !!usersError && (
            <div style={{ padding: '6px 16px' }} className="muted">Could not load users for the picker.</div>
          )}
          <div className="tbl-wrap">
            <table className="tbl" style={{ minWidth: 0 }}>
              <tbody>
                {team.members.map((u) => (
                  <tr key={u.id}>
                    <td>
                      <span className="row">
                        <Avatar name={u.displayName || u.username} size={24} />
                        {u.displayName || u.username}
                      </span>
                    </td>
                    <td className="mono muted">{u.username}</td>
                    <td style={{ textAlign: 'right' }}>
                      {isAdmin && (
                        <button
                          type="button"
                          className="btn ghost sm"
                          aria-label={`Remove ${u.displayName || u.username}`}
                          disabled={m.busy}
                          onClick={() => void remove(u.id, u.displayName || u.username)}
                        >
                          <Icon name="x" />
                        </button>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {team.members.length === 0 && <Empty>No members yet.</Empty>}
        </div>

        <div className="card grow" style={{ flex: 1, minWidth: 0 }}>
          <div className="hd">
            Grants
            <span className="grow" />
            <Link to="/grants" className="btn sm">
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

/** docs/api.md has no per-team history endpoint, so this searches the audit log by team name. */
function ChangeHistory({ team, now }: { team: TeamView; now: number }) {
  const hist = useApi<AuditEvent[]>(async () => {
    const page = await api.audit({ q: team.name, limit: 10 });
    return page.items.filter((e) => e.eventType.startsWith('admin') || e.eventType.includes('team'));
    // member count changes after add/remove, which refreshes the list
  }, [team.id, team.name, team.members.length]);

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
                    <b>{e.username}</b> <span className="mono">{e.eventType}</span>
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
