import type { AccessRequest } from '../api';
import { groupStatus, statusSummary } from '../lib/requestBatches';

/** Namespaces shown before "+N more"; the title lists them all. */
const SHOWN = 3;

/** A batch's scope: its namespaces on its cluster. */
export function BatchScope({ g }: { g: AccessRequest[] }) {
  const names = g.map((r) => r.scope.namespace ?? '');
  const lines = g.map((r) => `${r.scope.namespace}: ${r.status}`).join('\n');
  return (
    <span title={lines}>
      <span className="tag">{g.length} namespaces</span>{' '}
      <span className="mono">{names.slice(0, SHOWN).join(', ')}</span>
      {names.length > SHOWN && <span className="muted"> +{names.length - SHOWN} more</span>}
      <span className="muted"> · {g[0].scope.cluster}</span>
    </span>
  );
}

/** Status chip of a request or batch ("Approved 3 · Rejected 2") with the decision note. */
export function GroupStatus({ g }: { g: AccessRequest[] }) {
  const s = groupStatus(g);
  const tone = s === 'pending' ? 'warn' : s === 'rejected' ? 'err' : s === 'approved' ? 'ok' : '';
  const note = g[0].decisionNote;
  return (
    <>
      <span className={`chip ${tone}`}>{statusSummary(g)}</span>
      {note && <div className="muted" style={{ fontSize: 11.5, marginTop: 2 }}>Note: {note}</div>}
    </>
  );
}
