import { NavLink } from 'react-router-dom';
import type { Action, GrantView, Role } from '../api';
import { adminErrorText } from '../lib/adminErrorText';
import { fmtDay } from '../lib/time';
import { ActionChips } from './Badges';
import { Dialog } from './Dialog';
import { SCOPE_KIND_LABEL, scopeCluster, scopeName } from './Scope';

/** "Users | Teams" tab bar shared by the /users and /teams routes. */
export function UsersTeamsTabs({ users, teams }: { users?: number; teams?: number }) {
  const cls = ({ isActive }: { isActive: boolean }) => (isActive ? 'on' : undefined);
  return (
    <nav className="tabs" aria-label="Users and teams">
      <NavLink to="/users" className={cls}>
        Users {users !== undefined && <span className="c">{users}</span>}
      </NavLink>
      <NavLink to="/teams" className={cls}>
        Teams {teams !== undefined && <span className="c">{teams}</span>}
      </NavLink>
    </nav>
  );
}

export function ReadOnlyNote() {
  return (
    <span className="muted" style={{ fontSize: 12 }} role="note">
      Read-only view
    </span>
  );
}

export const ROLE_LABEL: Record<Role, string> = { user: 'User', auditor: 'Auditor', admin: 'Admin' };

export function RoleChip({ role }: { role: Role }) {
  return <span className={`chip${role === 'admin' ? ' info' : role === 'auditor' ? ' warn' : ''}`}>{ROLE_LABEL[role]}</span>;
}

/** Red banner for a failed admin mutation (409 "last admin" is made readable). */
export function AdminErrorBanner({ error }: { error: unknown }) {
  if (!error) return null;
  return (
    <div className="banner red" role="alert">
      {adminErrorText(error)}
    </div>
  );
}

export function FieldError({ id, text }: { id: string; text: string | undefined }) {
  if (!text) return null;
  return (
    <span id={id} className="err-text" role="alert">
      {text}
    </span>
  );
}

/** Confirmation dialog for destructive or disruptive admin actions. */
export function ConfirmAdminDialog({
  title, children, confirmLabel, danger, busy, error, onConfirm, onClose,
}: {
  title: string;
  children: React.ReactNode;
  confirmLabel: string;
  danger?: boolean;
  busy: boolean;
  error: unknown;
  onConfirm: () => void;
  onClose: () => void;
}) {
  return (
    <Dialog
      title={title}
      width={440}
      onClose={busy ? () => {} : onClose}
      footer={
        <>
          <button type="button" className="btn" onClick={onClose} disabled={busy}>
            Cancel
          </button>
          <button type="button" className={`btn ${danger ? 'dng' : 'pri'}`} onClick={onConfirm} disabled={busy}>
            {busy ? 'Working…' : confirmLabel}
          </button>
        </>
      }
    >
      {children}
      <AdminErrorBanner error={error} />
    </Dialog>
  );
}

/** One grant in an "effective access" style list: scope, actions, and where it comes from. */
type MiniGrant = Pick<GrantView, 'scope' | 'actions' | 'expiresAt'> & { expired?: boolean };

export function MiniGrantRow({ grant, via, borderTop = true }: { grant: MiniGrant; via?: string | null; borderTop?: boolean }) {
  const s = grant.scope;
  const origin = via === undefined ? '' : via ? `via team ${via}` : 'Direct';
  const expiry = !grant.expiresAt ? 'No expiry' : grant.expired ? '' : `expires ${fmtDay(grant.expiresAt)}`;
  return (
    <div style={{ padding: '10px 0', borderTop: borderTop ? '1px solid var(--ln)' : undefined, opacity: grant.expired ? 0.7 : 1 }}>
      <div className="row wrap">
        <span className="tag">{SCOPE_KIND_LABEL[s.kind]}</span>
        <b className="mono">{scopeName(s)}</b>
        <span className="muted">{scopeCluster(s)}</span>
      </div>
      <div className="row wrap" style={{ marginTop: 6 }}>
        <ActionChips actions={grant.actions as Action[]} />
        {grant.expired && <span className="chip warn">expired</span>}
        <span className="muted" style={{ marginLeft: 'auto' }}>
          {[origin, expiry].filter(Boolean).join(' · ')}
        </span>
      </div>
    </div>
  );
}
