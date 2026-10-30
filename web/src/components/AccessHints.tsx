import type { Action, DecisionInfo } from '../api';
import { useAuth } from '../context/auth';
import { useBreakGlass } from '../context/breakglass';
import { explainDecision } from '../lib/decision';
import { Icon } from './Icon';
import { Link } from 'react-router-dom';

const LABEL: Record<Action, string> = { logs: 'Logs', exec: 'Exec', restart: 'Restart', 'run-job': 'Run', sync: 'Sync' };

/** Visible reasons for every denied action on a workload (so disabled buttons are never a mystery). */
export function AccessHints({ access, target, actions }: { access: Partial<Record<Action, DecisionInfo>>; target: string; actions: Action[] }) {
  const { role, me } = useAuth();
  const bg = useBreakGlass();
  const rows = actions
    .map((a) => ({ a, e: explainDecision(access[a], { action: a, target, canBreakGlass: role === 'user' && me.settings.breakGlassEnabled }) }))
    .filter((x) => x.e);
  if (rows.length === 0) return null;
  return (
    <ul className="hints" aria-label="Unavailable actions">
      {rows.map(({ a, e }) => (
        <li key={a}>
          <Icon name="lock" size={13} />
          <b>{LABEL[a]}:</b> <span>{e!.text}</span>
          {e!.cta === 'request' && <Link to="/access?request=1">Request access</Link>}
          {e!.cta === 'breakglass' && (
            <button type="button" className="linklike blue" onClick={() => bg.open({ action: a, target, environment: access[a]?.environment ?? '' })}>Use break-glass</button>
          )}
        </li>
      ))}
    </ul>
  );
}
