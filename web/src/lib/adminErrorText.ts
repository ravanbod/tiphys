import { ApiError, errorText } from '../api';

/** Readable text for admin mutations; maps the "last admin" 409 to guidance. */
export function adminErrorText(e: unknown): string {
  if (e instanceof ApiError && e.status === 409) {
    if (/last[ _-]?admin|no (enabled )?admin|only (enabled )?admin/i.test(`${e.code} ${e.message}`)) {
      return 'This would leave Tiphys without an enabled admin. Make another user an admin first, then try again.';
    }
    return e.message;
  }
  return errorText(e);
}

/** 422 field messages (empty object for any other error). */
export function adminFieldErrors(e: unknown): Record<string, string> {
  return e instanceof ApiError && e.status === 422 ? e.fields : {};
}
