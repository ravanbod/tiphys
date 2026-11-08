export interface ComboOption {
  value: string;
  /** Shown and searched; the value when absent. */
  label?: string;
  /** Muted text after the label (e.g. "Team"), also searched. */
  hint?: string;
}

export const optionLabel = (o: ComboOption) => o.label ?? o.value;

/**
 * The options matching every word of the query (label, value, hint), those
 * whose label starts with the query first, otherwise in their given order.
 */
export function filterOptions(options: ComboOption[], query: string): ComboOption[] {
  const q = query.trim().toLowerCase();
  if (!q) return options;
  const words = q.split(/\s+/);
  const hits = options.filter((o) => {
    const text = `${optionLabel(o)} ${o.value} ${o.hint ?? ''}`.toLowerCase();
    return words.every((w) => text.includes(w));
  });
  const starts = (o: ComboOption) => optionLabel(o).toLowerCase().startsWith(q);
  return [...hits.filter(starts), ...hits.filter((o) => !starts(o))];
}

/** The option the typed text names exactly (label or value, any case). */
export function exactOption(options: ComboOption[], query: string): ComboOption | undefined {
  const q = query.trim().toLowerCase();
  if (!q) return undefined;
  return options.find((o) => optionLabel(o).toLowerCase() === q || o.value.toLowerCase() === q);
}

/** Checklists (e.g. workloads) get a filter box once they are longer than this. */
export const LIST_SEARCH_FROM = 8;

/** Whether the text contains every word of the query, any case. */
export function matchesQuery(text: string, query: string): boolean {
  const t = text.toLowerCase();
  return query.trim().toLowerCase().split(/\s+/).filter(Boolean).every((w) => t.includes(w));
}
