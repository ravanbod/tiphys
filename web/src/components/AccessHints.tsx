import type { Action, DecisionInfo } from '../api';
import { useAuth } from '../context/auth';
import { useBreakGlass } from '../context/breakglass';
import { explainDecision } from '../lib/decision';
import { Icon } from './Icon';
import { Link } from 'react-router-dom';
import { requestUrl } from '../lib/requestLink';
import type { RequestPrefill } from '../lib/requestLink';
import { breakGlassOn, breakGlassLabel } from '../lib/breakGlass';

const LABEL: Record<Action, string> = { logs: 'Logs', exec: 'Exec', restart: 'Restart', 'run-job': 'Run', sync: 'Sync', rollback: 'Rollback' };

/** Visible reasons for every denied action on a workload (so disabled buttons are never a mystery). */
export function AccessHints({ access, target, actions, request }: {
  access: Partial<Record<Action, DecisionInfo>>; target: string; actions: Action[];
  /** Where these actions are, so "Request access" opens the form filled in. */
  request?: Omit<RequestPrefill, 'action'>;
}) {
  const { usesGrants, me } = useAuth();
  const bg = useBreakGlass();
  const rows = actions
    .map((a) => ({ a, e: explainDecision(access[a], { action: a, target, canBreakGlass: usesGrants && breakGlassOn(me) }) }))
    .filter((x) => x.e);
  if (rows.length === 0) return null;
  return (
    <ul className="hints" aria-label="Unavailable actions">
      {rows.map(({ a, e }) => (
        <li key={a}>
          <Icon name="lock" size={13} />
          <b>{LABEL[a]}:</b> <span>{e!.text}</span>
          {e!.cta === 'request' && <Link to={requestUrl(request && { ...request, action: a })}>Request access</Link>}
          {e!.cta === 'breakglass' && (
            <button type="button" className="linklike blue" onClick={() => bg.open({ action: a, target, environment: access[a]?.environment ?? '' })}>{breakGlassLabel(me)}</button>
          )}
        </li>
      ))}
    </ul>
  );
}
