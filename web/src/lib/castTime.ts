/**
 * asciinema-player compresses idle gaps (idleTimeLimit) when it loads a recording, so
 * player.getCurrentTime()/seek() use a shorter "player time" than the recording's own
 * timestamps (which commands and search matches use). This mirrors the player's
 * timeLimiter so we can convert between the two.
 */
export interface CastTimeMap {
  /** recording time (s) -> player time (s) */
  toPlayer(orig: number): number;
  /** player time (s) -> recording time (s) */
  toOrig(player: number): number;
  /** total player-time duration */
  duration: number;
}

export function buildCastTimeMap(times: number[], idleLimit: number | undefined): CastTimeMap {
  const limit = idleLimit !== undefined && idleLimit > 0 ? idleLimit : Infinity;
  const orig: number[] = [];
  const comp: number[] = [];
  let prev = 0;
  let shift = 0;
  for (const t of times) {
    const delta = t - prev - limit;
    prev = t;
    if (delta > 0) shift += delta;
    orig.push(t);
    comp.push(t - shift);
  }
  // last index with arr[i] <= x, or -1
  const lastLE = (arr: number[], x: number): number => {
    let lo = 0;
    let hi = arr.length - 1;
    let ans = -1;
    while (lo <= hi) {
      const mid = (lo + hi) >> 1;
      if (arr[mid] <= x) {
        ans = mid;
        lo = mid + 1;
      } else hi = mid - 1;
    }
    return ans;
  };
  return {
    duration: comp.length ? comp[comp.length - 1] : 0,
    toPlayer(o) {
      const i = lastLE(orig, o);
      if (i < 0) return Math.min(Math.max(o, 0), limit);
      return comp[i] + Math.min(o - orig[i], limit);
    },
    toOrig(p) {
      const i = lastLE(comp, p);
      if (i < 0) return Math.max(p, 0);
      const o = orig[i] + (p - comp[i]);
      return i + 1 < orig.length ? Math.min(o, orig[i + 1]) : o;
    },
  };
}

/** Index of the last item whose `at` <= t (items sorted ascending), or -1. */
export function indexAtOrBefore(ats: number[], t: number): number {
  let lo = 0;
  let hi = ats.length - 1;
  let ans = -1;
  while (lo <= hi) {
    const mid = (lo + hi) >> 1;
    if (ats[mid] <= t) {
      ans = mid;
      lo = mid + 1;
    } else hi = mid - 1;
  }
  return ans;
}
