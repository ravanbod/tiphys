/**
 * Keyboard movement in a list of options, as a native select does it:
 * disabled options are skipped and the ends do not wrap.
 */

/** The enabled index `step` away from `from` (±1, ±10), clamped to the ends; `from` when none is enabled that way. */
export function moveIndex(disabled: boolean[], from: number, step: number): number {
  const n = disabled.length;
  if (n === 0) return -1;
  const dir = step > 0 ? 1 : -1;
  const target = Math.min(Math.max(from + step, 0), n - 1);
  // Land on an enabled option: look further in the direction, then back.
  for (let i = target; i >= 0 && i < n; i += dir) if (!disabled[i]) return i;
  for (let i = target - dir; i >= 0 && i < n; i -= dir) if (!disabled[i]) return i;
  return from;
}

/** The first (`last`: last) enabled index, or -1. */
export function edgeIndex(disabled: boolean[], last = false): number {
  if (last) {
    for (let i = disabled.length - 1; i >= 0; i--) if (!disabled[i]) return i;
    return -1;
  }
  return disabled.findIndex((d) => !d);
}

/**
 * Type-ahead: the next enabled option after `from` (wrapping) whose label
 * starts with what was typed, any case; typing one letter again cycles
 * through the options starting with it. `from` when nothing matches.
 */
export function typeaheadIndex(labels: string[], disabled: boolean[], from: number, typed: string): number {
  const t = typed.toLowerCase();
  if (!t) return from;
  const n = labels.length;
  // One letter repeated ("ppp") cycles like typing "p" again.
  const cycle = t.length > 1 && [...t].every((c) => c === t[0]);
  const prefix = cycle ? t[0] : t;
  // A longer prefix may still match the current option; a single letter moves on.
  const start = prefix.length > 1 && !cycle ? from : from + 1;
  for (let k = 0; k < n; k++) {
    const i = (((start + k) % n) + n) % n;
    if (!disabled[i] && labels[i].toLowerCase().startsWith(prefix)) return i;
  }
  return from;
}
