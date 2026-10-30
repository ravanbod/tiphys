import { describe, expect, it } from 'vitest';
import { ApiError } from '../api';
import { adminErrorText, adminFieldErrors } from './adminErrorText';

describe('adminErrorText', () => {
  it('explains the last admin conflict', () => {
    const e = new ApiError(409, 'conflict', 'cannot remove the last admin');
    expect(adminErrorText(e)).toMatch(/without an enabled admin/);
  });
  it('passes other 409 messages through', () => {
    expect(adminErrorText(new ApiError(409, 'conflict', 'team name already exists'))).toBe('team name already exists');
  });
  it('extracts 422 fields only', () => {
    expect(adminFieldErrors(new ApiError(422, 'invalid', 'bad', { fields: { name: 'required' } }))).toEqual({ name: 'required' });
    expect(adminFieldErrors(new ApiError(409, 'conflict', 'x'))).toEqual({});
  });
});
