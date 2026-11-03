import type { TrafficEdge, TrafficGraph, TrafficNode } from '../api';

export const TRAFFIC_COLUMNS = ['Entry', 'Ingress / route', 'Service', 'Workload'] as const;

/** The column a node is drawn in, by kind. */
export function trafficColumn(kind: string): number {
  switch (kind) {
    case 'Address':
    case 'Gateway':
    case 'Waypoint':
      return 0;
    case 'Ingress':
    case 'HTTPRoute':
    case 'GRPCRoute':
      return 1;
    case 'Service':
      return 2;
    default:
      return 3;
  }
}

const byName = (a: TrafficNode, b: TrafficNode) => a.kind.localeCompare(b.kind) || a.name.localeCompare(b.name) || a.id.localeCompare(b.id);

/**
 * What only traffic inside the cluster reaches: every node that no external
 * entry leads to. Entries are load balancer addresses, Gateways, Ingresses
 * and LoadBalancer or NodePort Services (whose type is in the details);
 * waypoints and the routes attached to Services are mesh traffic.
 */
export function inClusterNodes(g: TrafficGraph): Set<string> {
  const external = new Set(g.nodes
    .filter((n) => n.kind === 'Address' || n.kind === 'Gateway' || n.kind === 'Ingress'
      || (n.kind === 'Service' && (n.details.includes('LoadBalancer') || n.details.includes('NodePort'))))
    .map((n) => n.id));
  const queue = [...external];
  for (let cur = queue.shift(); cur !== undefined; cur = queue.shift()) {
    for (const e of g.edges) {
      if (e.from === cur && !external.has(e.to)) {
        external.add(e.to);
        queue.push(e.to);
      }
    }
  }
  return new Set(g.nodes.filter((n) => !external.has(n.id)).map((n) => n.id));
}

/** The graph without the given nodes and their edges. */
export function withoutNodes(g: TrafficGraph, ids: ReadonlySet<string>): TrafficGraph {
  if (ids.size === 0) return g;
  return { nodes: g.nodes.filter((n) => !ids.has(n.id)), edges: g.edges.filter((e) => !ids.has(e.from) && !ids.has(e.to)) };
}

/**
 * Splits the nodes into the four columns and orders each to keep edges short:
 * Ingresses and routes by name, every later column by the mean position of
 * the nodes pointing to it, the entry column by the mean position of the
 * nodes it points to. Nodes without neighbours go last, by name, and the
 * nodes in last (only reachable inside the cluster) after them.
 */
export function layoutTraffic(g: TrafficGraph, last: ReadonlySet<string> = new Set()): TrafficNode[][] {
  const cols: TrafficNode[][] = TRAFFIC_COLUMNS.map(() => []);
  for (const n of g.nodes) cols[trafficColumn(n.kind)].push(n);
  const pos = new Map<string, number>();
  const place = (col: TrafficNode[]) => col.forEach((n, i) => pos.set(n.id, i));
  const orderBy = (col: TrafficNode[], neighbours: (id: string) => string[]) => {
    const key = new Map(col.map((n) => {
      const ps = neighbours(n.id).map((id) => pos.get(id)).filter((p): p is number => p !== undefined);
      return [n.id, ps.length ? ps.reduce((a, b) => a + b, 0) / ps.length : Infinity];
    }));
    col.sort((a, b) => Number(last.has(a.id)) - Number(last.has(b.id)) || (key.get(a.id)! - key.get(b.id)!) || byName(a, b));
    place(col);
  };
  const preds = (id: string) => g.edges.filter((e) => e.to === id).map((e) => e.from);
  const succs = (id: string) => g.edges.filter((e) => e.from === id).map((e) => e.to);
  cols[1].sort((a, b) => Number(last.has(a.id)) - Number(last.has(b.id)) || byName(a, b));
  place(cols[1]);
  orderBy(cols[2], preds);
  orderBy(cols[3], preds);
  orderBy(cols[0], succs);
  return cols;
}

/** Rules an Ingress or route box shows before "+N more". */
export const TRAFFIC_RULES_SHOWN = 6;

/** Where an Ingress or route sends traffic, as "rule → Service" lines; none for other kinds. */
export function trafficRules(g: TrafficGraph, n: TrafficNode): string[] {
  if (n.kind !== 'Ingress' && !n.kind.endsWith('Route')) return [];
  const name = (id: string) => g.nodes.find((x) => x.id === id)?.name ?? id;
  return g.edges.filter((e) => e.from === n.id).flatMap((e) => e.labels.map((l) => `${l} → ${name(e.to)}`));
}

/** The edges and nodes upstream and downstream of a node (the node included). */
export function trafficPath(g: TrafficGraph, id: string): { nodes: Set<string>; edges: Set<TrafficEdge> } {
  const nodes = new Set([id]);
  const edges = new Set<TrafficEdge>();
  const walk = (forward: boolean) => {
    const queue = [id];
    for (let cur = queue.shift(); cur !== undefined; cur = queue.shift()) {
      for (const e of g.edges) {
        if ((forward ? e.from : e.to) !== cur || edges.has(e)) continue;
        const next = forward ? e.to : e.from;
        edges.add(e);
        nodes.add(next);
        queue.push(next);
      }
    }
  };
  walk(true);
  walk(false);
  return { nodes, edges };
}
