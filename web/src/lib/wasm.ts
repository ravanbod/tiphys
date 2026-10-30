let cached: Promise<boolean> | null = null;

/** True when WebAssembly may be compiled (a strict CSP without 'wasm-unsafe-eval' forbids it). */
export function canCompileWasm(): Promise<boolean> {
  if (!cached) {
    cached = (async () => {
      if (typeof WebAssembly === 'undefined') return false;
      try {
        await WebAssembly.compile(new Uint8Array([0, 97, 115, 109, 1, 0, 0, 0]));
        return true;
      } catch {
        return false;
      }
    })();
  }
  return cached;
}
