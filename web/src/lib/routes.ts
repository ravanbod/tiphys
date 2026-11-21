import { kindSegment } from '../api/query';

const e = encodeURIComponent;

export interface WlLoc {
  cluster: string;
  namespace: string;
  kind: string;
  name: string;
}

export function workloadUrl(w: WlLoc, tab?: string): string {
  const base = `/c/${e(w.cluster)}/n/${e(w.namespace)}/w/${kindSegment(w.kind)}/${e(w.name)}`;
  return tab ? `${base}?tab=${tab}` : base;
}
export function logsUrl(w: WlLoc, pod?: string, container?: string, extra: Record<string, string> = {}): string {
  const p = new URLSearchParams({ ...(pod ? { pod } : {}), ...(container ? { container } : {}), ...extra });
  const s = p.toString();
  return `${workloadUrl(w)}/logs${s ? `?${s}` : ''}`;
}
export function execUrl(w: WlLoc, pod?: string, container?: string, debug = false): string {
  const p = new URLSearchParams({ ...(pod ? { pod } : {}), ...(container ? { container } : {}), ...(debug ? { mode: 'debug' } : {}) });
  const s = p.toString();
  return `${workloadUrl(w)}/exec${s ? `?${s}` : ''}`;
}
/** The logs of a pod no workload owns (OtherPod). */
export function otherPodLogsUrl(cluster: string, ns: string, pod: string): string {
  return `/c/${e(cluster)}/n/${e(ns)}/pods/logs?${new URLSearchParams({ pod })}`;
}
export function namespaceUrl(cluster: string, ns: string): string {
  return `/c/${e(cluster)}/n/${e(ns)}`;
}
export function cronJobsUrl(cluster: string, ns: string): string {
  return `/c/${e(cluster)}/n/${e(ns)}/cronjobs`;
}
