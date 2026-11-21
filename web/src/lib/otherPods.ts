import type { OtherPod, PodOwner } from '../api';

export const ownerKey = (o: PodOwner | null) => (o ? `${o.kind}.${o.group}/${o.name}` : '');

/** "Cluster (postgresql.cnpg.io)": the owner's kind with its API group. */
export const ownerKind = (o: PodOwner | null) => (o ? (o.group ? `${o.kind} (${o.group})` : o.kind) : 'No controller');

export interface OwnerGroup {
  key: string;
  owner: PodOwner | null;
  pods: OtherPod[];
  /** Pods whose containers are all ready. */
  ready: number;
}

export const podReady = (p: OtherPod) => {
  const [r, t] = p.ready.split('/');
  return t !== undefined && r === t && t !== '0';
};

/** The pods grouped by owner, in the server's order (by owner, then name). */
export function groupByOwner(pods: OtherPod[]): OwnerGroup[] {
  const out: OwnerGroup[] = [];
  for (const p of pods) {
    const key = ownerKey(p.owner);
    let g = out.find((x) => x.key === key);
    if (!g) out.push((g = { key, owner: p.owner, pods: [], ready: 0 }));
    g.pods.push(p);
    if (podReady(p)) g.ready++;
  }
  return out;
}

/** The pods sharing the pod's owner, for the log page's picker (a pod without one has only itself); all when the pod is not listed. */
export function siblings(pods: OtherPod[], pod: string | null): OtherPod[] {
  const p = pods.find((x) => x.name === pod);
  if (!p) return pods;
  if (!p.owner) return [p];
  const key = ownerKey(p.owner);
  return pods.filter((x) => ownerKey(x.owner) === key);
}

/** Filters by pod or owner name. */
export function filterOtherPods(pods: OtherPod[], q: string): OtherPod[] {
  const l = q.trim().toLowerCase();
  if (!l) return pods;
  return pods.filter((p) => p.name.toLowerCase().includes(l) || !!p.owner?.name.toLowerCase().includes(l));
}
