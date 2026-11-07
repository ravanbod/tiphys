/**
 * The Workloads page keeps its filters in the URL and remembers the last ones
 * for this tab and user (logging out and in as someone else keeps the tab),
 * so coming back (breadcrumb, sidebar or browser back) shows the same list.
 */
export const FILTER_KEYS = ['q', 'cluster', 'env', 'project'] as const;
export type FilterKey = (typeof FILTER_KEYS)[number];

const storageKey = (userId: number) => `tiphys.workloadFilters.${userId}`;

/** Only the filter parameters of a query, in a fixed order ('' when none). */
export function filtersOf(sp: URLSearchParams): string {
  const out = new URLSearchParams();
  for (const k of FILTER_KEYS) {
    const v = sp.get(k);
    if (v) out.set(k, v);
  }
  return out.toString();
}

/**
 * The query to restore on arrival: the saved filters, when the URL brings
 * none of its own and does not point at one app (`expand`, which a filter
 * could hide). Null when nothing is to change.
 */
export function restoredQuery(sp: URLSearchParams, saved: string): URLSearchParams | null {
  if (!saved || filtersOf(sp) || sp.has('expand')) return null;
  const n = new URLSearchParams(sp);
  new URLSearchParams(saved).forEach((v, k) => {
    if ((FILTER_KEYS as readonly string[]).includes(k)) n.set(k, v);
  });
  return filtersOf(n) ? n : null;
}

export function loadFilters(userId: number): string {
  try {
    return window.sessionStorage.getItem(storageKey(userId)) ?? '';
  } catch {
    return '';
  }
}

export function saveFilters(userId: number, v: string) {
  try {
    if (v) window.sessionStorage.setItem(storageKey(userId), v);
    else window.sessionStorage.removeItem(storageKey(userId));
  } catch {
    /* storage may be unavailable */
  }
}
