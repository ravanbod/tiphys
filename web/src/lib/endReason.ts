import type { EndReason } from '../api/types';

export interface EndInfo {
  title: string;
  detail: string;
  tone: 'neutral' | 'warn' | 'err';
  chip: string;
  canReconnect: boolean;
  suggestBreakGlass: boolean;
}

export function describeEnd(reason: EndReason | string, message?: string, limits?: { maxMinutes?: number; idleMinutes?: number }): EndInfo {
  const mk = (
    title: string, detail: string, tone: EndInfo['tone'], chip: string,
    canReconnect = true, suggestBreakGlass = false,
  ): EndInfo => ({ title, detail: message || detail, tone, chip, canReconnect, suggestBreakGlass });
  switch (reason) {
    case 'client_closed':
      return mk('You disconnected', 'The session was closed from this browser.', 'neutral', 'You disconnected');
    case 'process_exited':
      return mk('Shell exited', 'The shell process in the container ended.', 'neutral', 'Shell exited');
    case 'idle_timeout':
      return mk(
        'Closed after being idle',
        `Sessions close after ${limits?.idleMinutes ?? 30} minutes without input.`, 'warn', 'Idle timeout',
      );
    case 'max_duration':
      return mk(
        'Maximum session length reached',
        `Exec sessions are limited to ${limits?.maxMinutes ?? 120} minutes. Everything you ran was recorded.`,
        'warn', 'Max duration reached',
      );
    case 'outside_window':
      return mk('Time window closed', 'Access to this environment closed. Use break-glass if this is urgent.', 'warn', 'Time window closed', false, true);
    case 'holiday':
      return mk('Closed for a holiday', 'Access to this environment is closed today.', 'warn', 'Holiday', false, true);
    case 'grant_revoked':
      return mk('Grant revoked', 'Your access to this workload was removed.', 'err', 'Grant revoked', false);
    case 'grant_expired':
      return mk('Grant expired', 'Your access to this workload has expired.', 'err', 'Grant expired', false);
    case 'user_disabled':
      return mk('Account disabled', 'Your account was disabled by an administrator.', 'err', 'Account disabled', false);
    case 'role_changed':
      return mk('Role changed', 'Your role changed, so this session was closed.', 'err', 'Role changed', false);
    case 'terminated_by_admin':
      return mk('Ended by the platform team', 'An administrator ended this session.', 'err', 'Ended by admin', false);
    case 'break_glass_ended':
      return mk('Break-glass ended', 'Your break-glass period ended, so the time window applies again.', 'warn', 'Break-glass ended', false);
    case 'recording_failed':
      return mk('Recording failed', 'The session recording could not be written, so the session was stopped.', 'err', 'Recording failed', true);
    case 'cluster_error':
      return mk('Cluster connection lost', 'The portal lost its connection to the cluster or the pod went away.', 'err', 'Cluster error');
    case 'portal_restart':
      return mk('Portal restarted', 'The portal restarted. Reconnect to continue.', 'warn', 'Portal restarted');
    case 'denied':
      return mk('Access denied', 'The portal refused this session.', 'err', 'Denied', false);
    default:
      return mk('Session closed', 'The session ended.', 'neutral', String(reason));
  }
}

/** Plain label for session tables: reason -> chip text + tone. */
export function endChip(reason: EndReason | null): { text: string; tone: EndInfo['tone'] } {
  if (!reason) return { text: '—', tone: 'neutral' };
  const e = describeEnd(reason);
  return { text: e.chip, tone: e.tone };
}
