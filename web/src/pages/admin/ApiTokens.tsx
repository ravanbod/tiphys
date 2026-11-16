import { useId, useState } from 'react';
import { api } from '../../api';
import type { ApiPermission, ApiTokenView } from '../../api';
import { AdminErrorBanner, ConfirmAdminDialog, FieldError } from '../../components/AdminPieces';
import { Dialog } from '../../components/Dialog';
import { Empty, ErrorBanner, Skeleton } from '../../components/Feedback';
import { Icon } from '../../components/Icon';
import { Seg } from '../../components/Seg';
import { useToast } from '../../context/toast';
import { useApi } from '../../hooks/useApi';
import { useNow } from '../../hooks/useNow';
import { adminFieldErrors } from '../../lib/adminErrorText';
import { curlExample, EXPIRY_CHOICES, expiryFrom, PERMISSION_LABEL, PERMISSION_ROWS, togglePermission } from '../../lib/apiTokens';
import type { PermissionColumn } from '../../lib/apiTokens';
import { fmtDay, fmtWhen } from '../../lib/time';
import { useAdminMutation } from '../../lib/useAdminMutation';

const STATUS_CHIP: Record<ApiTokenView['status'], { cls: string; label: string }> = {
  active: { cls: 'chip ok', label: 'Active' },
  expired: { cls: 'chip warn', label: 'Expired' },
  revoked: { cls: 'chip', label: 'Revoked' },
};

/**
 * API tokens: service accounts that change time windows, holidays and
 * settings with the permissions chosen here. Applies at once (not through
 * the page's Save). Only admins create and revoke; auditors read.
 */
export function ApiTokensCard({ isAdmin }: { isAdmin: boolean }) {
  const tokens = useApi(() => api.apiTokens(), []);
  const m = useAdminMutation();
  const now = useNow(60_000);
  const [creating, setCreating] = useState(false);
  const [pendingRevoke, setPendingRevoke] = useState<ApiTokenView | null>(null);
  const list = tokens.data;

  const revoke = async (t: ApiTokenView) => {
    const r = await m.run(() => api.revokeApiToken(t.id), `${t.name} is revoked`);
    if (r.ok) {
      setPendingRevoke(null);
      tokens.reload();
    }
  };

  return (
    <div className="card">
      <div className="hd">
        <Icon name="key" />
        API tokens <small>Change time windows, holidays and settings through the API.</small>
        <span className="grow" />
        {isAdmin && (
          <button type="button" className="btn sm" onClick={() => setCreating(true)}>
            <Icon name="plus" />
            New token
          </button>
        )}
      </div>
      {tokens.loading && !list && <Skeleton rows={2} />}
      {!!tokens.error && !list && <div className="bd"><ErrorBanner error={tokens.error} onRetry={tokens.reload} /></div>}
      {list && (
        <div className="tbl-wrap">
          <table className="tbl" style={{ minWidth: 720 }}>
            <thead>
              <tr>
                <th>Name</th>
                <th>Permissions</th>
                <th>Created</th>
                <th>Expires</th>
                <th>Last used</th>
                <th>Status</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {list.map((t) => (
                <tr key={t.id} className={t.status === 'active' ? undefined : 'off'}>
                  <td>
                    <div style={{ fontWeight: 600 }}>{t.name}</div>
                    <div className="mono muted" style={{ fontSize: 11 }}>{t.prefix}…</div>
                    {t.description && <div className="muted" style={{ fontSize: 12 }}>{t.description}</div>}
                  </td>
                  <td>
                    <span className="row wrap" style={{ gap: 4 }}>
                      {t.permissions.map((p) => (
                        <span key={p} className="tag" title={p}>
                          {PERMISSION_LABEL[p] ?? p}
                        </span>
                      ))}
                    </span>
                  </td>
                  <td>
                    {fmtDay(t.createdAt)}
                    <div className="muted" style={{ fontSize: 12 }}>by {t.createdBy}</div>
                  </td>
                  <td>{t.expiresAt ? fmtDay(t.expiresAt) : <span className="muted">Never</span>}</td>
                  <td>{t.lastUsedAt ? fmtWhen(t.lastUsedAt, now) : <span className="muted">Never</span>}</td>
                  <td>
                    <span className={STATUS_CHIP[t.status].cls} title={t.revokedAt ? `Revoked by ${t.revokedBy}` : undefined}>
                      {STATUS_CHIP[t.status].label}
                    </span>
                  </td>
                  <td style={{ textAlign: 'right' }}>
                    {isAdmin && t.status !== 'revoked' && (
                      <button type="button" className="btn ghost sm" onClick={() => setPendingRevoke(t)}>
                        Revoke
                      </button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          {list.length === 0 && <Empty>No API tokens.</Empty>}
        </div>
      )}
      {creating && (
        <NewTokenDialog
          onCreated={() => tokens.reload()}
          onClose={() => setCreating(false)}
        />
      )}
      {pendingRevoke && (
        <ConfirmAdminDialog
          title={`Revoke ${pendingRevoke.name}?`} confirmLabel="Revoke token" danger busy={m.busy} error={m.error}
          onConfirm={() => void revoke(pendingRevoke)}
          onClose={() => {
            m.clear();
            setPendingRevoke(null);
          }}
        >
          <div className="banner prod" style={{ fontSize: 12 }}>
            Requests with this token fail at once. This cannot be undone: create a new token instead.
          </div>
        </ConfirmAdminDialog>
      )}
    </div>
  );
}

const COLUMNS: { key: PermissionColumn; label: string }[] = [
  { key: 'read', label: 'Read' },
  { key: 'create', label: 'Create' },
  { key: 'update', label: 'Update' },
  { key: 'delete', label: 'Delete' },
];

type ExpiryKey = '30' | '90' | '365' | 'never';
const expiryKey = (days: number | null): ExpiryKey => (days === null ? 'never' : (String(days) as ExpiryKey));

/** Name, expiry and the permission grid; then the value, shown once. */
function NewTokenDialog({ onCreated, onClose }: { onCreated: () => void; onClose: () => void }) {
  const formId = useId();
  const toast = useToast();
  const m = useAdminMutation();
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [expiry, setExpiry] = useState<ExpiryKey>('90');
  const [perms, setPerms] = useState<ApiPermission[]>([]);
  const [localErr, setLocalErr] = useState<Record<string, string>>({});
  const [created, setCreated] = useState<ApiTokenView | null>(null);
  const fields = { ...adminFieldErrors(m.error), ...localErr };

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    const errs: Record<string, string> = {};
    if (!name.trim()) errs.name = 'A name is required';
    if (perms.length === 0) errs.permissions = 'Choose at least one permission';
    setLocalErr(errs);
    if (Object.keys(errs).length) return;
    const days = EXPIRY_CHOICES.find((c) => expiryKey(c.days) === expiry)?.days ?? null;
    const r = await m.run(() => api.createApiToken({ name: name.trim(), description: description.trim(), permissions: perms, expiresAt: expiryFrom(days, Date.now()) }));
    if (r.ok) {
      setCreated(r.value);
      onCreated();
    }
  };

  const toggle = (p: ApiPermission, on: boolean) => {
    setLocalErr((x) => ({ ...x, permissions: '' }));
    setPerms((cur) => togglePermission(cur, p, on));
  };

  const copy = async (value: string, what: string) => {
    try {
      await navigator.clipboard.writeText(value);
      toast(`${what} copied`);
    } catch {
      toast('Copying failed: select the token and copy it by hand', 'err');
    }
  };

  if (created?.token) {
    const example = curlExample(created.permissions, window.location.origin);
    return (
      <Dialog
        title={`Token ${created.name} created`}
        width={560}
        closeOnBackdrop={false}
        onClose={onClose}
        footer={<button type="button" className="btn pri" onClick={onClose}>Done</button>}
      >
        <div className="col" style={{ gap: 12 }}>
          <div className="banner prod" role="note">
            Copy the token now. It is not stored and cannot be shown again.
          </div>
          <div className="row" style={{ gap: 8 }}>
            <input className="input mono grow" readOnly value={created.token} aria-label="Token" onFocus={(e) => e.target.select()} data-autofocus />
            <button type="button" className="btn" onClick={() => void copy(created.token!, 'Token')}>Copy</button>
          </div>
          <div className="f">
            <span className="row">
              Example
              <span className="hint">with <span className="mono">TIPHYS_TOKEN</span> set to the token above</span>
              <span className="grow" />
              <button type="button" className="btn ghost sm" onClick={() => void copy(example, 'Example')}>Copy</button>
            </span>
            <pre
              className="mono"
              style={{ margin: 0, padding: '10px 12px', background: 'var(--sf2)', border: '1px solid var(--ln)', borderRadius: 6,
                fontSize: 11.5, lineHeight: 1.5, whiteSpace: 'pre-wrap', wordBreak: 'break-all', color: 'var(--tx)', fontWeight: 400 }}
            >
              {example}
            </pre>
          </div>
          <span className="muted" style={{ fontSize: 12 }}>
            Send it as <span className="mono">Authorization: Bearer &lt;token&gt;</span> to the <span className="mono">/api/v1/admin</span> endpoints it may use.
          </span>
        </div>
      </Dialog>
    );
  }

  return (
    <Dialog
      title="New API token"
      subtitle="Tokens only change configuration. They never reach a cluster, and they cannot manage tokens."
      width={600}
      onClose={m.busy ? () => {} : onClose}
      footer={
        <>
          <button type="button" className="btn" onClick={onClose} disabled={m.busy}>
            Cancel
          </button>
          <button type="submit" form={formId} className="btn pri" disabled={m.busy}>
            {m.busy ? 'Creating…' : 'Create token'}
          </button>
        </>
      }
    >
      <form id={formId} className="col" style={{ gap: 14 }} onSubmit={(e) => void submit(e)}>
        <AdminErrorBanner error={Object.keys(adminFieldErrors(m.error)).length ? null : m.error} />
        <label className="f">
          Name
          <input
            className="input" value={name} maxLength={64} placeholder="e.g. holiday-sync" data-autofocus
            onChange={(e) => {
              setName(e.target.value);
              setLocalErr((x) => ({ ...x, name: '' }));
            }}
            aria-invalid={!!fields.name} aria-describedby={fields.name ? `${formId}-n` : undefined}
          />
          <FieldError id={`${formId}-n`} text={fields.name || undefined} />
        </label>
        <label className="f">
          Description <span className="hint">Optional: what uses it, who owns it</span>
          <input className="input" value={description} onChange={(e) => setDescription(e.target.value)} />
        </label>
        <div className="f">
          Expires after
          <Seg
            label="Expires after" style={{ alignSelf: 'flex-start' }} value={expiry} onChange={setExpiry}
            options={EXPIRY_CHOICES.map((c) => ({ value: expiryKey(c.days), label: c.label }))}
          />
          {expiry === 'never' && <span className="hint">A token that never expires works until it is revoked.</span>}
          <FieldError id={`${formId}-x`} text={fields.expiresAt || undefined} />
        </div>
        <div className="f">
          Permissions <span className="hint">A write includes reading what it changes.</span>
          <PermissionGrid perms={perms} onToggle={toggle} />
          <FieldError id={`${formId}-p`} text={fields.permissions || undefined} />
        </div>
      </form>
    </Dialog>
  );
}

function PermissionGrid({ perms, onToggle }: { perms: ApiPermission[]; onToggle: (p: ApiPermission, on: boolean) => void }) {
  return (
    <div className="tbl-wrap" style={{ border: '1px solid var(--ln)', borderRadius: 6 }}>
      <table className="tbl" style={{ minWidth: 0, fontWeight: 400, color: 'var(--tx)' }}>
        <thead>
          <tr>
            <th>Resource</th>
            {COLUMNS.map((c) => (
              <th key={c.key} style={{ textAlign: 'center' }}>{c.label}</th>
            ))}
          </tr>
        </thead>
        <tbody>
          {PERMISSION_ROWS.map((r) => (
            <tr key={r.label}>
              <td>{r.label}</td>
              {COLUMNS.map((c) => {
                const p = r.cells[c.key];
                return (
                  <td key={c.key} style={{ textAlign: 'center' }}>
                    {p ? (
                      <input type="checkbox" checked={perms.includes(p)} aria-label={`${r.label}: ${c.label.toLowerCase()}`} title={p}
                        onChange={(e) => onToggle(p, e.target.checked)} />
                    ) : (
                      <span className="muted" aria-hidden>—</span>
                    )}
                  </td>
                );
              })}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
