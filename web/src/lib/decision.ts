import type { Action, DecisionInfo, DenyReason } from '../api/types';
import { fmtReopens, fmtSpan, msUntil } from './time';

export type DecisionCta = 'request' | 'breakglass' | 'none';

export interface Explanation {
  /** Short sentence for tooltips and inline hints. */
  text: string;
  cta: DecisionCta;
  reason: DecisionInfo['reason'];
}

export interface ExplainContext {
  action: Action;
  /** Workload or pod name used in the message. */
  target?: string;
  now?: number;
  tz?: string;
  /** Whether the viewer may use break-glass (role `user`). */
  canBreakGlass?: boolean;
}

export const ACTION_LABEL: Record<Action, string> = {
  logs: 'Logs',
  exec: 'Exec',
  restart: 'Restart',
  'run-job': 'Run now',
  sync: 'Sync',
};

const ACTION_NOUN: Record<Action, string> = { logs: 'logs', exec: 'exec', restart: 'restart', 'run-job': 'run', sync: 'sync' };

export const REASON_TEXT: Record<DenyReason, string> = {
  protected_namespace: 'This namespace is not available through the portal.',
  user_disabled: 'Your account is disabled. Ask an administrator.',
  role: 'Your role does not allow this action.',
  no_grant: 'No grant covers this action.',
  grant_expired: 'Your access has expired.',
  outside_window: 'Closed by a time window.',
  holiday: 'Closed today: holiday.',
  argo_stale: 'Project-scoped access is paused: Argo CD data is out of date.',
  app_scope: 'This application manages cluster-wide resources, so only an admin can sync it.',
};

/** Human explanation of a denied DecisionInfo. Returns null if the action is allowed. */
export function explainDecision(info: DecisionInfo | undefined | null, ctx: ExplainContext): Explanation | null {
  if (!info || info.allowed) return null;
  const now = ctx.now ?? Date.now();
  const noun = ACTION_NOUN[ctx.action];
  const reopens = info.reopensAt ? fmtReopens(info.reopensAt, now, ctx.tz) : '';
  const bg: DecisionCta = ctx.canBreakGlass === false ? 'none' : 'breakglass';
  const mk = (text: string, cta: DecisionCta): Explanation => ({ text, cta, reason: info.reason });
  switch (info.reason) {
    case 'no_grant':
      return mk(`You don't have ${noun} access${ctx.target ? ` to ${ctx.target}` : ''}.`, 'request');
    case 'grant_expired':
      return mk('Your access has expired.', 'request');
    case 'outside_window': {
      const env = info.environment ? ` in ${info.environment}` : '';
      const cap = noun.charAt(0).toUpperCase() + noun.slice(1);
      return mk(`${cap}${env} is closed${reopens ? ` until ${reopens}` : ''}.`, bg);
    }
    case 'holiday':
      return mk(`Closed today: holiday.${reopens ? ` Reopens ${reopens}.` : ''}`, bg);
    case 'protected_namespace':
    case 'user_disabled':
    case 'role':
    case 'argo_stale':
    case 'app_scope':
      return mk(REASON_TEXT[info.reason], 'none');
    default:
      return mk('Not available right now.', 'none');
  }
}

/** Short "Reopens Sat 09:00" label for a chip, or '' if not time-based. */
export function reopensLabel(info: DecisionInfo | undefined | null, now: number, tz?: string): string {
  if (!info || info.allowed || !info.reopensAt) return '';
  const t = fmtReopens(info.reopensAt, now, tz);
  return t ? `Reopens ${t}` : '';
}

/** Seconds until access closes (earliest of window close / holiday / grant expiry / break-glass end), or null. */
export function closesInSeconds(info: DecisionInfo | undefined | null, now: number): number | null {
  if (!info || !info.allowed) return null;
  const ms = msUntil(info.closesAt, now);
  return ms === null ? null : Math.max(0, Math.round(ms / 1000));
}

/** "Closes in 42 min" when access closes within `withinSeconds`, else ''. */
export function closesSoonLabel(info: DecisionInfo | undefined | null, now: number, withinSeconds = 3 * 3600): string {
  const s = closesInSeconds(info, now);
  if (s === null || s > withinSeconds) return '';
  return `Closes in ${fmtSpan(s)}`;
}

/** A grant gives the action and at most time (a window or a holiday) stops it now. */
export function grantedOrClosed(info: DecisionInfo | undefined | null): boolean {
  return !!info && (info.allowed || info.reason === 'outside_window' || info.reason === 'holiday');
}

/** Access-checker verdict wording from an explain result. */
export function reasonTitle(reason: DecisionInfo['reason']): string {
  switch (reason) {
    case 'allowed':
      return 'Allowed';
    case 'admin':
      return 'Allowed (administrator)';
    default:
      return REASON_TEXT[reason];
  }
}
