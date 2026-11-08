import { useEffect, useRef, useState } from 'react';
import { Link, NavLink, Outlet, useLocation, useNavigate } from 'react-router-dom';
import { api } from '../api';
import type { Role } from '../api';
import { useAuth } from '../context/auth';
import { useBreakGlass } from '../context/breakglass';
import { LiveIndicator } from '../context/live';
import { useShellCtx } from '../context/shell';
import { useTheme } from '../context/theme';
import { useApi } from '../hooks/useApi';
import { useNow } from '../hooks/useNow';
import { useStars } from '../hooks/useStars';
import { fmtCountdown, fmtDateTime, fmtReopens, msUntil } from '../lib/time';
import { Avatar, isProd } from './Badges';
import { Icon } from './Icon';
import type { IconName } from './Icon';
import { ARGO_UI } from '../lib/features';
import { breakGlassOn, breakGlassLabel, breakGlassScheduled } from '../lib/breakGlass';

const ROLE_LABEL: Record<Role, string> = { user: 'Developer', admin: 'Admin', auditor: 'Auditor' };

export function homeFor(role: Role): string {
  return role === 'auditor' ? '/audit' : '/';
}

function BreakGlassPill() {
  const { me, reload } = useAuth();
  const { end } = useBreakGlass();
  const now = useNow(1000);
  const bg = me.breakGlass;
  const left = bg ? msUntil(bg.expiresAt, now) : null;
  const expired = left !== null && left <= 0;
  useEffect(() => {
    if (expired) void reload();
  }, [expired, reload]);
  // A scheduled one begins: refetch /me until the server shows it active, so
  // pages keyed on breakGlassKey refetch what it now opens.
  const scheduled = breakGlassScheduled(bg, now);
  const begun = !!bg && !bg.active && !scheduled && !expired;
  useEffect(() => {
    if (!begun) return;
    void reload().catch(() => {});
    const id = window.setInterval(() => void reload().catch(() => {}), 5000);
    return () => window.clearInterval(id);
  }, [begun, reload]);
  if (bg && scheduled) {
    return (
      <span className="bgp" role="status" aria-label="Break-glass scheduled">
        <Icon name="clock" size={14} />
        <span className="bgp-t">Break-glass scheduled</span>
        <span title={`${fmtDateTime(bg.startedAt)} – ${fmtDateTime(bg.expiresAt)}`}>{fmtReopens(bg.startedAt, now)}</span>
        <button type="button" onClick={() => void end()}>Cancel</button>
      </span>
    );
  }
  if (!bg || !bg.active || left === null) return <BreakGlassRequestPill />;
  return (
    <span className="bgp" role="status" aria-label="Break-glass is active">
      <Icon name="warn" size={14} />
      <span className="bgp-t">Break-glass active</span>
      <span className="mono">{fmtCountdown(left)}</span>
      <button type="button" onClick={() => void end()}>End now</button>
    </span>
  );
}

function BreakGlassRequestPill() {
  const { me } = useAuth();
  const { open } = useBreakGlass();
  if (!me.breakGlassRequest) return null;
  return (
    <span className="bgp" role="status" aria-label="Break-glass request pending">
      <Icon name="clock" size={14} />
      <span className="bgp-t">Break-glass requested</span>
      <span>waiting for approval</span>
      <button type="button" onClick={() => open()}>View</button>
    </span>
  );
}

function UserMenu() {
  const { me, usesGrants } = useAuth();
  const { pref, setPref } = useTheme();
  const bg = useBreakGlass();
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open) return;
    const onDoc = (e: MouseEvent) => {
      if (!ref.current?.contains(e.target as Node)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => e.key === 'Escape' && setOpen(false);
    document.addEventListener('mousedown', onDoc);
    document.addEventListener('keydown', onKey);
    return () => {
      document.removeEventListener('mousedown', onDoc);
      document.removeEventListener('keydown', onKey);
    };
  }, [open]);

  async function signOut() {
    try {
      const r = await api.logout();
      const to = r?.redirect && r.redirect !== '/' ? r.redirect : '/login';
      window.location.assign(to);
    } catch {
      window.location.assign('/login');
    }
  }

  return (
    <div className="menuwrap" ref={ref}>
      <button type="button" className="user userbtn" aria-haspopup="menu" aria-expanded={open} onClick={() => setOpen((o) => !o)}>
        <Avatar name={me.displayName || me.username} />
        <span className="uname"><b>{me.displayName || me.username}</b><i>{ROLE_LABEL[me.role]}</i></span>
        <Icon name="chevd" size={12} />
      </button>
      {open && (
        <div className="menu" role="menu">
          <div className="menu-h">Theme</div>
          {(['system', 'light', 'dark'] as const).map((p) => (
            <button key={p} type="button" role="menuitemradio" aria-checked={pref === p} className="mi" onClick={() => setPref(p)}>
              <Icon name={p === 'dark' ? 'moon' : 'sun'} />
              {p === 'system' ? 'Match system' : p === 'light' ? 'Light' : 'Dark'}
              {pref === p && <Icon name="check" style={{ marginLeft: 'auto' }} />}
            </button>
          ))}
          {usesGrants && breakGlassOn(me) && !me.breakGlass && (
            <>
              <div className="menu-sep" />
              <button type="button" role="menuitem" className="mi red" onClick={() => { setOpen(false); bg.open(); }}>
                <Icon name="warn" />{me.breakGlassRequest ? 'Break-glass request pending' : breakGlassLabel(me)}
              </button>
            </>
          )}
          <div className="menu-sep" />
          <button type="button" role="menuitem" className="mi" onClick={() => void signOut()}>Sign out</button>
        </div>
      )}
    </div>
  );
}

export function TopBar() {
  const { role } = useAuth();
  const ctx = useShellCtx();
  const nav = useNavigate();
  const loc = useLocation();
  const inputRef = useRef<HTMLInputElement>(null);
  const [q, setQ] = useState(() => new URLSearchParams(loc.search).get('q') ?? '');

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const t = e.target as HTMLElement | null;
      const typing = t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.tagName === 'SELECT' || t.isContentEditable);
      if (e.key === '/' && !typing && !e.metaKey && !e.ctrlKey && !e.altKey) {
        e.preventDefault();
        inputRef.current?.focus();
        inputRef.current?.select();
      }
    };
    document.addEventListener('keydown', onKey);
    return () => document.removeEventListener('keydown', onKey);
  }, []);

  const searchTarget = role === 'auditor' ? '/audit' : '/';
  const onSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    nav(`${searchTarget}${q.trim() ? `?q=${encodeURIComponent(q.trim())}` : ''}`);
  };

  return (
    <header className="top">
      <Link className="brand" to={homeFor(role)}><span className="mark" />Tiphys</Link>
      <form className="search" role="search" onSubmit={onSubmit}>
        <Icon name="search" />
        <input
          ref={inputRef} value={q} onChange={(e) => setQ(e.target.value)}
          placeholder={role === 'auditor' ? 'Search audit events' : 'Search apps, namespaces, workloads'}
          aria-label="Global search" aria-keyshortcuts="/"
        />
        <kbd aria-hidden="true">/</kbd>
      </form>
      <span className="grow" />
      <LiveIndicator />
      {ctx.cluster ? (
        <span className={`ctx${isProd(ctx.environment) ? ' prod' : ''}`}>
          <span className={`dot${isProd(ctx.environment) ? ' prod' : ''}`} />
          {ctx.cluster} · <b>{ctx.environment}</b>
        </span>
      ) : (
        <span className="ctx"><span className="dot" />All environments</span>
      )}
      <BreakGlassPill />
      <UserMenu />
    </header>
  );
}

interface NavItem { to: string; label: string; icon: IconName; end?: boolean; badge?: number; ro?: boolean }
interface NavGroup { title?: string; items: NavItem[] }

function useNavGroups(): NavGroup[] {
  const { role, me } = useAuth();
  const myTeams: NavItem[] = me.managedTeams.length ? [{ to: '/my-teams', label: 'My teams', icon: 'users' }] : [];
  const overview = useApi(() => api.overview(), [role], { enabled: role === 'admin', poll: 30000 });
  if (role === 'user') {
    return [{ items: [
      { to: '/', label: 'Workloads', icon: 'box', end: true },
      { to: '/access', label: 'My access', icon: 'key' },
      ...myTeams,
    ] }];
  }
  if (role === 'admin') {
    return [
      { items: [
        { to: '/', label: 'Workloads', icon: 'box', end: true },
        { to: '/requests', label: 'Access requests', icon: 'inbox', badge: overview.data && overview.data.pendingRequests + overview.data.pendingBreakGlassRequests },
      ] },
      { title: 'Audit', items: [
        { to: '/audit', label: 'Audit log', icon: 'list' },
        { to: '/sessions', label: 'Sessions', icon: 'term' },
      ] },
      { title: 'Manage', items: [
        { to: '/users', label: 'Users & teams', icon: 'users' },
        { to: '/grants', label: 'Grants', icon: 'shield' },
        { to: '/clusters', label: 'Clusters & environments', icon: 'server' },
        { to: '/time-windows', label: 'Time windows & holidays', icon: 'clock' },
        { to: '/settings', label: 'Settings', icon: 'gear' },
      ] },
    ];
  }
  return [
    { items: [
      { to: '/', label: 'Workloads', icon: 'box', end: true },
      { to: '/access', label: 'My access', icon: 'key' },
      ...myTeams,
    ] },
    { title: 'Audit', items: [
      { to: '/audit', label: 'Audit log', icon: 'list' },
      { to: '/sessions', label: 'Sessions & replay', icon: 'term' },
    ] },
    { title: 'Configuration', items: [
      { to: '/users', label: 'Users & teams', icon: 'users', ro: true },
      { to: '/grants', label: 'Grants', icon: 'shield', ro: true },
      { to: '/clusters', label: 'Clusters & environments', icon: 'server', ro: true },
      { to: '/time-windows', label: 'Time windows & holidays', icon: 'clock', ro: true },
      { to: '/settings', label: 'Settings', icon: 'gear', ro: true },
    ] },
  ];
}

export function SideNav() {
  const groups = useNavGroups();
  const { stars } = useStars();
  return (
    <nav className="side" aria-label="Main">
      {groups.map((g, gi) => (
        <div key={gi} className="sidegrp">
          {g.title && <h6>{g.title}</h6>}
          {g.items.map((it) => (
            <NavLink key={it.to} to={it.to} end={it.end} className={({ isActive }) => (isActive ? 'on' : undefined)}>
              <Icon name={it.icon} />
              {it.label}
              {it.badge ? <span className="n" aria-label={`${it.badge} pending`}>{it.badge}</span> : null}
              {it.ro && <span className="ro" title="Read-only"><Icon name="eye" size={13} /><span className="sr-only">read-only</span></span>}
            </NavLink>
          ))}
        </div>
      ))}
      {stars.length > 0 && (
        <div className="sidegrp">
          <h6>Starred</h6>
          {stars.map((s) => (
            <Link key={s.key} to={`/?expand=${encodeURIComponent(s.key)}`}>
              <Icon name="star" />
              <span className="mono">{s.name}</span>
              <span className={`env${isProd(s.environment) ? ' prod' : ''}`} style={{ marginLeft: 'auto' }}>{s.environment}</span>
            </Link>
          ))}
        </div>
      )}
    </nav>
  );
}

function StaleArgoBanner() {
  const { me, isAuditor } = useAuth();
  if (!me.argo.stale || isAuditor) return null;
  return (
    <div className="banner yel" role="status">
      <Icon name="warn" />
      <span>
        <b>Argo CD data is out of date{me.argo.ageSeconds ? ` (${Math.round(me.argo.ageSeconds / 60)} min old)` : ''}.</b>{' '}
        Grants scoped to an Argo project are paused until it refreshes. Namespace and workload grants still work.
      </span>
    </div>
  );
}

export function Shell() {
  return (
    <>
      <TopBar />
      <div className="body">
        <SideNav />
        <main className="main" id="main">
          {ARGO_UI && <StaleArgoBanner />}
          <Outlet />
        </main>
      </div>
    </>
  );
}

/** Full-height layout for logs, terminal and replay: top bar only. */
export function BareShell() {
  return (
    <div className="bare">
      <TopBar />
      <Outlet />
    </div>
  );
}

