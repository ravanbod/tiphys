import { getCsrfToken } from './client';
import { qs } from './query';
import type { ClientControl, ServerControl } from './types';

export interface StreamHandlers {
  onControl: (m: ServerControl) => void;
  onBytes: (b: Uint8Array) => void;
  /** Fired once if the socket closed (cleanly or not). `opened` tells whether the upgrade succeeded. */
  onClose: (info: { opened: boolean; code: number; wasEnded: boolean }) => void;
}

export interface StreamHandle {
  sendBytes(data: Uint8Array | string): void;
  sendControl(m: ClientControl): void;
  close(): void;
  readonly ready: boolean;
}

export function wsUrl(path: string, params: object = {}): string {
  const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
  return `${proto}//${window.location.host}/api/v1${path}${qs({ ...params, csrf: getCsrfToken() })}`;
}

/** Opens a WebSocket speaking the Tiphys framing: binary = raw bytes, text = JSON control. */
export function openStream(url: string, h: StreamHandlers): StreamHandle {
  const ws = new WebSocket(url);
  ws.binaryType = 'arraybuffer';
  let opened = false;
  let ended = false;
  const enc = new TextEncoder();

  ws.onopen = () => {
    opened = true;
  };
  ws.onmessage = (ev: MessageEvent) => {
    if (typeof ev.data === 'string') {
      try {
        const m = JSON.parse(ev.data) as ServerControl;
        if (m.type === 'end') ended = true;
        h.onControl(m);
      } catch {
        /* ignore malformed control frame */
      }
    } else if (ev.data instanceof ArrayBuffer) {
      h.onBytes(new Uint8Array(ev.data));
    }
  };
  ws.onclose = (ev: CloseEvent) => h.onClose({ opened, code: ev.code, wasEnded: ended });

  return {
    get ready() {
      return ws.readyState === WebSocket.OPEN;
    },
    sendBytes(data) {
      if (ws.readyState !== WebSocket.OPEN) return;
      ws.send(typeof data === 'string' ? enc.encode(data) : data);
    },
    sendControl(m) {
      if (ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify(m));
    },
    close() {
      try {
        ws.close(1000);
      } catch {
        /* already closed */
      }
    },
  };
}
