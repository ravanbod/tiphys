import type { ArgoDiff } from '../api';

type Res = ArgoDiff['resources'][number];

const STATE_LABEL: Record<Res['state'], string> = { changed: 'changed', missing: 'missing in cluster', extra: 'not in Git (left in place)' };

/** A unified diff (live → desired), lines coloured; the file header is left out. */
export function DiffText({ text }: { text: string }) {
  const lines = text.split('\n').filter((l) => !l.startsWith('---') && !l.startsWith('+++'));
  return (
    <pre className="diff">
      {lines.map((l, i) => (
        <span key={i} className={l.startsWith('@@') ? 'hunk' : l.startsWith('+') ? 'add' : l.startsWith('-') ? 'del' : undefined}>{l}{'\n'}</span>
      ))}
    </pre>
  );
}

/** The resources a sync would change, each with its diff folded. */
export function DiffList({ diff, open }: { diff: ArgoDiff; open?: boolean }) {
  if (diff.resources.length === 0) return <div className="empty">Nothing to sync: the cluster matches Git.</div>;
  return (
    <div className="col" style={{ gap: 6 }}>
      {diff.resources.map((r) => (
        <details key={`${r.kind}/${r.namespace}/${r.name}`} className="diffres" open={open}>
          <summary>
            <span className="tag">{r.kind}</span>
            <span className="mono">{r.namespace ? `${r.namespace}/` : ''}{r.name}</span>
            <span className={`chip ${r.state === 'changed' ? 'warn' : r.state === 'missing' ? 'info' : ''}`}>{STATE_LABEL[r.state]}</span>
          </summary>
          {r.hidden
            ? <div className="muted" style={{ padding: '6px 0' }}>Secret contents are never shown.</div>
            : r.diff ? <DiffText text={r.diff} /> : <div className="muted" style={{ padding: '6px 0' }}>Only ignored fields differ.</div>}
          {r.truncated && <div className="muted">Diff cut after 2,000 lines.</div>}
        </details>
      ))}
      {diff.truncated && <div className="muted">Only the first 200 resources are listed.</div>}
    </div>
  );
}
