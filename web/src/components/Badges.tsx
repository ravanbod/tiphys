import type { Action, JobSummary, WorkloadKind } from '../api/types';

export function isProd(env: string | null | undefined): boolean {
  return (env ?? '').toLowerCase() === 'prod' || (env ?? '').toLowerCase() === 'production';
}

export function EnvBadge({ env }: { env: string | null | undefined }) {
  if (!env) return <span className="muted">—</span>;
  return <span className={`env${isProd(env) ? ' prod' : ''}`}>{env}</span>;
}

export function Tag({ children }: { children: React.ReactNode }) {
  return <span className="tag">{children}</span>;
}

export function KindTag({ kind }: { kind: WorkloadKind | string }) {
  return <span className="tag">{kind}</span>;
}

/** logs is read-only (plain chip); write actions use the dark `.act` chip. */
export function ActionChip({ action }: { action: Action }) {
  return <span className={`chip${action === 'logs' ? '' : ' act'}`}>{action}</span>;
}

export function ActionChips({ actions, nowrap }: { actions: Action[]; nowrap?: boolean }) {
  return (
    <span className="row" style={{ gap: 4, flexWrap: nowrap ? 'nowrap' : 'wrap' }}>
      {actions.map((a) => (
        <ActionChip key={a} action={a} />
      ))}
    </span>
  );
}

export function HealthChip({ health }: { health: string }) {
  const tone = health === 'Healthy' ? 'ok' : health === 'Degraded' || health === 'Missing' ? 'err' : health === 'Progressing' ? 'info' : health === 'Suspended' ? 'warn' : '';
  return (
    <span className={`chip ${tone}`}>
      {tone && <span className={`dot ${tone}`} />}
      {health}
    </span>
  );
}

export function SyncChip({ sync }: { sync: string }) {
  return <span className={`chip ${sync === 'Synced' ? 'ok' : sync === 'OutOfSync' ? 'warn' : ''}`}>{sync}</span>;
}

export function PhaseChip({ phase }: { phase: string }) {
  const tone = phase === 'Running' || phase === 'Succeeded' ? 'ok' : phase === 'Pending' ? 'warn' : phase === 'Failed' ? 'err' : '';
  return (
    <span className={`chip ${tone}`}>
      {tone && <span className={`dot ${tone}`} />}
      {phase}
    </span>
  );
}

export function JobStatusChip({ status }: { status: JobSummary['status'] | 'running' | 'succeeded' | 'failed' | null }) {
  if (!status) return <span className="muted">—</span>;
  if (status === 'running')
    return (
      <span className="chip info">
        <span className="dot info" />
        Running
      </span>
    );
  return <span className={`chip ${status === 'succeeded' ? 'ok' : 'err'}`}>{status === 'succeeded' ? 'Succeeded' : 'Failed'}</span>;
}

export function ResultChip({ result }: { result: string }) {
  const tone = result === 'denied' || result === 'failed' ? 'err' : 'ok';
  const label = result.charAt(0).toUpperCase() + result.slice(1);
  return <span className={`chip ${tone}`}>{label}</span>;
}

export function BreakGlassMark() {
  return <span className="bgm">Break-glass</span>;
}

export function Avatar({ name, size }: { name: string; size?: number }) {
  const initials = name
    .split(/[\s._-]+/)
    .filter(Boolean)
    .slice(0, 2)
    .map((p) => p[0]?.toUpperCase())
    .join('');
  const style = size ? { width: size, height: size, fontSize: Math.round(size * 0.4) } : undefined;
  return (
    <span className="av" style={style} aria-hidden="true">
      {initials || '·'}
    </span>
  );
}
