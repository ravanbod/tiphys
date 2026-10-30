import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { api } from '../api';
import { ApiError, errorText } from '../api/errors';

const MOCK = import.meta.env.VITE_MOCK === '1';

function loginErrorText(e: unknown): string {
  if (e instanceof ApiError && e.code === 'rate_limited') return 'Too many sign-in attempts. Wait a minute and try again.';
  return errorText(e);
}

export function Login() {
  const next = new URLSearchParams(window.location.search).get('next') ?? '/';
  const safe = next.startsWith('/') && !next.startsWith('//') ? next : '/';
  const [local, setLocal] = useState(false);

  useEffect(() => {
    if (MOCK) return;
    api.authOptions().then((o) => setLocal(o.localLogin), () => setLocal(false));
  }, []);

  return (
    <div style={{ flex: 1, display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center' }}>
      <main style={{ width: '100%', maxWidth: 400, padding: 24, display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 28, margin: 'auto' }}>
        <div className="col" style={{ alignItems: 'center', gap: 16 }}>
          <span className="mark lg" aria-hidden="true" />
          <h1 style={{ margin: 0, fontSize: 34, fontWeight: 600, letterSpacing: '-.01em' }}>Tiphys</h1>
        </div>
        <div className="col" style={{ width: '100%', gap: 12 }}>
          {MOCK ? (
            <Link className="btn pri" to="/" style={{ height: 42, justifyContent: 'center', fontSize: 14, width: '100%' }}>Sign in (mock)</Link>
          ) : (
            <a className="btn pri" href={`/auth/login?next=${encodeURIComponent(safe)}`} style={{ height: 42, justifyContent: 'center', fontSize: 14, width: '100%' }}>
              Sign in
            </a>
          )}
        </div>
        {local && <LocalLoginForm next={safe} />}
      </main>
    </div>
  );
}

function LocalLoginForm({ next }: { next: string }) {
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!username.trim() || !password) {
      setError('Enter your username and password.');
      return;
    }
    setBusy(true);
    setError('');
    try {
      const r = await api.localLogin({ username: username.trim(), password, next });
      window.location.assign(r.redirect);
    } catch (err) {
      setError(loginErrorText(err));
      setPassword('');
      setBusy(false);
    }
  };

  return (
    <form className="col" style={{ width: '100%', gap: 12 }} onSubmit={(e) => void submit(e)} aria-label="Sign in with a local account">
      <div className="muted" style={{ textAlign: 'center', fontSize: 12 }}>or with a local account</div>
      <label className="f">
        Username
        <input className="input mono" value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="username" />
      </label>
      <label className="f">
        Password
        <input className="input" type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="current-password" />
      </label>
      {error && (
        <div className="banner red" role="alert" style={{ fontSize: 12 }}>
          {error}
        </div>
      )}
      <button type="submit" className="btn" disabled={busy} style={{ height: 42, justifyContent: 'center', fontSize: 14, width: '100%' }}>
        {busy ? 'Signing in…' : 'Sign in with password'}
      </button>
    </form>
  );
}
