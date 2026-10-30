import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react';
import { Link } from 'react-router-dom';
import type { TrafficGraph, TrafficNode, WorkloadKind } from '../api';
import { KindTag } from './Badges';
import { workloadUrl } from '../lib/routes';
import { TRAFFIC_COLUMNS, TRAFFIC_RULES_SHOWN, layoutTraffic, trafficPath, trafficRules } from '../lib/traffic';

const WORKLOAD_KINDS: string[] = ['Deployment', 'StatefulSet', 'DaemonSet', 'Rollout', 'CronJob'] satisfies WorkloadKind[];

interface Line { key: string; d: string; bad: boolean; title: string }

/**
 * The namespace's traffic flow drawn like Argo CD's network view: entry
 * points → Ingresses and routes → Services → workloads. Boxes are laid out in
 * columns by CSS; the connecting lines are measured from the boxes after
 * every render and whenever a box changes size. Hovering a box highlights its
 * path; clicking boxes pins theirs (clicking again, the background or Escape
 * unpins), and hovering only previews while nothing is pinned. A box's "+N more" shows all its rules; allOpen does so for every box,
 * and a box toggled on its own differs from allOpen until allOpen changes.
 */
export function TrafficFlow({ graph, cluster, ns, allOpen }: { graph: TrafficGraph; cluster: string; ns: string; allOpen: boolean }) {
  const cols = layoutTraffic(graph).map((nodes, i) => ({ title: TRAFFIC_COLUMNS[i], nodes })).filter((c) => c.nodes.length);
  const [toggled, setToggled] = useState({ allOpen, ids: new Set<string>() });
  if (toggled.allOpen !== allOpen) setToggled({ allOpen, ids: new Set() });
  const toggle = (id: string) => setToggled((t) => {
    const ids = new Set(t.ids);
    if (!ids.delete(id)) ids.add(id);
    return { ...t, ids };
  });
  const box = useRef<HTMLDivElement>(null);
  const boxes = useRef(new Map<string, HTMLElement>());
  const [lines, setLines] = useState<Line[]>([]);
  const [hover, setHover] = useState<string | null>(null);
  const [pinned, setPinned] = useState<string[]>([]);
  const shown = pinned.filter((id) => graph.nodes.some((n) => n.id === id));
  const focus = shown.length ? shown : hover ? [hover] : [];
  const path = focus.length ? union(focus.map((id) => trafficPath(graph, id))) : null;
  const pin = (id: string) => setPinned((p) => (p.includes(id) ? p.filter((x) => x !== id) : [...p, id]));

  useEffect(() => {
    if (!shown.length) return;
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') setPinned([]); };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [shown.length]);

  const measure = useCallback(() => {
    const root = box.current?.getBoundingClientRect();
    if (!root) return;
    const bad = new Set(graph.nodes.filter((n) => n.problem).map((n) => n.id));
    const out: Line[] = [];
    for (const e of graph.edges) {
      const a = boxes.current.get(e.from)?.getBoundingClientRect();
      const b = boxes.current.get(e.to)?.getBoundingClientRect();
      if (!a || !b) continue;
      const x1 = a.right - root.left, y1 = a.top + a.height / 2 - root.top;
      const x2 = b.left - root.left, y2 = b.top + b.height / 2 - root.top;
      const dx = Math.max(24, (x2 - x1) / 2);
      out.push({
        key: `${e.from}>${e.to}`,
        d: `M${x1},${y1} C${x1 + dx},${y1} ${x2 - dx},${y2} ${x2},${y2}`,
        bad: bad.has(e.to),
        title: e.labels.join(', '),
      });
    }
    setLines(out);
  }, [graph]);

  useLayoutEffect(() => {
    measure();
    const el = box.current;
    if (!el) return;
    // Expanding a box moves the boxes below it without resizing the graph.
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    boxes.current.forEach((b) => ro.observe(b));
    return () => ro.disconnect();
  }, [measure]);

  const edgeOn = (key: string) => !path || [...path.edges].some((e) => `${e.from}>${e.to}` === key);

  return (
    <div className="tbl-wrap">
      <div
        className="tf"
        ref={box}
        style={{ gridTemplateColumns: `repeat(${cols.length}, minmax(180px, 1fr))` }}
        onClick={(e) => { if (e.target === e.currentTarget || (e.target as HTMLElement).classList.contains('tf-col')) setPinned([]); }}
      >
        {shown.length > 0 && (
          <div className="tf-sel">
            {shown.length} selected
            <button type="button" className="tf-more" onClick={() => setPinned([])}>Clear</button>
          </div>
        )}
        <svg aria-hidden="true">
          {lines.map((l) => (
            <path key={l.key} d={l.d} className={[l.bad && 'bad', path && (edgeOn(l.key) ? 'on' : 'dim')].filter(Boolean).join(' ')}>
              {l.title && <title>{l.title}</title>}
            </path>
          ))}
        </svg>
        {cols.map((c) => (
          <div key={c.title} className="tf-col">
            <div className="tf-hd">{c.title}</div>
            {c.nodes.map((n) => (
              <div
                key={n.id}
                ref={(el) => { if (el) boxes.current.set(n.id, el); else boxes.current.delete(n.id); }}
                className={`tf-n${n.problem ? ' bad' : ''}${shown.includes(n.id) ? ' pin' : ''}${path && !path.nodes.has(n.id) ? ' dim' : ''}`}
                onMouseEnter={() => setHover(n.id)}
                onMouseLeave={() => setHover(null)}
                // Links and "+N more" keep their own meaning; selecting text to copy does not pin.
                onClick={(e) => { if (!(e.target as HTMLElement).closest('a, button') && !window.getSelection()?.toString()) pin(n.id); }}
              >
                <NodeBody n={n} graph={graph} cluster={cluster} ns={ns} open={allOpen !== toggled.ids.has(n.id)} onToggle={() => toggle(n.id)} />
              </div>
            ))}
          </div>
        ))}
      </div>
    </div>
  );
}

function union(paths: ReturnType<typeof trafficPath>[]): ReturnType<typeof trafficPath> {
  return { nodes: new Set(paths.flatMap((p) => [...p.nodes])), edges: new Set(paths.flatMap((p) => [...p.edges])) };
}

function NodeBody({ n, graph, cluster, ns, open, onToggle }: {
  n: TrafficNode; graph: TrafficGraph; cluster: string; ns: string; open: boolean; onToggle: () => void;
}) {
  const isWorkload = WORKLOAD_KINDS.includes(n.kind) && n.namespace === ns;
  const name = isWorkload
    ? <Link className="mono" title={n.name} to={workloadUrl({ cluster, namespace: ns, kind: n.kind as WorkloadKind, name: n.name })}><b>{n.name}</b></Link>
    : <b className="mono" title={n.name}>{n.name}</b>;
  const rules = trafficRules(graph, n);
  const more = rules.length - TRAFFIC_RULES_SHOWN;
  const pods = n.kind !== 'Address' && n.kind !== 'Gateway' && n.total > 0;
  return (
    <>
      <div className="t">
        <KindTag kind={n.kind === 'Address' ? 'LB address' : n.kind} />
        {name}
      </div>
      {n.namespace && n.namespace !== ns && <small>in <span className="mono">{n.namespace}</span></small>}
      {n.details.map((d) => <small key={d}>{d}</small>)}
      {(open ? rules : rules.slice(0, TRAFFIC_RULES_SHOWN)).map((r) => <small key={r} className={open ? 'mono' : 'mono r'} title={r}>{r}</small>)}
      {more > 0 && (
        <button type="button" className="tf-more" aria-expanded={open} onClick={onToggle}>
          {open ? 'Show less' : `+${more} more`}
        </button>
      )}
      {pods && (
        <span className={`chip ${n.ready === n.total ? 'ok' : n.ready === 0 ? 'err' : 'warn'}`} style={{ alignSelf: 'flex-start' }}>
          {n.ready}/{n.total} ready
        </span>
      )}
      {n.problem && <span className="p">{n.problem}</span>}
    </>
  );
}
