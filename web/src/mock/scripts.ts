/** Canned terminal content for the fake exec/watch sockets and the fake recordings. */
export interface ScriptStep {
  at: number; // seconds
  input?: string;
  output: string;
}

const PROMPT = (pod: string) => `\x1b[32mapp@${pod}\x1b[0m:\x1b[34m/data\x1b[0m$ `;

export function replayScript(pod: string): ScriptStep[] {
  const p = PROMPT(pod);
  return [
    { at: 0.3, output: `${p}` },
    { at: 41, input: 'df -h /data', output: 'Filesystem      Size  Used Avail Use% Mounted on\r\n/dev/nvme1n1    200G  192G  8.0G  97% /data\r\n' + p },
    { at: 83, input: 'ls shards/4/', output: 'segments_12  segments_13  write.lock  _7a.cfs  _7a.si\r\n' + p },
    { at: 192, input: 'stat shards/4/write.lock', output: '  File: shards/4/write.lock\r\n  Size: 0         \tBlocks: 0          IO Block: 4096   regular empty file\r\nModify: 2026-10-20 12:52:03.000000000 +0330\r\n' + p },
    { at: 290, input: 'ps -o pid,etime,cmd -C indexer', output: '  PID     ELAPSED CMD\r\n    1    03:11:42 /usr/local/bin/indexer --shard=4\r\n' + p },
    { at: 411, input: 'cat shards/4/segments_13 | head', output: 'segment-v3 docs=1204411 deleted=2210\r\nlock-owner pid=1 host=search-indexer-6f7d9-q8zcl\r\n' + p },
    { at: 575, input: 'exit', output: 'exit\r\n' },
  ];
}

/** asciicast v2 text for a session. */
export function buildCast(pod: string, startedAtSec: number): string {
  const steps = replayScript(pod);
  const lines: string[] = [JSON.stringify({ version: 2, width: 100, height: 28, timestamp: Math.floor(startedAtSec), env: { SHELL: '/bin/sh', TERM: 'xterm-256color' }, title: pod })];
  for (const s of steps) {
    if (s.input) {
      // typed one character at a time, then Enter, then the output
      let t = s.at - 0.5;
      for (const ch of s.input) {
        t += 0.06;
        lines.push(JSON.stringify([Number(t.toFixed(3)), 'i', ch]));
        lines.push(JSON.stringify([Number(t.toFixed(3)), 'o', ch]));
      }
      lines.push(JSON.stringify([Number((s.at).toFixed(3)), 'i', '\r']));
      lines.push(JSON.stringify([Number((s.at).toFixed(3)), 'o', '\r\n']));
      lines.push(JSON.stringify([Number((s.at + 0.2).toFixed(3)), 'o', s.output]));
    } else {
      lines.push(JSON.stringify([s.at, 'o', s.output]));
    }
  }
  return lines.join('\n') + '\n';
}

export function commandList(): { offsetSeconds: number; text: string }[] {
  return replayScript('x').filter((s) => s.input).map((s) => ({ offsetSeconds: s.at, text: s.input as string }));
}

export const LOG_TEMPLATES: [string, string][] = [
  ['INFO', 'http   POST /v1/charges 201 {ms}ms req={req}'],
  ['INFO', 'ledger posted entry le_{n} amount={amt} currency=EUR'],
  ['INFO', 'http   GET  /v1/charges/ch_{hex} 200 {ms}ms req={req}'],
  ['WARN', 'gateway upstream latency p99=1.{d}s host=psp-a.internal'],
  ['INFO', 'http   GET  /healthz 200 1ms'],
  ['DEBUG', 'pool   db connections active={d}{d} idle=6 waiting=0'],
  ['ERROR', 'psp    timeout calling psp-a.internal after 2000ms (attempt 1/3) req={req}'],
  ['WARN', 'memory rss=468Mi limit=512Mi (91%) gc pause=212ms'],
  ['INFO', 'http   POST /v1/refunds 201 96ms req={req}'],
];
