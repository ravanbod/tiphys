import type { Action, GrantInput, GrantView, ScopeView, WorkloadKind } from '../api/types';
import { ADMIN_TZ, isoToLocalInput, localInputToIso } from './adminDatetime';
import { ARGO_UI } from './features';

export interface GrantForm {
  id: number | null;
  holderKind: 'team' | 'user';
  holderId: number | null;
  scopeKind: ScopeView['kind'];
  argoProject: string;
  cluster: string;
  namespace: string;
  /** `Kind/name` keys. */
  workloads: string[];
  actions: Action[];
  expiryMode: 'none' | 'date';
  /** `datetime-local` value in ADMIN_TZ. */
  expiry: string;
  note: string;
}

/** Sync acts on whole Argo CD applications, which a workloads scope cannot cover. */
export const SYNC_SCOPE_HINT = 'Sync needs a namespace or Argo project scope: it applies to whole Argo CD applications.';

export const ACTIONS: Action[] = ['logs', 'exec', 'restart', 'run-job', 'sync'];

/** How an action is named on the grant, request and time window checkboxes. */
export function actionOptionLabel(a: Action): string {
  return a === 'sync' ? 'ArgoCD Sync' : a;
}

export function emptyGrantForm(): GrantForm {
  return {
    id: null, holderKind: 'team', holderId: null, scopeKind: ARGO_UI ? 'argo_project' : 'namespace', argoProject: '', cluster: '', namespace: '',
    workloads: [], actions: ['logs'], expiryMode: 'none', expiry: '', note: '',
  };
}

export const wlKey = (w: { kind: string; name: string }): string => `${w.kind}/${w.name}`;
export function parseWlKey(k: string): { kind: WorkloadKind; name: string } {
  const i = k.indexOf('/');
  return { kind: k.slice(0, i) as WorkloadKind, name: k.slice(i + 1) };
}

export function formFromGrant(g: GrantView): GrantForm {
  return {
    id: g.id,
    holderKind: g.holder.kind,
    holderId: g.holder.id,
    scopeKind: g.scope.kind,
    argoProject: g.scope.argoProject ?? '',
    cluster: g.scope.cluster ?? '',
    namespace: g.scope.namespace ?? '',
    workloads: (g.scope.workloads ?? []).map(wlKey),
    actions: g.actions,
    expiryMode: g.expiresAt ? 'date' : 'none',
    expiry: isoToLocalInput(g.expiresAt, ADMIN_TZ),
    note: g.note,
  };
}

/** Local validation; keys mirror the server's `details.fields` names. */
export function validateGrantForm(f: GrantForm, now: number): Record<string, string> {
  const e: Record<string, string> = {};
  if (f.holderId === null) e.holder = `Choose a ${f.holderKind}.`;
  if (f.scopeKind === 'argo_project') {
    if (!f.argoProject) e.argoProject = 'Choose an Argo project.';
  } else {
    if (!f.cluster) e.cluster = 'Choose a cluster.';
    if (!f.namespace) e.namespace = 'Choose a namespace.';
    if (f.scopeKind === 'workloads' && f.workloads.length === 0) e.workloads = 'Select at least one workload.';
  }
  if (f.actions.length === 0) e.actions = 'Select at least one action.';
  else if (f.scopeKind === 'workloads' && f.actions.includes('sync')) e.actions = SYNC_SCOPE_HINT;
  if (f.expiryMode === 'date') {
    const iso = localInputToIso(f.expiry);
    if (!iso) e.expiresAt = 'Enter a valid date and time.';
    else if (Date.parse(iso) <= now) e.expiresAt = 'Expiry must be in the future.';
  }
  return e;
}

/** Assumes `validateGrantForm` passed. */
export function grantInputFromForm(f: GrantForm): GrantInput {
  const scope: ScopeView =
    f.scopeKind === 'argo_project'
      ? { kind: 'argo_project', argoProject: f.argoProject }
      : f.scopeKind === 'namespace'
        ? { kind: 'namespace', cluster: f.cluster, namespace: f.namespace }
        : { kind: 'workloads', cluster: f.cluster, namespace: f.namespace, workloads: f.workloads.map(parseWlKey) };
  return {
    holder: { kind: f.holderKind, id: f.holderId as number },
    scope,
    actions: ACTIONS.filter((a) => f.actions.includes(a)),
    expiresAt: f.expiryMode === 'date' ? localInputToIso(f.expiry) : null,
    note: f.note.trim(),
  };
}

const VERB: Record<Action, string> = { logs: 'read logs', exec: 'exec', restart: 'restart', 'run-job': 'run jobs', sync: 'sync Argo CD apps' };

/** "read logs, exec and restart" */
export function actionPhrase(actions: Action[]): string {
  const v = ACTIONS.filter((a) => actions.includes(a)).map((a) => VERB[a]);
  if (v.length === 0) return '…';
  if (v.length === 1) return v[0];
  return `${v.slice(0, -1).join(', ')} and ${v[v.length - 1]}`;
}
