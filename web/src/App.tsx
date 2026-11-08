import { Suspense, lazy } from 'react';
import { BrowserRouter, Navigate, Outlet, Route, Routes } from 'react-router-dom';
import type { Role } from './api';
import { BareShell, Shell, homeFor } from './components/Shell';
import { AuthProvider, useAuth } from './context/auth';
import { BreakGlassProvider } from './context/breakglass';
import { LiveProvider } from './context/live';
import { ShellContextProvider } from './context/shell';
import { ThemeProvider, useTheme } from './context/theme';
import { ToastProvider } from './context/toast';
import { Login } from './pages/Login';
import { Workloads } from './pages/Workloads';
import { NamespacePage } from './pages/NamespacePage';
import { WorkloadDetailPage } from './pages/WorkloadDetailPage';
import { CronJobsPage } from './pages/CronJobsPage';
import { LogsPage } from './pages/LogsPage';
import { MyAccess } from './pages/MyAccess';
import { MyTeams } from './pages/MyTeams';
import { Requests } from './pages/admin/Requests';
import { Grants } from './pages/admin/Grants';
import { TimeWindows } from './pages/admin/TimeWindows';
import { Users } from './pages/admin/Users';
import { Teams } from './pages/admin/Teams';
import { Clusters } from './pages/admin/Clusters';
import { Settings } from './pages/admin/Settings';
import { AuditLog } from './pages/audit/AuditLog';
import { Sessions } from './pages/audit/Sessions';

// Heavy pages (xterm.js, asciinema-player) are split out of the main bundle.
const ExecPage = lazy(() => import('./pages/ExecPage').then((m) => ({ default: m.ExecPage })));
const Replay = lazy(() => import('./pages/audit/Replay').then((m) => ({ default: m.Replay })));
const Watch = lazy(() => import('./pages/audit/Watch').then((m) => ({ default: m.Watch })));

function AppRoot({ children }: { children: React.ReactNode }) {
  const { dark } = useTheme();
  return <div className={`app${dark ? ' dark' : ''}`}>{children}</div>;
}

function Authed() {
  return (
    <AuthProvider>
      <ShellContextProvider>
        <BreakGlassProvider>
          <LiveProvider>
            <Outlet />
          </LiveProvider>
        </BreakGlassProvider>
      </ShellContextProvider>
    </AuthProvider>
  );
}

/** Redirects to the role's home when the role is not allowed. */
function Only({ roles, children }: { roles: Role[]; children: React.ReactNode }) {
  const { role } = useAuth();
  if (!roles.includes(role)) return <Navigate to={homeFor(role)} replace />;
  return <>{children}</>;
}

const WORK: Role[] = ['user', 'auditor', 'admin'];
const ADMIN: Role[] = ['admin'];
const READ_CONFIG: Role[] = ['admin', 'auditor'];

export function App() {
  return (
    <ThemeProvider>
      <BrowserRouter>
        <AppRoot>
          <ToastProvider>
            <Suspense fallback={<div className="boot" aria-busy="true">Loading…</div>}>
            <Routes>
              <Route path="/login" element={<Login />} />
              <Route element={<Authed />}>
                <Route element={<Shell />}>
                  <Route index element={<Only roles={WORK}><Workloads /></Only>} />
                  <Route path="c/:cluster/n/:ns" element={<Only roles={WORK}><NamespacePage /></Only>} />
                  <Route path="c/:cluster/n/:ns/w/:kind/:name" element={<Only roles={WORK}><WorkloadDetailPage /></Only>} />
                  <Route path="c/:cluster/n/:ns/cronjobs" element={<Only roles={WORK}><CronJobsPage /></Only>} />
                  <Route path="access" element={<Only roles={['user', 'auditor']}><MyAccess /></Only>} />
                  <Route path="my-teams" element={<Only roles={['user', 'auditor']}><MyTeams /></Only>} />
                  <Route path="requests" element={<Only roles={ADMIN}><Requests /></Only>} />
                  <Route path="grants" element={<Only roles={READ_CONFIG}><Grants /></Only>} />
                  <Route path="time-windows" element={<Only roles={READ_CONFIG}><TimeWindows /></Only>} />
                  <Route path="users" element={<Only roles={READ_CONFIG}><Users /></Only>} />
                  <Route path="teams" element={<Only roles={READ_CONFIG}><Teams /></Only>} />
                  <Route path="clusters" element={<Only roles={READ_CONFIG}><Clusters /></Only>} />
                  <Route path="settings" element={<Only roles={READ_CONFIG}><Settings /></Only>} />
                  <Route path="audit" element={<Only roles={READ_CONFIG}><AuditLog /></Only>} />
                  <Route path="sessions" element={<Only roles={READ_CONFIG}><Sessions /></Only>} />
                </Route>
                <Route element={<BareShell />}>
                  <Route path="c/:cluster/n/:ns/w/:kind/:name/logs" element={<Only roles={WORK}><LogsPage /></Only>} />
                  <Route path="c/:cluster/n/:ns/w/:kind/:name/exec" element={<Only roles={WORK}><ExecPage /></Only>} />
                  <Route path="sessions/:id" element={<Only roles={READ_CONFIG}><Replay /></Only>} />
                  <Route path="sessions/:id/watch" element={<Only roles={READ_CONFIG}><Watch /></Only>} />
                </Route>
                <Route path="*" element={<NotFound />} />
              </Route>
            </Routes>
            </Suspense>
          </ToastProvider>
        </AppRoot>
      </BrowserRouter>
    </ThemeProvider>
  );
}

function NotFound() {
  const { role } = useAuth();
  return <Navigate to={homeFor(role)} replace />;
}
