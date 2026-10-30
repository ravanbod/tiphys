import { describe, expect, it } from 'vitest';
import type { TrafficGraph, TrafficNode } from '../api';
import { layoutTraffic, trafficColumn, trafficPath, trafficRules } from './traffic';

const n = (kind: string, name: string): TrafficNode => ({
  id: `${kind}/shop/${name}`, kind, namespace: 'shop', name, details: [], ready: 0, total: 0, problem: '',
});
const e = (from: TrafficNode, to: TrafficNode) => ({ from: from.id, to: to.id, labels: [] });

const addr = n('Address', '1.2.3.4');
const gw = n('Gateway', 'public');
const ingB = n('Ingress', 'b');
const routeA = n('HTTPRoute', 'a');
const svcX = n('Service', 'x');
const svcY = n('Service', 'y');
const lone = n('Service', 'lone');
const depX = n('Deployment', 'x');
const depY = n('Deployment', 'y');
const g: TrafficGraph = {
  nodes: [depY, depX, lone, svcX, svcY, routeA, ingB, gw, addr],
  edges: [e(addr, ingB), e(gw, routeA), e(ingB, svcY), e(routeA, svcX), e(svcX, depX), e(svcY, depY)],
};

describe('trafficColumn', () => {
  it('puts entries, routing, Services and workloads in order', () => {
    expect(['Address', 'Gateway', 'Ingress', 'GRPCRoute', 'Service', 'Rollout', 'Pod'].map(trafficColumn)).toEqual([0, 0, 1, 1, 2, 3, 3]);
  });
});

describe('layoutTraffic', () => {
  it('orders each column after the nodes it is connected to', () => {
    const cols = layoutTraffic(g).map((c) => c.map((x) => x.name));
    expect(cols).toEqual([['public', '1.2.3.4'], ['a', 'b'], ['x', 'y', 'lone'], ['x', 'y']]);
  });
});

describe('trafficPath', () => {
  it('follows a node upstream and downstream only', () => {
    const p = trafficPath(g, svcX.id);
    expect([...p.nodes].sort()).toEqual([depX.id, gw.id, routeA.id, svcX.id].sort());
    expect(p.edges.size).toBe(3);
  });
});

describe('trafficRules', () => {
  it('lists the rules of Ingresses and routes with their Service', () => {
    const labelled: TrafficGraph = { ...g, edges: [{ from: ingB.id, to: svcY.id, labels: ['a.example.com/', 'a.example.com/api'] }] };
    expect(trafficRules(labelled, ingB)).toEqual(['a.example.com/ → y', 'a.example.com/api → y']);
    expect(trafficRules(labelled, svcY)).toEqual([]);
  });
});
