import { describe, expect, it } from 'vitest';
import { loginUrl } from './query';

describe('loginUrl', () => {
  it('goes straight to Keycloak unless local accounts are offered', () => {
    expect(loginUrl('/c/prod/n/payments?x=1')).toBe('/auth/login?next=%2Fc%2Fprod%2Fn%2Fpayments%3Fx%3D1');
    expect(loginUrl('/access', true)).toBe('/login?next=%2Faccess');
    expect(loginUrl('//evil.example', true)).toBe('/login?next=%2F');
  });
});
