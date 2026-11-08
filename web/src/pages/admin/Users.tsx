import { useId, useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { api } from '../../api';
import type { GrantView, Role, TeamView, UserView } from '../../api';
import {
  AdminErrorBanner, ConfirmAdminDialog, FieldError, MiniGrantRow, ReadOnlyNote, RoleChip, ROLE_LABEL, UsersTeamsTabs,
} from '../../components/AdminPieces';
import { Avatar } from '../../components/Badges';
import { Dialog } from '../../components/Dialog';
import { Empty, ErrorBanner, Loading, Skeleton } from '../../components/Feedback';
import { Icon } from '../../components/Icon';
import { Select } from '../../components/Select';
import { PageHeader } from '../../components/PageHeader';
import { Seg } from '../../components/Seg';
import { useAuth } from '../../context/auth';
import { useApi } from '../../hooks/useApi';
import { useUrlState } from '../../hooks/useUrlState';
import { useNow } from '../../hooks/useNow';
import { adminFieldErrors } from '../../lib/adminErrorText';
import { fmtWhen } from '../../lib/time';
import { useAdminMutation } from '../../lib/useAdminMutation';

type StatusFilter = 'all' | 'active' | 'disabled';

const ROLES: Role[] = ['user', 'auditor', 'admin'];

function isWaiting(u: UserView): boolean {
  return !u.keycloakBound && !u.lastLoginAt;
}

function displayOf(u: UserView): string {
  return isWaiting(u) && !u.displayName ? '(pre-created)' : u.displayName || u.username;
}

function StatusChip({ user }: { user: UserView }) {
  if (user.disabled) return <span className="chip err">Disabled</span>;
  if (isWaiting(user)) return <span className="chip">Waiting for first login</span>;
  return <span className="chip ok">Active</span>;
}

export function Users() {
  const { isAdmin, me } = useAuth();
  const localUsers = me.settings.localUsers;
  const users = useApi(() => api.users(), []);
  const teams = useApi(() => api.teams(), []);
  const now = useNow(60_000);
  const [f, setF] = useUrlState({ q: '', status: 'all' });
  const query = f.q;
  const status: StatusFilter = f.status === 'active' || f.status === 'disabled' ? f.status : 'all';
  const [selId, setSelId] = useState<number | null>(null);
  const [dialog, setDialog] = useState<'create' | 'role' | 'team' | 'disable' | 'password' | null>(null);
  const toggle = useAdminMutation();

  const list = users.data;
  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    return (list ?? []).filter((u) => {
      if (status === 'active' && u.disabled) return false;
      if (status === 'disabled' && !u.disabled) return false;
      if (!q) return true;
      const hay = [u.displayName, u.username, u.role, ...u.teams.map((t) => t.name)].join(' ').toLowerCase();
      return hay.includes(q);
    });
  }, [list, query, status]);

  const selected = list?.find((u) => u.id === selId) ?? null;

  const reloadAll = () => {
    users.reload();
    teams.reload();
  };

  const enable = async (u: UserView) => {
    const r = await toggle.run(() => api.patchUser(u.id, { disabled: false }), `${u.username} enabled`);
    if (r.ok) users.reload();
  };

  return (
    <>
      <PageHeader title="Users & teams" subtitle="Users appear after their first Keycloak login. Roles are set here, not in Keycloak.">
        {isAdmin ? (
          <button type="button" className="btn pri" onClick={() => setDialog('create')}>
            <Icon name="plus" />
            {localUsers ? 'Add user' : 'Pre-create user'}
          </button>
        ) : (
          <ReadOnlyNote />
        )}
      </PageHeader>
      <UsersTeamsTabs users={list?.length} teams={teams.data?.length} />

      <div className="row wrap" style={{ marginTop: 12 }}>
        <label className="search" style={{ width: 'min(320px,100%)' }}>
          <Icon name="search" />
          <input
            placeholder="Search name, username, role or team"
            aria-label="Search users"
            value={query}
            onChange={(e) => setF({ q: e.target.value })}
          />
        </label>
        <Seg
          label="Filter by status"
          value={status}
          onChange={(v: StatusFilter) => setF({ status: v })}
          options={[
            { value: 'all', label: 'All' },
            { value: 'active', label: 'Active' },
            { value: 'disabled', label: 'Disabled' },
          ]}
        />
      </div>

      <AdminErrorBanner error={toggle.error} />

      {users.loading && !list && <Loading what="users" />}
      {!!users.error && !list && <ErrorBanner error={users.error} onRetry={users.reload} />}

      {list && (
        <div className="two" style={{ marginTop: 12 }}>
          <div className="card grow" style={{ flex: 1, minWidth: 0 }}>
            <div className="tbl-wrap">
              <table className="tbl">
                <thead>
                  <tr>
                    <th>Name</th>
                    <th>Username</th>
                    <th>Role</th>
                    <th>Teams</th>
                    <th>Last login</th>
                    <th>Open sessions</th>
                    <th>Status</th>
                    <th />
                  </tr>
                </thead>
                <tbody>
                  {filtered.map((u) => {
                    const off = u.disabled || isWaiting(u);
                    return (
                      <tr
                        key={u.id}
                        className={`click${off ? ' off' : ''}${u.id === selId ? ' sel' : ''}`}
                        onClick={() => setSelId(u.id)}
                      >
                        <td>
                          <span className="row">
                            <Avatar name={u.displayName || u.username} size={24} />
                            <button
                              type="button"
                              className="linklike"
                              style={{ color: 'inherit', fontWeight: off ? 400 : 600 }}
                              aria-pressed={u.id === selId}
                              onClick={() => setSelId(u.id)}
                            >
                              {displayOf(u)}
                            </button>
                          </span>
                        </td>
                        <td className="mono">
                          {u.username}
                          {u.local && (
                            <span className="tag" style={{ marginLeft: 6 }} title="Signs in with a portal password, not Keycloak">
                              Local
                            </span>
                          )}
                        </td>
                        <td>
                          <RoleChip role={u.role} />
                        </td>
                        <td>
                          {u.teams.length === 0 ? (
                            '—'
                          ) : (
                            <span className="row wrap" style={{ gap: 4 }}>
                              {u.teams.map((t) => (
                                <span key={t.id} className="tag">
                                  {t.name}
                                </span>
                              ))}
                            </span>
                          )}
                        </td>
                        <td>{fmtWhen(u.lastLoginAt, now)}</td>
                        <td>
                          {u.activeSessions > 0 ? (
                            <span className="chip prod">
                              <span className="dot rec" />
                              {u.activeSessions} open
                            </span>
                          ) : (
                            0
                          )}
                        </td>
                        <td>
                          <StatusChip user={u} />
                        </td>
                        <td>
                          <button
                            type="button"
                            className="btn ghost sm"
                            aria-label={`Open access panel for ${u.displayName || u.username}`}
                            onClick={() => setSelId(u.id)}
                          >
                            ···
                          </button>
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
            {filtered.length === 0 && <Empty>{list.length === 0 ? 'No users yet.' : 'No users match this filter.'}</Empty>}
          </div>

          {selected && (
            <aside className="card" style={{ width: 400, flex: 'none' }} aria-label="Effective access">
              <div className="hd">
                <Avatar name={selected.displayName || selected.username} />
                <div style={{ minWidth: 0 }}>
                  <div>{displayOf(selected)}</div>
                  <small>Effective access: what they can use, and through which grant</small>
                </div>
                <span className="grow" />
                <button type="button" className="btn ghost sm" aria-label="Close panel" onClick={() => setSelId(null)}>
                  <Icon name="x" />
                </button>
              </div>
              <div className="bd col" style={{ gap: 12 }}>
                {isAdmin && (
                  <div className="row wrap">
                    <button type="button" className="btn sm" onClick={() => setDialog('role')}>
                      Change role
                    </button>
                    <button type="button" className="btn sm" onClick={() => setDialog('team')}>
                      Add to team
                    </button>
                    {selected.local && localUsers && (
                      <button type="button" className="btn sm" onClick={() => setDialog('password')}>
                        Set password
                      </button>
                    )}
                    {selected.disabled ? (
                      <button type="button" className="btn sm" disabled={toggle.busy} onClick={() => void enable(selected)}>
                        {toggle.busy ? 'Enabling…' : 'Enable'}
                      </button>
                    ) : (
                      <button type="button" className="btn dng sm" onClick={() => setDialog('disable')}>
                        Disable
                      </button>
                    )}
                  </div>
                )}
                {selected.disabled && <div className="banner yel" style={{ fontSize: 12 }}>This user is disabled and cannot open sessions or streams.</div>}
                {isWaiting(selected) && !selected.disabled && (
                  <div className="banner info" style={{ fontSize: 12 }}>
                    Waiting for first login. Grants and teams apply as soon as this user signs in
                    {selected.local ? ' with their password.' : ' through Keycloak.'}
                  </div>
                )}
                {isAdmin && !selected.disabled && selected.activeSessions > 0 && (
                  <div className="banner prod" style={{ fontSize: 12 }}>
                    Disabling ends {selected.activeSessions} open exec session{selected.activeSessions === 1 ? '' : 's'} immediately.
                  </div>
                )}
                <EffectiveAccess user={selected} />
              </div>
            </aside>
          )}
        </div>
      )}

      {dialog === 'create' && (
        <CreateUserDialog
          localUsers={localUsers}
          onClose={() => setDialog(null)}
          onDone={(u) => {
            setDialog(null);
            users.reload();
            setSelId(u.id);
          }}
        />
      )}
      {dialog === 'role' && selected && (
        <ChangeRoleDialog
          user={selected}
          onClose={() => setDialog(null)}
          onDone={() => {
            setDialog(null);
            users.reload();
          }}
        />
      )}
      {dialog === 'team' && selected && teams.data && (
        <AddToTeamDialog
          user={selected}
          teams={teams.data}
          onClose={() => setDialog(null)}
          onDone={() => {
            setDialog(null);
            reloadAll();
          }}
        />
      )}
      {dialog === 'team' && selected && !teams.data && (
        <Dialog title="Add to team" onClose={() => setDialog(null)}>
          {teams.error ? <ErrorBanner error={teams.error} onRetry={teams.reload} /> : <Skeleton rows={2} />}
        </Dialog>
      )}
      {dialog === 'password' && selected && (
        <SetPasswordDialog
          user={selected}
          onClose={() => setDialog(null)}
          onDone={() => {
            setDialog(null);
            users.reload();
          }}
        />
      )}
      {dialog === 'disable' && selected && (
        <DisableDialog
          user={selected}
          onClose={() => setDialog(null)}
          onDone={() => {
            setDialog(null);
            users.reload();
          }}
        />
      )}
    </>
  );
}

interface AccessRow {
  grant: GrantView;
  via: string | null;
}

function EffectiveAccess({ user }: { user: UserView }) {
  const teamKey = user.teams.map((t) => t.id).join(',');
  const acc = useApi<AccessRow[]>(
    async () => {
      const [direct, ...perTeam] = await Promise.all([
        api.grants({ holderKind: 'user', holderId: user.id }),
        ...user.teams.map((t) => api.grants({ holderKind: 'team', holderId: t.id })),
      ]);
      return [
        ...direct.map((grant) => ({ grant, via: null })),
        ...perTeam.flatMap((gs, i) => gs.map((grant) => ({ grant, via: user.teams[i].name }))),
      ];
    },
    [user.id, teamKey],
  );

  if (acc.loading && !acc.data) return <Skeleton rows={3} />;
  if (acc.error && !acc.data) return <ErrorBanner error={acc.error} onRetry={acc.reload} title="Could not load grants" />;
  const rows = acc.data ?? [];
  return (
    <div className="col" style={{ gap: 0 }}>
      {rows.length === 0 && (
        <div className="muted" style={{ padding: '10px 0', borderTop: '1px solid var(--ln)' }}>
          No grants. This user can only use what break-glass or a new grant opens up.
        </div>
      )}
      {rows.map((r) => (
        <MiniGrantRow key={`${r.via ?? 'u'}-${r.grant.id}`} grant={r.grant} via={r.via} />
      ))}
      <div style={{ paddingTop: 10, borderTop: '1px solid var(--ln)' }}>
        <Link to={`/grants?holder=user:${user.id}`} className="linklike blue">
          Open in Grants
        </Link>
      </div>
    </div>
  );
}

type SignIn = 'keycloak' | 'local';

function CreateUserDialog({ localUsers, onClose, onDone }: { localUsers: boolean; onClose: () => void; onDone: (u: UserView) => void }) {
  const formId = useId();
  const m = useAdminMutation();
  const [username, setUsername] = useState('');
  const [displayName, setDisplayName] = useState('');
  const [role, setRole] = useState<Role>('user');
  const [signIn, setSignIn] = useState<SignIn>('keycloak');
  const [password, setPassword] = useState('');
  const [localErr, setLocalErr] = useState('');
  const fields = adminFieldErrors(m.error);
  const uErr = localErr || fields.username;
  const local = localUsers && signIn === 'local';

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    const name = username.trim();
    if (!name) {
      setLocalErr('Username is required');
      return;
    }
    setLocalErr('');
    const r = await m.run(
      () =>
        api.createUser({
          username: name, role,
          ...(displayName.trim() ? { displayName: displayName.trim() } : {}),
          ...(local ? { password } : {}),
        }),
      local ? `${name} created` : `${name} pre-created`,
    );
    if (r.ok) onDone(r.value);
  };

  return (
    <Dialog
      title={localUsers ? 'Add user' : 'Pre-create user'}
      subtitle={
        local
          ? 'A local user signs in with this password on the login page, without Keycloak.'
          : 'Match the Keycloak username exactly. The user is bound on their first login.'
      }
      onClose={m.busy ? () => {} : onClose}
      footer={
        <>
          <button type="button" className="btn" onClick={onClose} disabled={m.busy}>
            Cancel
          </button>
          <button type="submit" form={formId} className="btn pri" disabled={m.busy}>
            {m.busy ? 'Creating…' : 'Create user'}
          </button>
        </>
      }
    >
      <form id={formId} className="col" onSubmit={(e) => void submit(e)}>
        <label className="f">
          Username
          <input
            className="input mono"
            data-autofocus
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            aria-invalid={!!uErr}
            aria-describedby={uErr ? `${formId}-u` : undefined}
            autoComplete="off"
          />
          <FieldError id={`${formId}-u`} text={uErr} />
        </label>
        <label className="f">
          Display name <span className="hint">(optional)</span>
          <input className="input" value={displayName} onChange={(e) => setDisplayName(e.target.value)} />
          <FieldError id={`${formId}-d`} text={fields.displayName} />
        </label>
        <div className="f">
          Role
          <Seg label="Role" value={role} onChange={setRole} options={ROLES.map((r) => ({ value: r, label: ROLE_LABEL[r] }))} />
          <FieldError id={`${formId}-r`} text={fields.role} />
        </div>
        {localUsers && (
          <div className="f">
            Sign-in
            <Seg
              label="Sign-in"
              value={signIn}
              onChange={setSignIn}
              options={[
                { value: 'keycloak', label: 'Keycloak' },
                { value: 'local', label: 'Local password' },
              ]}
            />
          </div>
        )}
        {local && (
          <label className="f">
            Password <span className="hint">(at least 12 characters)</span>
            <input
              className="input"
              type="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              aria-invalid={!!fields.password}
              aria-describedby={fields.password ? `${formId}-p` : undefined}
              autoComplete="new-password"
            />
            <FieldError id={`${formId}-p`} text={fields.password} />
          </label>
        )}
        <AdminErrorBanner error={Object.keys(fields).length ? null : m.error} />
      </form>
    </Dialog>
  );
}

function ChangeRoleDialog({ user, onClose, onDone }: { user: UserView; onClose: () => void; onDone: () => void }) {
  const m = useAdminMutation();
  const [role, setRole] = useState<Role>(user.role);
  const save = async () => {
    const r = await m.run(() => api.patchUser(user.id, { role }), `${user.username} is now ${ROLE_LABEL[role]}`);
    if (r.ok) onDone();
  };
  return (
    <Dialog
      title={`Change role for ${user.displayName || user.username}`}
      subtitle="Takes effect on their next request. Open sessions end if the new role cannot use them."
      width={440}
      onClose={m.busy ? () => {} : onClose}
      footer={
        <>
          <button type="button" className="btn" onClick={onClose} disabled={m.busy}>
            Cancel
          </button>
          <button type="button" className="btn pri" onClick={() => void save()} disabled={m.busy || role === user.role}>
            {m.busy ? 'Saving…' : 'Save role'}
          </button>
        </>
      }
    >
      <div className="f">
        Role
        <Seg label="Role" value={role} onChange={setRole} options={ROLES.map((r) => ({ value: r, label: ROLE_LABEL[r] }))} />
      </div>
      <div className="muted" style={{ fontSize: 12 }}>
        User: runs workloads within granted scope and requests access. Auditor: the same, plus read-only access to audit, sessions and configuration. Admin: manages everything and decides requests.
      </div>
      <AdminErrorBanner error={m.error} />
    </Dialog>
  );
}

function AddToTeamDialog({
  user, teams, onClose, onDone,
}: {
  user: UserView;
  teams: TeamView[];
  onClose: () => void;
  onDone: () => void;
}) {
  const formId = useId();
  const m = useAdminMutation();
  const available = teams.filter((t) => !t.members.some((x) => x.id === user.id));
  const [teamId, setTeamId] = useState<number | ''>(available[0]?.id ?? '');
  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    const team = available.find((t) => t.id === teamId);
    if (!team) return;
    const ids = Array.from(new Set([...team.members.map((x) => x.id), user.id]));
    const r = await m.run(() => api.setTeamMembers(team.id, ids), `${user.username} added to ${team.name}`);
    if (r.ok) onDone();
  };
  return (
    <Dialog
      title={`Add ${user.displayName || user.username} to a team`}
      subtitle="They inherit all of the team's grants."
      width={440}
      onClose={m.busy ? () => {} : onClose}
      footer={
        <>
          <button type="button" className="btn" onClick={onClose} disabled={m.busy}>
            Cancel
          </button>
          <button type="submit" form={formId} className="btn pri" disabled={m.busy || teamId === ''}>
            {m.busy ? 'Adding…' : 'Add to team'}
          </button>
        </>
      }
    >
      <form id={formId} className="col" onSubmit={(e) => void submit(e)}>
        {available.length === 0 ? (
          <div className="muted">{teams.length === 0 ? 'There are no teams yet. Create one on the Teams tab.' : 'This user is already in every team.'}</div>
        ) : (
          <label className="f">
            Team
            <Select label="Team" autoFocus style={{ height: 30, width: '100%' }} value={teamId} onChange={setTeamId}
              options={available.map((t) => ({ value: t.id, label: t.name, hint: `${t.members.length} members` }))} />
          </label>
        )}
        <AdminErrorBanner error={m.error} />
      </form>
    </Dialog>
  );
}

function SetPasswordDialog({ user, onClose, onDone }: { user: UserView; onClose: () => void; onDone: () => void }) {
  const formId = useId();
  const m = useAdminMutation();
  const [password, setPassword] = useState('');
  const fields = adminFieldErrors(m.error);
  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    const r = await m.run(() => api.setUserPassword(user.id, password), `Password set for ${user.username}`);
    if (r.ok) onDone();
  };
  return (
    <Dialog
      title={`Set password for ${user.displayName || user.username}`}
      subtitle="Their open sessions and streams end, and they sign in again with the new password."
      width={440}
      onClose={m.busy ? () => {} : onClose}
      footer={
        <>
          <button type="button" className="btn" onClick={onClose} disabled={m.busy}>
            Cancel
          </button>
          <button type="submit" form={formId} className="btn pri" disabled={m.busy || !password}>
            {m.busy ? 'Saving…' : 'Set password'}
          </button>
        </>
      }
    >
      <form id={formId} className="col" onSubmit={(e) => void submit(e)}>
        <label className="f">
          New password <span className="hint">(at least 12 characters)</span>
          <input
            className="input"
            type="password"
            data-autofocus
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            aria-invalid={!!fields.password}
            aria-describedby={fields.password ? `${formId}-p` : undefined}
            autoComplete="new-password"
          />
          <FieldError id={`${formId}-p`} text={fields.password} />
        </label>
        <AdminErrorBanner error={Object.keys(fields).length ? null : m.error} />
      </form>
    </Dialog>
  );
}

function DisableDialog({ user, onClose, onDone }: { user: UserView; onClose: () => void; onDone: () => void }) {
  const m = useAdminMutation();
  const go = async () => {
    const r = await m.run(() => api.patchUser(user.id, { disabled: true }), `${user.username} disabled`);
    if (r.ok) onDone();
  };
  return (
    <ConfirmAdminDialog
      title={`Disable ${user.displayName || user.username}?`}
      confirmLabel="Disable user"
      danger
      busy={m.busy}
      error={m.error}
      onConfirm={() => void go()}
      onClose={onClose}
    >
      <div className="banner prod" style={{ fontSize: 12 }}>
        Disabling ends {user.activeSessions} open exec session{user.activeSessions === 1 ? '' : 's'} immediately, and closes their log streams.
      </div>
      <span className="muted" style={{ fontSize: 12 }}>
        You can enable the account again at any time. Grants are kept.
      </span>
    </ConfirmAdminDialog>
  );
}
