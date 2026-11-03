import { useRef } from 'react';

/** Shows a value and briefly pops it whenever it changes (not on the first render). */
export function Bump({ value, children }: { value: string | number; children?: React.ReactNode }) {
  const seen = useRef({ value, changed: false });
  if (seen.current.value !== value) seen.current = { value, changed: true };
  return <span key={String(value)} className={seen.current.changed ? 'bump' : undefined}>{children ?? value}</span>;
}
