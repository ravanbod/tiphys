export interface SegOption<T extends string | number> {
  value: T;
  label: React.ReactNode;
  mono?: boolean;
}

/** Segmented control (single choice) built from buttons, styled by `.seg`. */
export function Seg<T extends string | number>({
  value, options, onChange, label, style,
}: {
  value: T;
  options: SegOption<T>[];
  onChange: (v: T) => void;
  label: string;
  style?: React.CSSProperties;
}) {
  return (
    <div className="seg" role="group" aria-label={label} style={style}>
      {options.map((o) => (
        <button
          key={String(o.value)}
          type="button"
          className={`${value === o.value ? 'on' : ''}${o.mono ? ' mono' : ''}`}
          aria-pressed={value === o.value}
          onClick={() => onChange(o.value)}
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}
