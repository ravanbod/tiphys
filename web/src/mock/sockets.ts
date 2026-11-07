import type { EndReason, K8sEvent, ServerControl } from '../api/types';
import { kindFromSegment } from '../api/query';
import { CSRF, MIN, currentUser, getDb } from './db';
import { decide } from './decide';
import { LOG_TEMPLATES, replayScript } from './scripts';

const iso = (t: number) => new Date(t).toISOString();

// ---- session registry (terminate / watchers)
const live = new Map<string, Set<(reason: EndReason, message: string) => void>>();
function register(id: string, fn: (reason: EndReason, message: string) => void) {
  if (!live.has(id)) live.set(id, new Set());
  live.get(id)!.add(fn);
  return () => live.get(id)?.delete(fn);
}
export function terminateSocket(id: string, reason: EndReason) {
  for (const fn of live.get(id) ?? []) fn(reason, 'An administrator ended this session.');
}

type Listener = (ev: Event) => void;

class FakeSocket {
  static CONNECTING = 0;
  static OPEN = 1;
  static CLOSING = 2;
  static CLOSED = 3;
  CONNECTING = 0;
  OPEN = 1;
  CLOSING = 2;
  CLOSED = 3;
  readyState = 0;
  binaryType: BinaryType = 'blob';
  url: string;
  protocol = '';
  extensions = '';
  bufferedAmount = 0;
  onopen: Listener | null = null;
  onmessage: ((ev: MessageEvent) => void) | null = null;
  onclose: ((ev: CloseEvent) => void) | null = null;
  onerror: Listener | null = null;
  private timers: number[] = [];
  private cleanups: (() => void)[] = [];
  private onSend: ((data: string | Uint8Array) => void) | null = null;
  private listeners: Record<string, Listener[]> = {};

  constructor(url: string) {
    this.url = url;
    this.timer(() => this.boot(), 120);
  }
  addEventListener(type: string, fn: Listener) { (this.listeners[type] ??= []).push(fn); }
  removeEventListener(type: string, fn: Listener) { this.listeners[type] = (this.listeners[type] ?? []).filter((f) => f !== fn); }
  private fire(type: string, ev: Event) {
    (this as unknown as Record<string, ((e: Event) => void) | null>)['on' + type]?.(ev);
    for (const f of this.listeners[type] ?? []) f(ev);
  }
  protected timer(fn: () => void, ms: number): number {
    const t = window.setTimeout(fn, ms);
    this.timers.push(t);
    return t;
  }
  protected every(fn: () => void, ms: number): number {
    const t = window.setInterval(fn, ms);
    this.timers.push(t);
    return t;
  }
  protected cleanup(fn: () => void) { this.cleanups.push(fn); }
  protected handleSend(fn: (data: string | Uint8Array) => void) { this.onSend = fn; }

  protected open() {
    this.readyState = 1;
    this.fire('open', new Event('open'));
  }
  protected control(m: ServerControl) {
    if (this.readyState !== 1) return;
    this.fire('message', new MessageEvent('message', { data: JSON.stringify(m) }));
  }
  protected bytes(data: string | Uint8Array) {
    if (this.readyState !== 1) return;
    const u = typeof data === 'string' ? new TextEncoder().encode(data) : data;
    const buf = u.buffer.slice(u.byteOffset, u.byteOffset + u.byteLength) as ArrayBuffer;
    this.fire('message', new MessageEvent('message', { data: buf }));
  }
  /** Server-side end: send `end` then close normally. */
  protected endAndClose(reason: EndReason, message: string) {
    this.control({ type: 'end', reason, message });
    this.timer(() => this.finish(1000), 30);
  }
  protected failHandshake() {
    this.finish(1006);
  }
  private finish(code: number) {
    if (this.readyState === 3) return;
    this.readyState = 3;
    this.timers.forEach((t) => { window.clearTimeout(t); window.clearInterval(t); });
    this.cleanups.forEach((c) => c());
    this.fire('close', new CloseEvent('close', { code, wasClean: code === 1000 }));
  }
  send(data: string | ArrayBufferLike | Uint8Array) {
    if (this.readyState !== 1) return;
    if (typeof data === 'string') this.onSend?.(data);
    else this.onSend?.(data instanceof Uint8Array ? data : new Uint8Array(data as ArrayBuffer));
  }
  close(code = 1000) {
    if (this.readyState >= 2) return;
    this.finish(code);
  }
  protected boot() { /* overridden */ }
}

function parse(url: string) {
  const u = new URL(url, window.location.href);
  return { path: u.pathname.replace(/^\/api\/v1/, ''), q: u.searchParams };
}

class ExecSocket extends FakeSocket {
  protected boot() {
    const { path, q } = parse(this.url);
    const m = /^\/clusters\/([^/]+)\/namespaces\/([^/]+)\/workloads\/([^/]+)\/([^/]+)\/pods\/([^/]+)\/(exec|debug)$/.exec(path);
    if (!m || q.get('csrf') !== CSRF) return this.failHandshake();
    const [, cluster, ns, kindSeg, name, pod, mode] = m.map(decodeURIComponent);
    const user = currentUser();
    const kind = kindFromSegment(kindSeg) as never;
    const info = decide(user, cluster, ns, kind, name, 'exec');
    this.open();
    if (!info.allowed) return this.endAndClose('denied', `Exec was refused: ${info.reason.replace('_', ' ')}.`);
    if (window.localStorage.getItem('tiphys.mockRecordingDown') === '1') return this.endAndClose('recording_failed', 'Session recording storage is unavailable, so exec is refused.');
    const d = getDb();
    const now = Date.now();
    const demoWarn = pod.endsWith('2kq8x') && info.environment === 'prod';
    let closes = now + d.settings.execMaxSessionMinutes * MIN;
    let closeReason: EndReason = 'max_duration';
    if (info.closesAt && Date.parse(info.closesAt) < closes) {
      closes = Date.parse(info.closesAt);
      closeReason = info.breakGlassId ? 'break_glass_ended' : 'grant_expired';
    }
    if (demoWarn) { closes = now + 5 * MIN + 20_000; closeReason = 'max_duration'; }
    const sessionId = 'sess_' + Math.random().toString(36).slice(2, 8);
    const debugContainer = mode === 'debug' ? `tiphys-debug-${sessionId.slice(5)}` : null;
    const container = q.get('container') ?? 'app';
    const shell = q.get('shell') ?? '/bin/sh';
    d.sessions.unshift({
      id: sessionId, userId: user.id, username: user.username, cluster, namespace: ns, workloadKind: kind, workloadName: name, pod, container, shell, debugContainer,
      environment: info.environment, breakGlassId: info.breakGlassId, startedAt: iso(now), endedAt: null, durationSeconds: null, endReason: null, recordingBytes: 0, active: true,
    });
    d.audit.unshift({
      id: d.nextId.audit++, at: iso(now), userId: user.id, username: user.username, eventType: 'exec.started', action: 'exec', cluster, namespace: ns, workloadKind: kindSeg, workloadName: name,
      pod, container, environment: info.environment, result: 'succeeded', reason: null, breakGlassId: info.breakGlassId, sessionId, details: debugContainer ? { shell, debugContainer } : { shell }, sourceIp: '10.20.4.7', userAgent: navigator.userAgent,
    });
    const endSession = (reason: EndReason) => {
      const s = d.sessions.find((x) => x.id === sessionId);
      if (s && s.active) Object.assign(s, { active: false, endedAt: iso(Date.now()), endReason: reason, durationSeconds: Math.round((Date.now() - now) / 1000) });
    };
    this.cleanup(() => endSession('client_closed'));
    this.control({ type: 'ready', sessionId, startedAt: iso(now), closesAt: iso(closes), idleTimeoutSeconds: d.settings.execIdleTimeoutMinutes * 60, maxDurationSeconds: d.settings.execMaxSessionMinutes * 60, environment: info.environment, breakGlassId: info.breakGlassId });
    const warnAt = closes - 5 * MIN;
    this.timer(() => this.control({ type: 'warning', code: 'closing_soon', closesAt: iso(closes), reason: closeReason, message: closeReason === 'max_duration' ? 'The maximum session length is about to be reached.' : 'Your access is about to end.' }), Math.max(300, warnAt - Date.now()));
    this.timer(() => { endSession(closeReason); this.endAndClose(closeReason, ''); }, Math.max(500, closes - Date.now()));
    const stop = register(sessionId, (r, msg) => { endSession(r); this.endAndClose(r, msg); });
    this.cleanup(stop);

    const prompt = `\x1b[32mapp@${pod}\x1b[0m:\x1b[34m/srv\x1b[0m$ `;
    this.bytes(`\x1b[90mConnected to ${pod} · container ${container} · ${shell}\x1b[0m\r\n\x1b[90mSession recorded · ${sessionId}\x1b[0m\r\n\r\n${prompt}`);
    let buf = '';
    this.handleSend((data) => {
      if (typeof data === 'string') return; // control frames: resize / ping
      const s = new TextDecoder().decode(data);
      for (const ch of s) {
        if (ch === '\r') {
          const line = buf.trim();
          buf = '';
          this.bytes('\r\n');
          const exit = this.run(line, pod);
          if (exit) return this.timer(() => { endSession('process_exited'); this.endAndClose('process_exited', 'The shell exited.'); }, 150);
          this.bytes(prompt);
        } else if (ch === '\x7f') {
          if (buf) { buf = buf.slice(0, -1); this.bytes('\b \b'); }
        } else if (ch === '\x03') {
          buf = '';
          this.bytes('^C\r\n' + prompt);
        } else if (ch >= ' ' && ch !== '\x1b') {
          buf += ch;
          this.bytes(ch);
        }
      }
    });
  }
  private run(line: string, pod: string): boolean {
    if (!line) return false;
    const [cmd, ...args] = line.split(/\s+/);
    const out = (t: string) => this.bytes(t.replace(/\n/g, '\r\n') + '\r\n');
    switch (cmd) {
      case 'exit': return true;
      case 'ls': out('app.jar  config  logs  tmp'); break;
      case 'pwd': out('/srv'); break;
      case 'whoami': out('app'); break;
      case 'id': out('uid=1000(app) gid=1000(app) groups=1000(app)'); break;
      case 'hostname': out(pod); break;
      case 'date': out(new Date().toString()); break;
      case 'echo': out(args.join(' ')); break;
      case 'env': out('PSP_A_HOST=psp-a.internal\nPSP_B_HOST=psp-b.internal\nPSP_TIMEOUT_MS=2000'); break;
      case 'clear': this.bytes('\x1b[2J\x1b[H'); break;
      case 'help': out('mock shell: ls pwd whoami id hostname date echo env clear exit'); break;
      default: out(`sh: ${cmd}: not found`);
    }
    return false;
  }
}

function logLine(i: number, withTs: boolean, at: number): string {
  const [lv, tpl] = LOG_TEMPLATES[(i * 7 + (i >> 2)) % LOG_TEMPLATES.length];
  const hex = (n: number) => (((i + 1) * 2654435761) >>> 0).toString(16).slice(0, n).padStart(n, '0');
  const text = tpl
    .replace('{ms}', String(12 + ((i * 37) % 400))).replace(/\{req\}/g, '8f3a' + hex(2)).replace('{n}', String(99200 + i))
    .replace('{amt}', String(((i * 131) % 5000) + 100)).replace('{hex}', hex(4)).replace(/\{d\}/g, String((i * 3) % 9));
  const ts = withTs ? iso(at) + ' ' : '';
  return `${ts}${lv.padEnd(5)} ${text}\n`;
}

class LogSocket extends FakeSocket {
  protected boot() {
    const { path, q } = parse(this.url);
    const m = /^\/clusters\/([^/]+)\/namespaces\/([^/]+)\/workloads\/([^/]+)\/([^/]+)\/pods\/([^/]+)\/logs\/stream$/.exec(path);
    if (!m || q.get('csrf') !== CSRF) return this.failHandshake();
    const [, cluster, ns, kindSeg, name] = m.map(decodeURIComponent);
    const info = decide(currentUser(), cluster, ns, kindFromSegment(kindSeg) as never, name, 'logs');
    this.open();
    if (!info.allowed) return this.endAndClose('denied', `Logs were refused: ${info.reason.replace('_', ' ')}.`);
    const now = Date.now();
    this.control({ type: 'ready', startedAt: iso(now), closesAt: info.closesAt, environment: info.environment, breakGlassId: info.breakGlassId });
    const tail = Math.min(Number(q.get('tailLines') ?? 1000) || 1000, 2000);
    const withTs = q.get('timestamps') === 'true';
    const previous = q.get('previous') === 'true';
    let i = 0;
    const chunk = (n: number, ending: number) => {
      let s = '';
      for (let k = 0; k < n; k++, i++) s += logLine(i, withTs, ending - (n - k) * 1000);
      return s;
    };
    const base = previous ? now - 2 * 3600_000 : now;
    for (let off = 0; off < tail; off += 250) this.bytes(chunk(Math.min(250, tail - off), base - (tail - off - Math.min(250, tail - off)) * 1000));
    if (Number(q.get('tailLines')) >= 10000) this.control({ type: 'truncated' });
    if (previous || q.get('follow') === 'false') return this.timer(() => this.endAndClose('process_exited', 'End of logs.'), 200);
    this.every(() => this.bytes(chunk(1 + Math.floor(Math.random() * 3), Date.now())), 700);
  }
}

class WatchSocket extends FakeSocket {
  protected boot() {
    const { path, q } = parse(this.url);
    const m = /^\/sessions\/([^/]+)\/watch$/.exec(path);
    const s = m ? getDb().sessions.find((x) => x.id === decodeURIComponent(m[1])) : null;
    if (!m || !s || q.get('csrf') !== CSRF) return this.failHandshake();
    this.open();
    if (!s.active) return this.endAndClose('process_exited', 'This session has already ended.');
    this.control({ type: 'ready', sessionId: s.id, startedAt: s.startedAt, closesAt: null, environment: s.environment, breakGlassId: s.breakGlassId });
    const steps = replayScript(s.pod).filter((x) => x.at < 400);
    this.bytes(`\x1b[90mAttached read-only to ${s.pod} (${s.username})\x1b[0m\r\n`);
    let k = 0;
    this.every(() => {
      const st = steps[k % steps.length];
      k++;
      if (st.input) this.bytes(st.input + '\r\n');
      this.bytes(st.output);
    }, 2500);
    this.cleanup(register(s.id, (r, msg) => this.endAndClose(r, msg)));
  }
}

export function makePatchedWebSocket(Real: typeof WebSocket): typeof WebSocket {
  const P = function (url: string | URL, protocols?: string | string[]) {
    const u = new URL(String(url), window.location.href);
    if (u.pathname.startsWith('/api/v1/')) {
      if (/\/(exec|debug)$/.test(u.pathname)) return new ExecSocket(String(url));
      if (/\/logs\/stream$/.test(u.pathname)) return new LogSocket(String(url));
      if (/\/watch$/.test(u.pathname)) return new WatchSocket(String(url));
    }
    return new Real(url, protocols);
  } as unknown as typeof WebSocket;
  Object.assign(P, { CONNECTING: 0, OPEN: 1, CLOSING: 2, CLOSED: 3 });
  P.prototype = Real.prototype;
  return P;
}

// ---- Server-Sent Events
class FakeEventSource {
  static CONNECTING = 0;
  static OPEN = 1;
  static CLOSED = 2;
  readyState = 0;
  url: string;
  onerror: Listener | null = null;
  onopen: Listener | null = null;
  onmessage: Listener | null = null;
  private ls: Record<string, ((e: MessageEvent) => void)[]> = {};
  private timers: number[] = [];
  constructor(url: string) {
    this.url = url;
    this.timers.push(window.setTimeout(() => this.start(), 200));
  }
  addEventListener(t: string, fn: (e: MessageEvent) => void) { (this.ls[t] ??= []).push(fn); }
  removeEventListener(t: string, fn: (e: MessageEvent) => void) { this.ls[t] = (this.ls[t] ?? []).filter((f) => f !== fn); }
  close() { this.readyState = 2; this.timers.forEach((t) => { window.clearTimeout(t); window.clearInterval(t); }); }
  private emit(type: string, data: unknown) {
    if (this.readyState === 2) return;
    for (const f of this.ls[type] ?? []) f(new MessageEvent(type, { data: JSON.stringify(data) }));
  }
  private start() {
    const id = decodeURIComponent(this.url.split('/operations/')[1].split('/')[0]);
    const op = getDb().ops[id];
    this.readyState = 1;
    if (!op) {
      this.emit('done', { outcome: 'failed', message: 'Unknown operation' });
      return this.close();
    }
    const d = getDb();
    if (op.kind === 'restart') {
      const wl = d.namespaces.find((n) => n.cluster === op.cluster && n.namespace === op.namespace)?.workloads.find((w) => w.name === op.name);
      const desired = wl?.desired ?? 3;
      let step = 0;
      const t = window.setInterval(() => {
        step++;
        const upd = Math.min(desired, step);
        const rdy = Math.max(0, Math.min(desired, step - 1));
        if (op.willFail && step > 2) {
          this.emit('progress', { desired, updated: 1, ready: 0, available: Math.max(0, desired - 1) });
          if (step > 4) {
            this.emit('done', { outcome: 'failed', message: `ProgressDeadlineExceeded: ReplicaSet "${op.name}-84d7f6c9b" has timed out progressing.` });
            this.close();
          }
          return;
        }
        this.emit('progress', { desired, updated: upd, ready: rdy, available: rdy });
        if (rdy >= desired) {
          this.emit('done', { outcome: 'succeeded', message: `${op.name} rolled out: ${desired}/${desired} ready.` });
          this.close();
        }
      }, 900);
      this.timers.push(t);
    } else if (op.kind === 'sync' || op.kind === 'rollback') {
      // Pending, then Running over a few resources, then the outcome.
      const total = 6;
      let tick = 0;
      this.emit('progress', { phase: 'Pending', message: '', synced: 0, failed: 0, total: 0 });
      const t = window.setInterval(() => {
        tick++;
        const synced = Math.min(total, tick * 2);
        if (op.willFail && tick >= 2) {
          this.emit('progress', { phase: 'Failed', message: 'one or more objects failed to apply', synced: 4, failed: 2, total });
          this.emit('done', { outcome: 'failed', message: 'one or more objects failed to apply, reason: Deployment.apps "payments-worker" is invalid' });
          return this.close();
        }
        if (synced < total) {
          this.emit('progress', { phase: 'Running', message: 'one or more tasks are running', synced, failed: 0, total });
          return;
        }
        this.emit('progress', { phase: 'Succeeded', message: 'successfully synced (all tasks run)', synced, failed: 0, total });
        this.emit('done', { outcome: 'succeeded', message: 'successfully synced (all tasks run)' });
        this.close();
      }, 900);
      this.timers.push(t);
    } else {
      const key = `${op.cluster}/${op.namespace}/${op.name}`;
      const job = d.jobs[key]?.find((j) => j.name === op.jobName);
      let tick = 0;
      const t = window.setInterval(() => {
        tick++;
        if (!job) return;
        if (tick >= 6) {
          const fail = op.name === 'archive-reports';
          Object.assign(job, { status: fail ? 'failed' : 'succeeded', active: 0, succeeded: fail ? 0 : 1, finishedAt: iso(Date.now()) });
          this.emit('progress', { ...job });
          this.emit('done', { outcome: fail ? 'failed' : 'succeeded', message: fail ? 'BackoffLimitExceeded: Job has reached the specified backoff limit' : `Job ${job.name} completed.` });
          this.close();
        } else {
          this.emit('progress', { ...job });
        }
      }, 1000);
      this.timers.push(t);
    }
  }
}

/**
 * GET /live: every few seconds something happens in one of the followed
 * namespaces (a container restart, a rollout, a warning, a CronJob run). The
 * mock data is changed to match, so the refetches show it.
 */
class FakeLiveSource {
  readyState = 0;
  onerror: Listener | null = null;
  private ls: Record<string, ((e: MessageEvent) => void)[]> = {};
  private timers: number[] = [];
  private keys: string[];
  private rolling = new Set<string>();
  constructor(url: string) {
    this.keys = new URL(url, window.location.href).searchParams.getAll('ns');
    this.timers.push(window.setTimeout(() => this.start(), 250));
  }
  addEventListener(t: string, fn: (e: MessageEvent) => void) { (this.ls[t] ??= []).push(fn); }
  removeEventListener(t: string, fn: (e: MessageEvent) => void) { this.ls[t] = (this.ls[t] ?? []).filter((f) => f !== fn); }
  close() { this.readyState = 2; this.timers.forEach((t) => window.clearTimeout(t)); }
  private emit(type: string, data: unknown) {
    if (this.readyState === 2) return;
    for (const f of this.ls[type] ?? []) f(new MessageEvent(type, { data: JSON.stringify(data) }));
  }
  private later(ms: number, fn: () => void) { this.timers.push(window.setTimeout(fn, ms)); }
  private start() {
    const d = getDb();
    this.readyState = 1;
    this.emit('ready', { namespaces: this.keys.map((k) => {
      const [cluster, namespace] = k.split('/');
      return { cluster, namespace, ...(d.namespaces.some((n) => n.cluster === cluster && n.namespace === namespace) ? {} : { error: 'not_found' }) };
    }) });
    this.later(2500, () => this.tick());
  }
  private tick() {
    this.later(5000 + Math.random() * 4000, () => this.tick());
    const d = getDb();
    const nss = this.keys.map((k) => d.namespaces.find((n) => `${n.cluster}/${n.namespace}` === k)).filter((n) => !!n);
    const n = nss[Math.floor(Math.random() * nss.length)];
    if (!n) return;
    const wls = n.workloads.filter((w) => w.kind !== 'CronJob' && w.desired > 0 && !this.rolling.has(w.name));
    const crons = n.workloads.filter((w) => w.kind === 'CronJob');
    const w = wls[Math.floor(Math.random() * wls.length)];
    const r = Math.random();
    const at = (c: { kind: string; name: string }) => ({ cluster: n.cluster, namespace: n.namespace, kind: c.kind, name: c.name });
    if (crons.length && (r > 0.85 || !w)) {
      const cj = crons[Math.floor(Math.random() * crons.length)];
      const job = `${cj.name}-${Math.floor(Date.now() / 60000)}`;
      const ev: K8sEvent = { type: 'Normal', reason: 'SuccessfulCreate', message: `Created job ${job}`, object: `CronJob/${cj.name}`, count: 1, firstSeen: iso(Date.now()), lastSeen: iso(Date.now()) };
      this.emit('change', { ...at(cj), type: 'event', event: ev });
      return;
    }
    if (!w) return;
    const pod0 = w.kind === 'StatefulSet' ? `${w.name}-0` : `${w.name}-7d9c6b5f4-2kq8x`;
    if (r < 0.4) {
      w.liveRestarts = (w.liveRestarts ?? 0) + 1;
      this.emit('change', { ...at(w), type: 'pod_restarted', pod: pod0, phase: 'Running', container: 'app', reason: 'OOMKilled', exitCode: 137 });
    } else if (r < 0.7) {
      const status = (updated: number, ready: number, complete = false) => ({ desired: w.desired, updated, ready, available: ready, complete, failed: false, message: complete ? 'rollout complete' : `${updated} of ${w.desired} replicas updated` });
      const surge = `${w.name}-5c8f7d6b9-n3w7p`;
      this.rolling.add(w.name);
      w.liveSurge = true;
      this.emit('change', { ...at(w), type: 'rollout_started', status: status(0, w.ready) });
      this.emit('change', { ...at(w), type: 'pod_added', pod: surge, phase: 'Pending' });
      this.later(3000, () => this.emit('change', { ...at(w), type: 'pod_updated', pod: surge, phase: 'Running' }));
      this.later(5500, () => {
        w.liveSurge = false;
        this.rolling.delete(w.name);
        this.emit('change', { ...at(w), type: 'pod_removed', pod: surge });
        this.emit('change', { ...at(w), type: 'rollout_complete', status: status(w.desired, w.ready, true) });
      });
    } else {
      const now = iso(Date.now());
      const ev: K8sEvent = { type: 'Warning', reason: 'BackOff', message: `Back-off restarting failed container app in pod ${pod0}`, object: `Pod/${pod0}`, count: 1, firstSeen: now, lastSeen: now };
      w.liveEvents = [ev, ...(w.liveEvents ?? [])].slice(0, 10);
      this.emit('change', { ...at(w), type: 'event', pod: pod0, event: ev });
    }
  }
}

export function makePatchedEventSource(Real: typeof EventSource): typeof EventSource {
  const P = function (url: string | URL, init?: EventSourceInit) {
    if (String(url).includes('/api/v1/live')) return new FakeLiveSource(String(url));
    if (String(url).includes('/api/v1/operations/')) return new FakeEventSource(String(url));
    return new Real(url, init);
  } as unknown as typeof EventSource;
  Object.assign(P, { CONNECTING: 0, OPEN: 1, CLOSED: 2 });
  return P;
}
