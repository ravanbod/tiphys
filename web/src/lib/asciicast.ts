/** Minimal asciicast v2 helpers for the replay side panel and search. */

export interface CastHeader {
  version: number;
  width: number;
  height: number;
  timestamp?: number;
  title?: string;
}
export type CastEvent = { t: number; code: 'o' | 'i' | 'r' | 'm' | string; data: string };

export interface ParsedCast {
  header: CastHeader | null;
  events: CastEvent[];
  duration: number;
}

export function parseAsciicast(text: string): ParsedCast {
  const lines = text.split('\n');
  let header: CastHeader | null = null;
  const events: CastEvent[] = [];
  for (const line of lines) {
    const s = line.trim();
    if (!s) continue;
    try {
      const v = JSON.parse(s) as unknown;
      if (!header && v && typeof v === 'object' && !Array.isArray(v)) {
        const o = v as Record<string, unknown>;
        header = {
          version: Number(o.version ?? 2),
          width: Number(o.width ?? 80),
          height: Number(o.height ?? 24),
          timestamp: typeof o.timestamp === 'number' ? o.timestamp : undefined,
          title: typeof o.title === 'string' ? o.title : undefined,
        };
      } else if (Array.isArray(v) && v.length >= 3 && typeof v[0] === 'number') {
        events.push({ t: v[0], code: String(v[1]), data: typeof v[2] === 'string' ? v[2] : String(v[2]) });
      }
    } catch {
      /* skip a corrupt line (a crash can lose the tail of a segment) */
    }
  }
  const duration = events.length ? events[events.length - 1].t : 0;
  return { header, events, duration };
}

export interface DerivedCommand {
  offsetSeconds: number;
  text: string;
}

/** Best-effort: reconstruct typed commands from input events (split at Enter). */
export function deriveCommands(events: CastEvent[]): DerivedCommand[] {
  const out: DerivedCommand[] = [];
  let buf = '';
  let startT = 0;
  for (const ev of events) {
    if (ev.code !== 'i') continue;
    const d = ev.data;
    for (let i = 0; i < d.length; i++) {
      const ch = d[i];
      if (ch === '\x1b') {
        // skip CSI / SS3 sequences (arrows, home, end...)
        if (d[i + 1] === '[' || d[i + 1] === 'O') {
          i += 2;
          while (i < d.length && !/[A-Za-z~]/.test(d[i])) i++;
        }
        continue;
      }
      if (ch === '\r' || ch === '\n') {
        const text = buf.trim();
        if (text) out.push({ offsetSeconds: startT, text });
        buf = '';
        continue;
      }
      if (ch === '\x7f' || ch === '\b') {
        buf = buf.slice(0, -1);
        continue;
      }
      if (ch === '\x03' || ch === '\x15' || ch === '\x04') {
        buf = '';
        continue;
      }
      if (ch === '\t') continue;
      if (ch < ' ') continue;
      if (!buf) startT = ev.t;
      buf += ch;
    }
  }
  return out;
}

// eslint-disable-next-line no-control-regex
const ANSI = /\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07]*\x07|\x1b[()][A-Za-z0-9]/g;

export function stripAnsi(s: string): string {
  return s.replace(ANSI, '');
}

/** Times (seconds) of output events whose text contains `query` (case-insensitive, across event boundaries). */
export function findOutputMatches(events: CastEvent[], query: string): number[] {
  if (!query) return [];
  let text = '';
  const starts: { at: number; t: number }[] = [];
  for (const ev of events) {
    if (ev.code !== 'o') continue;
    starts.push({ at: text.length, t: ev.t });
    text += stripAnsi(ev.data);
  }
  const hay = text.toLowerCase();
  const q = query.toLowerCase();
  const result: number[] = [];
  let from = 0;
  for (;;) {
    const i = hay.indexOf(q, from);
    if (i < 0) break;
    // binary search for the event containing index i
    let lo = 0;
    let hi = starts.length - 1;
    while (lo < hi) {
      const mid = (lo + hi + 1) >> 1;
      if (starts[mid].at <= i) lo = mid;
      else hi = mid - 1;
    }
    const t = starts[lo]?.t ?? 0;
    if (result[result.length - 1] !== t) result.push(t);
    from = i + q.length;
  }
  return result;
}
