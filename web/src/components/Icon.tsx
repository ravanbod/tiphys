import type { CSSProperties } from 'react';

const PATHS = {
  bolt: <path d="M9 1.5L3.5 9H8l-1 5.5L12.5 7H8z" />,
  bug: (<><rect x="5" y="5" width="6" height="8.5" rx="3" /><path d="M6.2 5.2L5 3M9.8 5.2L11 3M2.5 8.5H5M11 8.5h2.5M3 12.5l2-1M13 12.5l-2-1M8 7.5v6" /></>),
  box: <path d="M2 5l6-3 6 3v6l-6 3-6-3zM2 5l6 3 6-3M8 8v6" />,
  check: <path d="M3 8.5l3.2 3L13 4.5" />,
  chev: <path d="M6 3.5L10.5 8 6 12.5" />,
  chevd: <path d="M3.5 6L8 10.5 12.5 6" />,
  clock: (<><circle cx="8" cy="8" r="6" /><path d="M8 4.5V8l2.3 1.5" /></>),
  down: <path d="M8 2v8M4.5 7L8 10.5 11.5 7M3 13.5h10" />,
  ext: <path d="M9 2.5h4.5V7M13.5 2.5L7.5 8.5M12 9.5V13H3V4h3.5" />,
  gear: (<><circle cx="8" cy="8" r="2" /><path d="M8 1.5v2M8 12.5v2M1.5 8h2M12.5 8h2M3.4 3.4l1.4 1.4M11.2 11.2l1.4 1.4M12.6 3.4l-1.4 1.4M4.8 11.2l-1.4 1.4" /></>),
  inbox: <path d="M2 9l2-6h8l2 6v4H2zM2 9h4l1 2h2l1-2h4" />,
  key: (<><circle cx="5.5" cy="10.5" r="2.5" /><path d="M7.3 8.7L13 3M11 5l2 2" /></>),
  list: <path d="M5 4h9M5 8h9M5 12h9M2 4h.01M2 8h.01M2 12h.01" />,
  lock: (<><rect x="3" y="7" width="10" height="7" rx="1.5" /><path d="M5.2 7V5a2.8 2.8 0 015.6 0v2" /></>),
  play: <path d="M4.5 2.8l8 5.2-8 5.2z" />,
  pause: <path d="M5 3v10M11 3v10" />,
  plus: <path d="M8 3v10M3 8h10" />,
  restart: <path d="M13 8a5 5 0 11-1.6-3.7M13 2.5v3h-3" />,
  search: (<><circle cx="7" cy="7" r="4.5" /><path d="M10.5 10.5L14 14" /></>),
  server: (<><rect x="2" y="2.5" width="12" height="4.5" rx="1.2" /><rect x="2" y="9" width="12" height="4.5" rx="1.2" /><path d="M4.5 4.75h.01M4.5 11.25h.01" /></>),
  shield: <path d="M8 1.5l5.5 2v4.2c0 3.2-2.3 5.6-5.5 6.8-3.2-1.2-5.5-3.6-5.5-6.8V3.5z" />,
  star: <path d="M8 1.8l1.9 4 4.3.5-3.2 2.9.9 4.3L8 11.4l-3.9 2.1.9-4.3L1.8 6.3l4.3-.5z" />,
  term: (<><rect x="1.5" y="2.5" width="13" height="11" rx="2" /><path d="M4.5 6l2 2-2 2M8 10.5h3.5" /></>),
  users: (<><circle cx="6" cy="5.5" r="2.5" /><path d="M1.5 13c.4-2.4 2.2-3.5 4.5-3.5s4.1 1.1 4.5 3.5M11 3.2a2.4 2.4 0 010 4.6M12.5 9.7c1.3.5 2 1.5 2.2 3.3" /></>),
  warn: <path d="M8 2l6.5 11.5h-13zM8 6.5v3.2M8 11.6h.01" />,
  x: <path d="M3.5 3.5l9 9M12.5 3.5l-9 9" />,
  moon: <path d="M13 9.5A5.5 5.5 0 116.5 3a4.5 4.5 0 006.5 6.5z" />,
  sun: (<><circle cx="8" cy="8" r="2.8" /><path d="M8 1.5v1.5M8 13v1.5M1.5 8H3M13 8h1.5M3.4 3.4l1 1M11.6 11.6l1 1M12.6 3.4l-1 1M4.4 11.6l-1 1" /></>),
  eye: (<><path d="M1.5 8S4 3.5 8 3.5 14.5 8 14.5 8 12 12.5 8 12.5 1.5 8 1.5 8z" /><circle cx="8" cy="8" r="2" /></>),
  trash: <path d="M3 4.5h10M6.5 4.5V3h3v1.5M4.5 4.5l.6 8.5h5.8l.6-8.5" />,
  edit: <path d="M2.5 13.5l.6-3L11 2.6a1.4 1.4 0 012 2L5.1 12.5zM9.8 3.8l2 2" />,
  refresh: <path d="M13.5 8a5.5 5.5 0 11-1.6-3.9M13.5 2.5v3h-3" />,
  menu: <path d="M2.5 4.5h11M2.5 8h11M2.5 11.5h11" />,
} as const;

export type IconName = keyof typeof PATHS;

export function Icon({ name, size, style, className }: { name: IconName; size?: number; style?: CSSProperties; className?: string }) {
  const s = size ? { width: size, height: size, ...style } : style;
  return (
    <svg className={className ? `i ${className}` : 'i'} viewBox="0 0 16 16" style={s} aria-hidden="true" focusable="false">
      {PATHS[name]}
    </svg>
  );
}
