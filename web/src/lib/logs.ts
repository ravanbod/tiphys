export type LogLevel = 'I' | 'W' | 'E' | 'D' | '';

export interface LogLine {
  id: number;
  raw: string;
  ts: string;
  level: LogLevel;
  levelText: string;
  msg: string;
}

const RFC3339 = /^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2}))\s?/;

/** Splits "2026-10-20T13:41:04.771Z message" into timestamp and message. */
export function splitTimestamp(raw: string): { ts: string; rest: string } {
  const m = RFC3339.exec(raw);
  if (!m) return { ts: '', rest: raw };
  return { ts: m[1], rest: raw.slice(m[0].length) };
}

/** "2026-10-20T13:41:04.771Z" -> "13:41:04.771" */
export function shortTs(ts: string): string {
  const m = /T(\d{2}:\d{2}:\d{2}(?:\.\d{1,3})?)/.exec(ts);
  return m ? m[1] : ts;
}

const LEVEL_WORDS: [RegExp, LogLevel, string][] = [
  [/^(ERROR|ERR|FATAL|CRITICAL|PANIC|SEVERE)\b/i, 'E', 'ERROR'],
  [/^(WARN|WARNING)\b/i, 'W', 'WARN'],
  [/^(INFO|NOTICE)\b/i, 'I', 'INFO'],
  [/^(DEBUG|TRACE|DBG)\b/i, 'D', 'DEBUG'],
];

/** Detects a level from a leading word, `level=x`, or a JSON `"level":"x"`. */
export function detectLevel(msg: string): { level: LogLevel; text: string; rest: string } {
  const trimmed = msg.replace(/^\[/, '');
  for (const [re, lv, text] of LEVEL_WORDS) {
    const m = re.exec(trimmed);
    if (m) return { level: lv, text, rest: trimmed.slice(m[0].length).replace(/^\]?\s*/, '') };
  }
  const kv = /\blevel=("?)(\w+)\1/i.exec(msg) ?? /"level"\s*:\s*"(\w+)"/i.exec(msg);
  if (kv) {
    const word = (kv[2] ?? kv[1]) as string;
    for (const [re, lv, text] of LEVEL_WORDS) if (re.test(word)) return { level: lv, text, rest: msg };
  }
  return { level: '', text: '', rest: msg };
}

let nextId = 1;
export function parseLogLine(raw: string, withTimestamps: boolean): LogLine {
  const clean = raw.replace(/\r$/, '');
  let ts = '';
  let body = clean;
  if (withTimestamps) {
    const s = splitTimestamp(clean);
    ts = s.ts;
    body = s.rest;
  }
  const lv = detectLevel(body);
  return { id: nextId++, raw: clean, ts, level: lv.level, levelText: lv.text, msg: lv.level ? lv.rest : body };
}

/** Incremental line splitter for chunked streams (keeps the partial trailing line). */
export class LineSplitter {
  private tail = '';
  push(chunk: string): string[] {
    const text = this.tail + chunk;
    const parts = text.split('\n');
    this.tail = parts.pop() ?? '';
    return parts;
  }
  /** Returns and clears any unterminated final line. */
  flush(): string[] {
    const t = this.tail;
    this.tail = '';
    return t ? [t] : [];
  }
}

export function escapeRegExp(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

/** Splits text into plain/hit segments for case-insensitive highlight. */
export function splitHighlight(text: string, query: string): { text: string; hit: boolean }[] {
  if (!query) return [{ text, hit: false }];
  const re = new RegExp(escapeRegExp(query), 'gi');
  const out: { text: string; hit: boolean }[] = [];
  let last = 0;
  for (let m = re.exec(text); m; m = re.exec(text)) {
    if (m[0] === '') {
      re.lastIndex++;
      continue;
    }
    if (m.index > last) out.push({ text: text.slice(last, m.index), hit: false });
    out.push({ text: m[0], hit: true });
    last = m.index + m[0].length;
  }
  if (last < text.length) out.push({ text: text.slice(last), hit: false });
  return out.length ? out : [{ text, hit: false }];
}

export function countMatches(lines: { raw: string }[], query: string): number[] {
  if (!query) return [];
  const q = query.toLowerCase();
  const idx: number[] = [];
  lines.forEach((l, i) => {
    if (l.raw.toLowerCase().includes(q)) idx.push(i);
  });
  return idx;
}

export const TAIL_OPTIONS = [100, 1000, 10000] as const;
export const SINCE_OPTIONS: { label: string; seconds: number }[] = [
  { label: '5 min', seconds: 300 },
  { label: '1 hour', seconds: 3600 },
  { label: '24 hours', seconds: 86400 },
];
