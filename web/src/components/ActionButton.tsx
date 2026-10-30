import { useId } from 'react';
import { Link } from 'react-router-dom';
import type { Action, DecisionInfo } from '../api/types';
import { useAuth } from '../context/auth';
import { useBreakGlass } from '../context/breakglass';
import { explainDecision } from '../lib/decision';
import { Icon } from './Icon';
import type { IconName } from './Icon';

export interface ActionButtonProps {
  action: Action;
  decision: DecisionInfo | undefined;
  label: string;
  icon: IconName;
  /** Workload or pod name for messages. */
  target?: string;
  to?: string;
  onClick?: () => void;
  size?: 'sm' | 'md';
  /** Plain (non-write) style, e.g. Logs. Write actions default to `.btn.act`. */
  plain?: boolean;
  /** tip = popover on hover/focus, inline = text under the button, none = title only. */
  hint?: 'tip' | 'inline' | 'none';
  /** Extra reason to disable, regardless of the decision (e.g. cluster unreachable). */
  forceDisabled?: string;
}

/** A button gated by a DecisionInfo: enabled, or `.btn.dis` with the reason from the API. */
export function ActionButton({
  action, decision, label, icon, target, to, onClick, size = 'md', plain, hint = 'tip', forceDisabled,
}: ActionButtonProps) {
  const { role, me } = useAuth();
  const bg = useBreakGlass();
  const tipId = useId();
  const sm = size === 'sm' ? ' sm' : '';
  const exp = explainDecision(decision, { action, target, canBreakGlass: role === 'user' && me.settings.breakGlassEnabled });
  const disabledText = forceDisabled ?? exp?.text;

  if (!disabledText) {
    const cls = `btn${plain || action === 'logs' ? '' : ' act'}${sm}`;
    const body = (<><Icon name={icon} />{label}</>);
    return to ? (
      <Link className={cls} to={to}>{body}</Link>
    ) : (
      <button type="button" className={cls} onClick={onClick}>{body}</button>
    );
  }

  const cta = forceDisabled ? 'none' : exp?.cta;
  const reasonLine = (
    <>
      {disabledText}
      {cta === 'request' && (
        <div style={{ marginTop: 6 }}>
          <Link to="/access?request=1">Request access</Link>
        </div>
      )}
      {cta === 'breakglass' && (
        <div style={{ marginTop: 6 }}>
          <button type="button" className="linklike" onClick={() => bg.open({ action, target, environment: decision?.environment })}>
            Use break-glass
          </button>
        </div>
      )}
    </>
  );

  return (
    <span className={`tipwrap${hint === 'inline' ? ' inl' : ''}`}>
      <span
        className={`btn dis${sm}`}
        role="button"
        tabIndex={0}
        aria-disabled="true"
        aria-describedby={hint === 'tip' ? tipId : undefined}
        title={hint === 'none' ? disabledText : undefined}
      >
        <Icon name={icon} />
        {label}
      </span>
      {hint === 'tip' && (
        <span className="tip pop" id={tipId} role="tooltip">
          {reasonLine}
        </span>
      )}
      {hint === 'inline' && <span className="muted hintline">{reasonLine}</span>}
    </span>
  );
}
