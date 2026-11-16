import { describe, expect, it } from 'vitest';
import { ALL_PERMISSIONS, curlExample, expiryFrom, PERMISSION_LABEL, PERMISSION_ROWS, readOf, togglePermission } from './apiTokens';

describe('api token permissions', () => {
  it('ticks the read with a write', () => {
    expect(togglePermission([], 'time_windows:delete', true)).toEqual(['time_windows:read', 'time_windows:delete']);
    expect(togglePermission([], 'settings.break_glass:write', true)).toEqual(['settings:read', 'settings.break_glass:write']);
  });
  it('unticks the writes that need a read', () => {
    const all = ['settings:read', 'settings.exec:write', 'settings.retention:write', 'holidays:read'] as const;
    expect(togglePermission([...all], 'settings:read', false)).toEqual(['holidays:read']);
    expect(togglePermission(['time_windows:read', 'time_windows:create'], 'time_windows:create', false)).toEqual(['time_windows:read']);
  });
  it('keeps catalog order without duplicates', () => {
    expect(togglePermission(['holidays:read', 'time_windows:read'], 'time_windows:read', true)).toEqual(['time_windows:read', 'holidays:read']);
  });
  it('covers every permission in the grid and the labels', () => {
    const inGrid = PERMISSION_ROWS.flatMap((r) => Object.values(r.cells));
    expect([...inGrid].sort()).toEqual([...ALL_PERMISSIONS].sort());
    ALL_PERMISSIONS.forEach((p) => expect(ALL_PERMISSIONS, p).toContain(readOf(p)));
    expect(Object.keys(PERMISSION_LABEL).sort()).toEqual([...ALL_PERMISSIONS].sort());
  });
  it('computes the expiry', () => {
    expect(expiryFrom(null, 0)).toBeNull();
    expect(expiryFrom(1, Date.UTC(2026, 0, 1))).toBe('2026-01-02T00:00:00.000Z');
  });
});

describe('curl example', () => {
  const origin = 'https://tiphys.example.com';
  it('shows a read and the first write the token may make', () => {
    expect(curlExample(['time_windows:read', 'time_windows:delete', 'settings:read', 'settings.break_glass:write'], origin)).toBe(
      'curl -H "Authorization: Bearer $TIPHYS_TOKEN" \\\n  https://tiphys.example.com/api/v1/admin/time-windows\n\n'
      + 'curl -X PATCH -H "Authorization: Bearer $TIPHYS_TOKEN" \\\n  -H \'Content-Type: application/json\' \\\n'
      + '  -d \'{"breakGlassMode":"approval"}\' \\\n  https://tiphys.example.com/api/v1/admin/settings',
    );
  });
  it('shows only a read for a read-only token, and a delete without a body', () => {
    expect(curlExample(['holidays:read'], origin)).not.toContain('-X');
    const del = curlExample(['time_windows:read', 'time_windows:delete'], origin);
    expect(del).toContain('-X DELETE');
    expect(del).toContain('/admin/time-windows/{id}');
    expect(del).not.toContain('Content-Type');
  });
  it('has an example for every write', () => {
    ALL_PERMISSIONS.filter((p) => readOf(p) !== p).forEach((p) => expect(curlExample([readOf(p), p], origin), p).toContain('-X'));
  });
});
