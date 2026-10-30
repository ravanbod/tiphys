import { describe, expect, it } from 'vitest';
import { execUrl } from './routes';

const w = { cluster: 'prod-1', namespace: 'payments-api', kind: 'Deployment', name: 'payments-api' };

describe('execUrl', () => {
  it('opens a shell in the container by default', () => {
    expect(execUrl(w, 'payments-api-abc')).toBe('/c/prod-1/n/payments-api/w/deployments/payments-api/exec?pod=payments-api-abc');
  });
  it('asks for a debug container with mode=debug', () => {
    expect(execUrl(w, 'payments-api-abc', undefined, true)).toBe('/c/prod-1/n/payments-api/w/deployments/payments-api/exec?pod=payments-api-abc&mode=debug');
  });
});
