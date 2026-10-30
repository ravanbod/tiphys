/** Before/after diff for admin-change audit events (details.before / details.after). */

export interface DiffLine {
  kind: 'minus' | 'plus';
  key: string;
  /** Ready to render: "- key: value" or "+ key: value". */
  text: string;
}

export function fmtDiffValue(v: unknown): string {
  if (v === null || v === undefined) return 'null';
  if (typeof v === 'string') return v;
  if (typeof v === 'number' || typeof v === 'boolean') return String(v);
  if (Array.isArray(v) && v.every((x) => x === null || ['string', 'number', 'boolean'].includes(typeof x))) {
    return v.length ? v.map((x) => String(x)).join(', ') : '(none)';
  }
  try {
    return JSON.stringify(v);
  } catch {
    return String(v);
  }
}

function isPlain(v: unknown): v is Record<string, unknown> {
  return !!v && typeof v === 'object' && !Array.isArray(v);
}

function line(kind: DiffLine['kind'], key: string, value: unknown): DiffLine {
  const sign = kind === 'minus' ? '-' : '+';
  return { kind, key, text: key ? `${sign} ${key}: ${fmtDiffValue(value)}` : `${sign} ${fmtDiffValue(value)}` };
}

/** One minus and/or plus line per changed key. Unchanged keys are omitted. */
export function diffObjects(before: unknown, after: unknown): DiffLine[] {
  const b = isPlain(before) ? before : before == null ? {} : null;
  const a = isPlain(after) ? after : after == null ? {} : null;
  if (!b || !a) {
    // scalars or arrays: show whole values
    const out: DiffLine[] = [];
    if (before != null) out.push(line('minus', '', before));
    if (after != null) out.push(line('plus', '', after));
    return out;
  }
  const keys = [...Object.keys(b)];
  for (const k of Object.keys(a)) if (!(k in b)) keys.push(k);
  const out: DiffLine[] = [];
  for (const k of keys) {
    const inB = k in b;
    const inA = k in a;
    if (inB && inA && fmtDiffValue(b[k]) === fmtDiffValue(a[k])) continue;
    if (inB) out.push(line('minus', k, b[k]));
    if (inA) out.push(line('plus', k, a[k]));
  }
  return out;
}

/** Diff for an event's details, or null when it has no before/after. */
export function adminDiff(details: Record<string, unknown> | null | undefined): DiffLine[] | null {
  if (!details || !('before' in details || 'after' in details)) return null;
  return diffObjects(details.before, details.after);
}
