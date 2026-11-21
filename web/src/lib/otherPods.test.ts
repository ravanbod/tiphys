import { describe, expect, it } from 'vitest';
import type { OtherPod } from '../api/types';
import { filterOtherPods, groupByOwner, ownerKind, siblings } from './otherPods';

const pg = { group: 'postgresql.cnpg.io', kind: 'Cluster', name: 'pg-main' };
const pod = (name: string, owner: OtherPod['owner'], ready = '1/1'): OtherPod => ({
  name, owner, ready, phase: 'Running', restarts: 0, node: 'n1', createdAt: '', lastTermination: null, lastTerminatedAt: null, job: null, containers: [],
});
const pods = [pod('debug', null), pod('tmp', null, '0/1'), pod('pg-main-1', pg), pod('pg-main-2', pg, '0/1'), pod('pg-main-3', pg)];

describe('other pods', () => {
  it('groups by owner and counts ready pods', () => {
    const g = groupByOwner(pods);
    expect(g.map((x) => [x.key, x.pods.length, x.ready])).toEqual([['', 2, 1], ['Cluster.postgresql.cnpg.io/pg-main', 3, 2]]);
    expect(ownerKind(pg)).toBe('Cluster (postgresql.cnpg.io)');
    expect(ownerKind({ group: '', kind: 'Node', name: 'n1' })).toBe('Node');
    expect(ownerKind(null)).toBe('No controller');
  });
  it('picks the pods of the same owner for the log picker', () => {
    expect(siblings(pods, 'pg-main-2').map((p) => p.name)).toEqual(['pg-main-1', 'pg-main-2', 'pg-main-3']);
    expect(siblings(pods, 'debug').map((p) => p.name)).toEqual(['debug']);
    expect(siblings(pods, 'gone')).toHaveLength(5);
  });
  it('filters by pod or owner name', () => {
    expect(filterOtherPods(pods, 'PG-MAIN').map((p) => p.name)).toEqual(['pg-main-1', 'pg-main-2', 'pg-main-3']);
    expect(filterOtherPods(pods, 'deb').map((p) => p.name)).toEqual(['debug']);
    expect(filterOtherPods(pods, ' ')).toHaveLength(5);
  });
});
