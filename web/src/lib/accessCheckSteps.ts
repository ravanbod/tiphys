/**
 * Turns the API's `DecisionExplain` into the checklist shown by the admin "Access checker".
 * Pure, so it can be tested. It only restates what the server decided; the optional rule list is used to NAME the
 * time-window rules that are likely responsible (best effort, never to change the verdict).
 */
import type { DecisionExplain, GrantView, HolidayView, TimeWindowView } from '../api/types';
import { fmtReopens } from './time';
import { buildModel, stateAt } from './windowPreview';

export type StepTone = 'ok' | 'err' | 'info' | 'none' | 'skip';

export interface CheckStep {
  id: 'user' | 'grant' | 'namespace' | 'environment' | 'holiday' | 'window' | 'breakglass';
  tone: StepTone;
  title: string;
  detail: string;
  /** Grants listed under the step (matching grants). */
  grants?: GrantView[];
}

export interface Verdict {
  allowed: boolean;
  /** Chip text: ALLOWED, DENIED NOW (time based) or DENIED. */
  chip: string;
  /** Extra sentence, e.g. "Would be allowed with break-glass." */
  text: string;
  timeBased: boolean;
  breakGlassAvailable: boolean;
}

const BEFORE_GRANTS = ['protected_namespace', 'user_disabled', 'role'];
const NO_GRANT = ['no_grant', 'grant_expired', 'argo_stale'];

export function isTimeDenied(d: Pick<DecisionExplain, 'allowed' | 'reason'>): boolean {
  return !d.allowed && (d.reason === 'outside_window' || d.reason === 'holiday');
}

export function breakGlassAvailable(d: DecisionExplain): boolean {
  return isTimeDenied(d) && d.userRole === 'user' && d.matchedGrants.length > 0;
}

export function verdict(d: DecisionExplain, now: number, tz?: string): Verdict {
  const bg = breakGlassAvailable(d);
  if (d.allowed) {
    let text = 'Allowed now.';
    if (d.reason === 'admin') text = 'Administrators have implicit access to every non-protected scope.';
    else if (d.breakGlassId !== null) text = `Allowed through break-glass #${d.breakGlassId}, which overrides time windows and holidays.`;
    else if (d.closesAt) text = `Allowed now; closes ${fmtReopens(d.closesAt, now, tz)}.`;
    return { allowed: true, chip: 'ALLOWED', text, timeBased: false, breakGlassAvailable: false };
  }
  const time = isTimeDenied(d);
  const base: Record<string, string> = {
    protected_namespace: 'This namespace is protected and not available through the portal.',
    user_disabled: 'The user is disabled.',
    role: 'The role of this user does not allow workload actions.',
    no_grant: 'No grant gives this user that action here.',
    grant_expired: 'Only expired grants cover this target.',
    outside_window: 'Closed by a time window.',
    holiday: 'Closed today: holiday.',
    argo_stale: 'Project-scoped grants are paused: Argo CD data is out of date.',
  };
  const reopen = d.reopensAt ? ` Reopens ${fmtReopens(d.reopensAt, now, tz)}.` : '';
  return {
    allowed: false,
    chip: time ? 'DENIED NOW' : 'DENIED',
    text: `${base[d.reason] ?? 'Not allowed.'}${time ? reopen : ''}${bg ? ' Would be allowed with break-glass.' : ''}`,
    timeBased: time,
    breakGlassAvailable: bg,
  };
}

/** Names of the time-window rules (and holiday) that are active at the evaluation instant. Best effort. */
export function windowCulprits(
  d: DecisionExplain,
  rules: TimeWindowView[] | null | undefined,
  holidays: HolidayView[] | null | undefined,
  clusterEnv: Record<string, string> = {},
): { deny: string[]; allow: string[]; holiday: string | null } {
  const none = { deny: [] as string[], allow: [] as string[], holiday: null };
  if (!rules) return none;
  const t = Date.parse(d.evaluatedAt);
  if (Number.isNaN(t)) return none;
  // Team rules can be resolved from the user's teams; Argo-project rules cannot, so they are left out.
  const usable: TimeWindowView[] = [];
  for (const r of rules) {
    if (r.scopeKind === 'team') {
      if (r.scopeValue && d.teams.includes(r.scopeValue)) usable.push({ ...r, scopeKind: 'global', scopeValue: null });
    } else if (r.scopeKind !== 'argo_project') usable.push(r);
  }
  const m = buildModel(
    { rules: usable, holidays: holidays ?? [], environment: d.environment, action: d.action, clusterEnv },
    t - 86400_000,
    t + 86400_000,
  );
  const s = stateAt(m, t);
  const allow = Array.from(new Set(m.allow.map((iv) => iv.rule.name)));
  return { deny: s.state === 'deny' ? s.rules : [], allow: s.state === 'closed' ? allow : [], holiday: s.holiday };
}

function grantLine(g: GrantView): string {
  return `${g.holder.kind === 'team' ? 'Team' : 'User'} ${g.holder.name} · ${g.actions.join(', ')}${g.expiresAt ? ' · expires' : ''}`;
}

export function buildSteps(
  d: DecisionExplain,
  now: number,
  opts: { tz?: string; rules?: TimeWindowView[] | null; holidays?: HolidayView[] | null; clusterEnv?: Record<string, string> } = {},
): CheckStep[] {
  const { reason } = d;
  const steps: CheckStep[] = [];
  const blockedEarly = BEFORE_GRANTS.includes(reason);
  const admin = reason === 'admin';
  const bgOverride = d.allowed && d.breakGlassId !== null;

  // User and role
  if (reason === 'protected_namespace') {
    steps.push({ id: 'user', tone: 'skip', title: 'User and role', detail: 'Not evaluated' });
  } else if (reason === 'user_disabled') {
    steps.push({ id: 'user', tone: 'err', title: 'User disabled', detail: 'A disabled user cannot do anything.' });
  } else if (reason === 'role') {
    steps.push({ id: 'user', tone: 'err', title: 'Role does not allow it', detail: `Role ${d.userRole}: auditors cannot use workloads.` });
  } else {
    steps.push({
      id: 'user', tone: 'ok', title: 'User and role',
      detail: `Role ${d.userRole}${d.teams.length ? ` · teams ${d.teams.join(', ')}` : ' · no teams'}`,
    });
  }

  // Namespace
  steps.push(
    reason === 'protected_namespace'
      ? { id: 'namespace', tone: 'err', title: 'Namespace is protected', detail: `${d.namespace} is not available through the portal, for any role.` }
      : { id: 'namespace', tone: 'ok', title: 'Namespace not protected', detail: `${d.namespace} is allowed` },
  );

  // Grants
  if (admin) {
    steps.push({ id: 'grant', tone: 'none', title: 'Administrator', detail: 'Implicit access, no grant needed.' });
  } else if (blockedEarly) {
    steps.push({ id: 'grant', tone: 'skip', title: 'Matching grant', detail: 'Not evaluated' });
  } else if (NO_GRANT.includes(reason)) {
    const detail =
      reason === 'no_grant'
        ? `No grant covers ${d.action} on this target for this user or their teams.`
        : reason === 'grant_expired'
          ? 'Only expired grants cover this target.'
          : 'Argo CD data is older than 15 minutes, so project-scoped grants do not match.';
    steps.push({ id: 'grant', tone: 'err', title: reason === 'no_grant' ? 'No matching grant' : reason === 'grant_expired' ? 'Grant expired' : 'Argo data out of date', detail, grants: d.matchedGrants });
  } else {
    steps.push({
      id: 'grant', tone: 'ok',
      title: d.matchedGrants.length > 1 ? 'Matching grants' : 'Matching grant',
      detail: d.matchedGrants.map(grantLine).join(' | '),
      grants: d.matchedGrants,
    });
  }

  // Environment
  steps.push({
    id: 'environment', tone: 'none', title: 'Environment',
    detail: `${d.environment || 'unlabeled'} · ${admin ? 'administrators are not limited by time windows' : 'time windows apply'}`,
  });

  // Holiday and window
  const skipTime = blockedEarly || NO_GRANT.includes(reason) || admin;
  const culprits = reason === 'outside_window' || reason === 'holiday' ? windowCulprits(d, opts.rules, opts.holidays, opts.clusterEnv) : null;
  const reopens = d.reopensAt ? ` Reopens ${fmtReopens(d.reopensAt, now, opts.tz)}.` : '';
  if (skipTime) {
    const detail = admin ? 'Not applicable to administrators' : 'Not evaluated';
    steps.push({ id: 'holiday', tone: 'skip', title: 'Holiday', detail });
    steps.push({ id: 'window', tone: 'skip', title: 'Time window', detail });
  } else if (bgOverride) {
    const detail = 'Break-glass overrides holidays and time windows.';
    steps.push({ id: 'holiday', tone: 'none', title: 'Holiday', detail });
    steps.push({ id: 'window', tone: 'none', title: 'Time window', detail });
  } else {
    steps.push(
      reason === 'holiday'
        ? {
            id: 'holiday', tone: 'err', title: 'Holiday — closed',
            detail: `${culprits?.holiday ? `“${culprits.holiday}”. ` : ''}${d.environment || 'This environment'} is closed today.${reopens}`,
          }
        : { id: 'holiday', tone: 'ok', title: 'Holiday', detail: 'Not a holiday today' },
    );
    if (reason === 'holiday') {
      steps.push({ id: 'window', tone: 'skip', title: 'Time window', detail: 'Not evaluated' });
    } else if (reason === 'outside_window') {
      let why = `${d.action} in ${d.environment || 'this environment'} is closed.`;
      if (culprits && culprits.deny.length) why = `Rule ${culprits.deny.map((n) => `“${n}”`).join(', ')} denies ${d.action} right now.`;
      else if (culprits && culprits.allow.length) why = `${d.action} is open only inside ${culprits.allow.map((n) => `“${n}”`).join(', ')}, and now is outside.`;
      steps.push({ id: 'window', tone: 'err', title: 'Time window — closed', detail: `${why}${reopens}` });
    } else {
      steps.push({
        id: 'window', tone: 'ok', title: 'Time window — open',
        detail: d.closesAt ? `Open until ${fmtReopens(d.closesAt, now, opts.tz)}.` : 'No window or deny rule closes this action right now.',
      });
    }
  }

  // Break-glass
  if (bgOverride) {
    steps.push({ id: 'breakglass', tone: 'info', title: 'Break-glass', detail: `Active: #${d.breakGlassId} overrides the closed window or holiday.` });
  } else if (breakGlassAvailable(d)) {
    steps.push({
      id: 'breakglass', tone: 'info', title: 'Break-glass',
      detail: `Available: the user has a grant for this scope and the block is only a ${reason === 'holiday' ? 'holiday' : 'time window'}.`,
    });
  }
  return steps;
}
