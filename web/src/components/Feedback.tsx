import { ApiError, errorText } from '../api';
import { Icon } from './Icon';

export function Skeleton({ rows = 3 }: { rows?: number }) {
  const widths = [90, 70, 80, 60, 85];
  return (
    <div className="col" style={{ gap: 8, padding: 16 }} aria-busy="true" aria-label="Loading">
      {Array.from({ length: rows }, (_, i) => (
        <div key={i} className="skel" style={{ width: `${widths[i % widths.length]}%` }} />
      ))}
    </div>
  );
}

export function ErrorBanner({ error, onRetry, title }: { error: unknown; onRetry?: () => void; title?: string }) {
  if (!error) return null;
  const e = error instanceof ApiError ? error : null;
  const heading =
    title ??
    (e?.code === 'cluster_error' ? 'Cluster unreachable' : e?.code === 'forbidden_role' ? 'Not available for your role' : e?.code === 'not_found' ? 'Not found' : 'Something went wrong');
  return (
    <div className="banner red" role="alert" style={{ alignItems: 'flex-start', flexDirection: 'column', gap: 4 }}>
      <b>{heading}</b>
      <span>{errorText(error)}</span>
      {onRetry && (
        <button type="button" className="btn sm" onClick={onRetry}>
          <Icon name="refresh" size={12} />
          Retry
        </button>
      )}
    </div>
  );
}

export function Empty({ children }: { children: React.ReactNode }) {
  return <div className="empty">{children}</div>;
}

export function Loading({ what }: { what?: string }) {
  return (
    <div className="card">
      <Skeleton rows={4} />
      <span className="sr-only">Loading {what}</span>
    </div>
  );
}
