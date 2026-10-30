import type { JobSummary, OperationDone, RestartProgress } from './types';

export interface OperationHandlers<P> {
  onProgress: (p: P) => void;
  onDone: (d: OperationDone) => void;
  onError: () => void;
}

/** Subscribes to /operations/{id}/stream. Returns an unsubscribe function. */
export function watchOperation<P = RestartProgress | JobSummary>(id: string, h: OperationHandlers<P>): () => void {
  const es = new EventSource(`/api/v1/operations/${encodeURIComponent(id)}/stream`);
  let finished = false;
  es.addEventListener('progress', (ev) => {
    try {
      h.onProgress(JSON.parse((ev as MessageEvent<string>).data) as P);
    } catch {
      /* ignore */
    }
  });
  es.addEventListener('done', (ev) => {
    finished = true;
    try {
      h.onDone(JSON.parse((ev as MessageEvent<string>).data) as OperationDone);
    } catch {
      h.onDone({ outcome: 'failed', message: 'Malformed completion event' });
    }
    es.close();
  });
  es.onerror = () => {
    // EventSource reconnects on its own; after `done` the server closes the stream, which also errors.
    if (!finished && es.readyState === EventSource.CLOSED) h.onError();
  };
  return () => es.close();
}
