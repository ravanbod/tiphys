import { describe, expect, it } from 'vitest';
import { ApiError, errorText, toApiError } from './errors';
import { kindFromSegment, kindSegment, loginUrl, qs, safeNext } from './query';

describe('toApiError', () => {
  it('parses the standard envelope', () => {
    const e = toApiError(
      403,
      JSON.stringify({ error: { code: 'denied', message: 'closed', details: { allowed: false, reason: 'outside_window', environment: 'prod' } } }),
    );
    expect(e).toBeInstanceOf(ApiError);
    expect(e.status).toBe(403);
    expect(e.code).toBe('denied');
    expect(e.decision?.reason).toBe('outside_window');
  });
  it('exposes 422 field errors and violations', () => {
    const e = toApiError(422, JSON.stringify({ error: { code: 'invalid', message: 'bad', details: { fields: { reason: 'too short' }, violations: ['can read secrets'] } } }));
    expect(e.fields).toEqual({ reason: 'too short' });
    expect(e.violations).toEqual(['can read secrets']);
  });
  it('decision is null for non-denied errors', () => {
    expect(toApiError(403, JSON.stringify({ error: { code: 'forbidden_role', message: 'no' } })).decision).toBeNull();
  });
  it('maps non-JSON bodies by status', () => {
    expect(toApiError(502, '<html>bad gateway</html>').code).toBe('cluster_error');
    expect(toApiError(429, '').code).toBe('rate_limited');
    expect(toApiError(500, 'boom').code).toBe('server_error');
    expect(toApiError(418, '').code).toBe('http_error');
    expect(toApiError(503, '').message).toContain('503');
  });
  it('errorText', () => {
    expect(errorText(new ApiError(429, 'rate_limited', 'x'))).toContain('Too many');
    expect(errorText(new ApiError(409, 'already_running', 'A run is in progress'))).toBe('A run is in progress');
    expect(errorText('weird')).toBe('Something went wrong');
  });
});

describe('query helpers', () => {
  it('qs drops empty values and encodes', () => {
    expect(qs({ a: 1, b: '', c: undefined, d: 'x y', e: false })).toBe('?a=1&d=x+y&e=false');
    expect(qs({})).toBe('');
    expect(qs(undefined)).toBe('');
  });
  it('kind segments', () => {
    expect(kindSegment('StatefulSet')).toBe('statefulsets');
    expect(kindFromSegment('rollouts')).toBe('Rollout');
  });
  it('next must be a same-origin path', () => {
    expect(safeNext('/clusters/x?y=1')).toBe('/clusters/x?y=1');
    expect(safeNext('//evil.example')).toBe('/');
    expect(safeNext('https://evil.example')).toBe('/');
    expect(loginUrl('/a b')).toBe('/auth/login?next=%2Fa%20b');
  });
});
