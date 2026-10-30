import { Link } from 'react-router-dom';
import { Icon } from './Icon';

export function PageHeader({ title, subtitle, children }: { title: React.ReactNode; subtitle?: React.ReactNode; children?: React.ReactNode }) {
  return (
    <div className="ph">
      <div>
        <h1>{title}</h1>
        {subtitle && <p>{subtitle}</p>}
      </div>
      <span className="grow" />
      {children}
    </div>
  );
}

export interface Crumb {
  label: React.ReactNode;
  to?: string;
  mono?: boolean;
}

export function Crumbs({ items }: { items: Crumb[] }) {
  return (
    <nav className="crumbs" aria-label="Breadcrumb">
      {items.map((c, i) => (
        <span key={i} className="row" style={{ gap: 6 }}>
          {i > 0 && <Icon name="chev" />}
          {c.to ? (
            <Link to={c.to} className={c.mono ? 'mono' : undefined}>{c.label}</Link>
          ) : (
            <span className={c.mono ? 'mono' : undefined} style={{ color: 'var(--tx)' }}>{c.label}</span>
          )}
        </span>
      ))}
    </nav>
  );
}
