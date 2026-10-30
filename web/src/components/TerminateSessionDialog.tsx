import { useState } from 'react';
import { api, errorText } from '../api';
import { useToast } from '../context/toast';
import { Dialog } from './Dialog';
import { EnvBadge } from './Badges';

export interface TerminateTarget {
  id: string;
  username: string;
  pod: string;
  container: string;
  environment: string;
}

/** Admin-only confirm dialog for ending an open exec session. */
export function TerminateSessionDialog({
  session, onClose, onDone,
}: {
  session: TerminateTarget;
  onClose: () => void;
  /** Called after the terminate request was accepted. */
  onDone: () => void;
}) {
  const toast = useToast();
  const [busy, setBusy] = useState(false);

  const confirm = async () => {
    setBusy(true);
    try {
      await api.terminateSession(session.id);
      toast(`Termination requested for ${session.username}'s session. It closes within a few seconds.`);
      onDone();
    } catch (e) {
      toast(errorText(e), 'err');
      setBusy(false);
    }
  };

  return (
    <Dialog
      title="Terminate this session?"
      subtitle={<>The shell closes within about 5 seconds. Everything recorded so far is kept.</>}
      onClose={busy ? () => {} : onClose}
      tone={session.environment.toLowerCase() === 'prod' ? 'prod' : undefined}
      width={460}
      footer={
        <>
          <button type="button" className="btn" data-autofocus onClick={onClose} disabled={busy}>
            Cancel
          </button>
          <button type="button" className="btn dng" onClick={confirm} disabled={busy}>
            {busy ? 'Terminating…' : 'Terminate session'}
          </button>
        </>
      }
    >
      <dl className="kv" style={{ gridTemplateColumns: '90px 1fr' }}>
        <dt>User</dt>
        <dd>{session.username}</dd>
        <dt>Pod</dt>
        <dd className="mono">{session.pod} · {session.container}</dd>
        <dt>Environment</dt>
        <dd><EnvBadge env={session.environment} /></dd>
      </dl>
      <div className="banner yel">The user sees &ldquo;Ended by the platform team&rdquo; and cannot reconnect until they start a new session.</div>
    </Dialog>
  );
}
