import { describe, expect, it } from 'vitest';
import { checkSyncClusterKind, parseSyncClusterKinds, syncClusterKindsError } from './syncClusterKinds';

describe('sync cluster kinds', () => {
  it('accepts Kind.group of a non-core group', () => {
    expect(checkSyncClusterKind('ClusterInput.fluentbit.fluent.io')).toBe('');
  });
  it('refuses what must stay with admins, like the server', () => {
    for (const k of ['Namespace', 'PersistentVolume', 'ClusterRole.rbac.authorization.k8s.io', 'MutatingWebhookConfiguration.admissionregistration.k8s.io',
      'CustomResourceDefinition.apiextensions.k8s.io', 'APIService.apiregistration.k8s.io', 'StorageClass.storage.k8s.io', 'PriorityClass.scheduling.k8s.io']) {
      expect(checkSyncClusterKind(k), k).not.toBe('');
    }
    for (const k of ['*.fluentbit.fluent.io', 'clusterinput.fluentbit.fluent.io', 'ClusterInput.apps', 'ClusterInput.Fluent.io', '']) {
      expect(checkSyncClusterKind(k), k).toMatch(/Kind\.group/);
    }
  });
  it('parses the text box and finds the first problem', () => {
    expect(parseSyncClusterKinds(' ClusterInput.fluentbit.fluent.io\n\nClusterOutput.fluentbit.fluent.io , ')).toEqual(['ClusterInput.fluentbit.fluent.io', 'ClusterOutput.fluentbit.fluent.io']);
    expect(syncClusterKindsError(['ClusterInput.fluentbit.fluent.io', 'ClusterInput.fluentbit.fluent.io'])).toMatch(/twice/);
    expect(syncClusterKindsError(['ClusterInput.fluentbit.fluent.io'])).toBe('');
  });
});
