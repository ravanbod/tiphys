import type { ArgoPlanResource, ArgoResourceRef, ArgoSyncPlan, SyncAppInput } from '../api';

/** The sync options of Argo CD's sync dialog, with its labels (the server accepts these and the app's own). */
export const SYNC_OPTIONS: { value: string; label: string; hint?: string }[] = [
  { value: 'Validate=false', label: 'Skip schema validation' },
  { value: 'CreateNamespace=true', label: 'Auto-create namespace' },
  { value: 'PruneLast=true', label: 'Prune last', hint: 'prunes after everything else applied' },
  { value: 'ApplyOutOfSyncOnly=true', label: 'Apply out of sync only' },
  { value: 'RespectIgnoreDifferences=true', label: 'Respect ignore differences' },
  { value: 'ServerSideApply=true', label: 'Server-side apply' },
  { value: 'Replace=true', label: 'Replace', hint: 'kubectl replace/create: may delete and recreate resources' },
  { value: 'FailOnSharedResource=true', label: 'Fail on shared resource' },
];

export const PRUNE_POLICIES = ['foreground', 'background', 'orphan'] as const;
export type PrunePolicy = (typeof PRUNE_POLICIES)[number];
const POLICY = 'PrunePropagationPolicy=';

/** Argo CD's own defaults for a retry the user turns on. */
const RETRY_DEFAULTS = { limit: 2, duration: '5s', factor: 2, maxDuration: '3m' };

export const refKey = (r: ArgoResourceRef) => `${r.group}/${r.kind}/${r.namespace}/${r.name}`;

export interface SyncForm {
  prune: boolean;
  dryRun: boolean;
  applyOnly: boolean;
  force: boolean;
  /** The sync options as Argo CD writes them, the prune propagation policy included. */
  options: string[];
  retry: { on: boolean; limit: number; duration: string; factor: number; maxDuration: string };
  /** The retry is sent only once changed, so the app's own applies otherwise. */
  retryTouched: boolean;
  /** Keys (refKey) of the resources to sync; null for all. */
  selected: string[] | null;
}

/** The form a sync starts from: the app's own sync options and retry. */
export function initialForm(plan: ArgoSyncPlan): SyncForm {
  const r = plan.defaults.retry;
  return {
    prune: false, dryRun: false, applyOnly: false, force: false,
    options: [...plan.defaults.syncOptions],
    retry: r && r.limit !== 0
      ? { on: true, limit: r.limit, duration: r.backoff?.duration || RETRY_DEFAULTS.duration, factor: r.backoff?.factor || RETRY_DEFAULTS.factor, maxDuration: r.backoff?.maxDuration || RETRY_DEFAULTS.maxDuration }
      : { on: false, ...RETRY_DEFAULTS },
    retryTouched: false,
    selected: null,
  };
}

export function toggleOption(options: string[], value: string): string[] {
  return options.includes(value) ? options.filter((o) => o !== value) : [...options, value];
}

export function prunePolicy(options: string[]): PrunePolicy | '' {
  const o = options.find((x) => x.startsWith(POLICY));
  return o ? (o.slice(POLICY.length) as PrunePolicy) : '';
}

export function withPrunePolicy(options: string[], policy: PrunePolicy | ''): string[] {
  const rest = options.filter((x) => !x.startsWith(POLICY));
  return policy ? [...rest, POLICY + policy] : rest;
}

/** The app's own options the dialog has no checkbox for; they are passed on as they are. */
export function otherOptions(options: string[]): string[] {
  return options.filter((o) => !o.startsWith(POLICY) && !SYNC_OPTIONS.some((x) => x.value === o));
}

const sameSet = (a: string[], b: string[]) => a.length === b.length && a.every((x) => b.includes(x));

function isSelected(form: SyncForm, r: ArgoPlanResource): boolean {
  return form.selected === null || form.selected.includes(refKey(r));
}

/** The request body: only what differs from a plain sync of the target revision with the app's own settings. */
export function syncInput(plan: ArgoSyncPlan, form: SyncForm): SyncAppInput {
  const out: SyncAppInput = {};
  if (plan.revision) out.revision = plan.revision;
  if (form.prune) out.prune = true;
  if (form.dryRun) out.dryRun = true;
  if (form.applyOnly) out.applyOnly = true;
  if (form.force) out.force = true;
  if (!sameSet(form.options, plan.defaults.syncOptions)) out.syncOptions = form.options;
  if (form.retryTouched) {
    const r = form.retry;
    out.retry = r.on
      ? { limit: r.limit, backoff: { duration: r.duration, factor: r.factor, maxDuration: r.maxDuration } }
      : { limit: 0, backoff: null };
  }
  if (form.selected !== null) {
    out.resources = plan.resources.filter((r) => isSelected(form, r)).map(({ group, kind, namespace, name }) => ({ group, kind, namespace, name }));
  }
  return out;
}

/** The selected resources a sync with this form deletes. */
export function toPrune(plan: ArgoSyncPlan, form: SyncForm): ArgoPlanResource[] {
  if (!form.prune) return [];
  return plan.resources.filter((r) => r.state === 'extra' && r.prunable && isSelected(form, r));
}

export interface PlanCounts { create: number; change: number; prune: number; leave: number; unchanged: number }

/** What a sync with this form does to the selected resources. */
export function planCounts(plan: ArgoSyncPlan, form: SyncForm): PlanCounts {
  const c: PlanCounts = { create: 0, change: 0, prune: 0, leave: 0, unchanged: 0 };
  for (const r of plan.resources) {
    if (!isSelected(form, r)) continue;
    if (r.state === 'missing') c.create++;
    else if (r.state === 'changed') c.change++;
    else if (r.state === 'extra') {
      if (form.prune && r.prunable) c.prune++;
      else c.leave++;
    } else c.unchanged++;
  }
  return c;
}

/** "2 changed · 1 created · 3 pruned"; parts that are zero are left out. */
export function countsText(c: PlanCounts, otherRevision: boolean): string {
  const parts = [
    c.change && `${c.change} changed`,
    c.create && `${c.create} created`,
    c.unchanged && (otherRevision ? `${c.unchanged} applied` : `${c.unchanged} in sync`),
    c.prune && `${c.prune} pruned`,
    c.leave && `${c.leave} left in the cluster`,
  ].filter(Boolean);
  return parts.length ? parts.join(' · ') : 'Nothing selected';
}

export function retryValid(r: SyncForm['retry']): boolean {
  if (!r.on) return true;
  const dur = /^\d+(ms|s|m|h)$/;
  return Number.isInteger(r.limit) && r.limit >= 1 && r.limit <= 10 && Number.isInteger(r.factor) && r.factor >= 1 && r.factor <= 10 &&
    dur.test(r.duration) && dur.test(r.maxDuration);
}
