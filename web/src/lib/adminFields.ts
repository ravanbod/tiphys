/** Helpers for 422 `details.fields` (field name -> message) coming back from the admin API. */

/** Message for the first of `keys` present in `fields` (a key also matches nested ones such as `scope.cluster`). */
export function fieldError(fields: Record<string, string>, ...keys: string[]): string | undefined {
  for (const k of keys) {
    if (fields[k]) return fields[k];
  }
  for (const k of keys) {
    for (const f of Object.keys(fields)) {
      if (f.startsWith(`${k}.`) || f.startsWith(`${k}[`)) return fields[f];
    }
  }
  return undefined;
}

/** Field errors that none of the form's inputs displayed, so nothing the server said is lost. */
export function unmatchedFields(fields: Record<string, string>, known: string[]): [string, string][] {
  return Object.entries(fields).filter(
    ([f]) => !known.some((k) => f === k || f.startsWith(`${k}.`) || f.startsWith(`${k}[`)),
  );
}
