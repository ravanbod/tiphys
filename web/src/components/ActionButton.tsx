import { useEffect, useId, useRef, useState } from 'react';
import type { CSSProperties } from 'react';
import { Link } from 'react-router-dom';
import type { Action, DecisionInfo } from '../api/types';
import { useAuth } from '../context/auth';
import { useBreakGlass } from '../context/breakglass';
import { explainDecision } from '../lib/decision';
import { Icon } from './Icon';
import type { IconName } from './Icon';
import { breakGlassOn, breakGlassLabel } from '../lib/breakGlass';
import { requestUrl } from '../lib/requestLink';
import type { RequestPrefill } from '../lib/requestLink';

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
  /**
   * tip = popover on hover/focus (fixed to the viewport, so a table's scroll
   * box does not clip it), inline = text under the button, none = title only.
   */
  hint?: 'tip' | 'inline' | 'none';
  /** Extra reason to disable, regardless of the decision (e.g. cluster unreachable). */
  forceDisabled?: string;
  /** Where the action was denied, so "Request access" opens the form filled in. */
  request?: Omit<RequestPrefill, 'action'>;
}

/** Matches `.tipwrap .pop` max-width in app.css. */
const TIP_WIDTH = 260;

/** A button gated by a DecisionInfo: enabled, or `.btn.dis` with the reason from the API. */
export function ActionButton({
  action, decision, label, icon, target, to, onClick, size = 'md', plain, hint = 'tip', forceDisabled, request,
}: ActionButtonProps) {
  const { usesGrants, me } = useAuth();
  const bg = useBreakGlass();
  const tipId = useId();
  const sm = size === 'sm' ? ' sm' : '';
  const exp = explainDecision(decision, { action, target, canBreakGlass: usesGrants && breakGlassOn(me) });
  const disabledText = forceDisabled ?? exp?.text;
  const wrap = useRef<HTMLSpanElement>(null);
  const [tipPos, setTipPos] = useState<CSSProperties | null>(null);
  const placeTip = () => {
    const r = wrap.current?.getBoundingClientRect();
    if (!r) return;
    // Below the button, left-aligned unless it would run off the right edge.
    const vw = document.documentElement.clientWidth;
    setTipPos(r.left + TIP_WIDTH > vw - 16
      ? { position: 'fixed', top: r.bottom + 6, left: 'auto', right: vw - r.right }
      : { position: 'fixed', top: r.bottom + 6, left: r.left });
  };
  const open = tipPos !== null;
  useEffect(() => {
    if (!open) return;
    const follow = () => placeTip();
    window.addEventListener('scroll', follow, true);
    window.addEventListener('resize', follow);
    return () => {
      window.removeEventListener('scroll', follow, true);
      window.removeEventListener('resize', follow);
    };
  }, [open]);

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
          <Link to={requestUrl(request && { ...request, action })}>Request access</Link>
        </div>
      )}
      {cta === 'breakglass' && (
        <div style={{ marginTop: 6 }}>
          <button type="button" className="linklike" onClick={() => bg.open({ action, target, environment: decision?.environment })}>
            {breakGlassLabel(me)}
          </button>
        </div>
      )}
    </>
  );

  return (
    <span
      ref={wrap}
      className={`tipwrap${hint === 'inline' ? ' inl' : ''}`}
      {...(hint === 'tip' && {
        onMouseEnter: placeTip, onFocus: placeTip,
        onMouseLeave: () => setTipPos(null),
        onBlur: (e: React.FocusEvent) => { if (!e.currentTarget.contains(e.relatedTarget)) setTipPos(null); },
      })}
    >
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
        <span className={`tip pop${tipPos?.right !== undefined ? ' end' : ''}`} id={tipId} role="tooltip" style={tipPos ?? undefined}>
          {reasonLine}
        </span>
      )}
      {hint === 'inline' && <span className="muted hintline">{reasonLine}</span>}
    </span>
  );
}
