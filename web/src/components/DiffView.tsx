import type { ArgoDiff, ArgoPlanResource } from '../api';

type Res = ArgoDiff['resources'][number];

const STATE_LABEL: Record<Res['state'], string> = { changed: 'changed', missing: 'missing in cluster', extra: 'not in Git (left in place unless pruned)' };

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

function planChip(r: ArgoPlanResource, prune: boolean): { text: string; tone: string } {
  switch (r.state) {
    case 'changed': return { text: 'changed', tone: 'warn' };
    case 'missing': return { text: 'created', tone: 'info' };
    case 'extra':
      if (!r.prunable) return { text: 'kept (Prune=false)', tone: '' };
      return prune ? { text: 'pruned', tone: 'err' } : { text: 'not in Git (left in place)', tone: '' };
    case 'synced': return { text: 'in sync', tone: 'ok' };
    case 'present': return { text: 'applied', tone: '' };
  }
}

/**
 * The resources of a sync plan, each with a checkbox to sync only some and,
 * for the target revision, its diff folded.
 */
export function PlanList({ resources, prune, isSelected, onToggle }: {
  resources: ArgoPlanResource[]; prune: boolean; isSelected: (r: ArgoPlanResource) => boolean; onToggle: (r: ArgoPlanResource) => void;
}) {
  return (
    <div className="col" style={{ gap: 6 }}>
      {resources.map((r) => {
        const chip = planChip(r, prune);
        const name = `${r.namespace ? `${r.namespace}/` : ''}${r.name}`;
        const head = (
          <>
            <input type="checkbox" checked={isSelected(r)} onChange={() => onToggle(r)} aria-label={`Sync ${r.kind} ${name}`} />
            <span className="tag">{r.kind}</span>
            <span className="mono">{name}</span>
            <span className={`chip ${chip.tone}`}>{chip.text}</span>
          </>
        );
        const key = `${r.group}/${r.kind}/${r.namespace}/${r.name}`;
        if (r.state === 'synced' || r.state === 'present' || (!r.diff && !r.hidden && r.state !== 'changed')) return <div key={key} className="diffres row" style={{ gap: 8 }}>{head}</div>;
        return (
          <details key={key} className="diffres">
            <summary>{head}</summary>
            {r.hidden
              ? <div className="muted" style={{ padding: '6px 0' }}>Secret contents are never shown.</div>
              : r.diff ? <DiffText text={r.diff} /> : <div className="muted" style={{ padding: '6px 0' }}>Only ignored fields differ.</div>}
            {r.truncated && <div className="muted">Diff cut after 2,000 lines.</div>}
          </details>
        );
      })}
    </div>
  );
}
