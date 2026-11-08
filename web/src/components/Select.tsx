import { useEffect, useId, useRef, useState } from 'react';
import type { CSSProperties } from 'react';
import { edgeIndex, moveIndex, typeaheadIndex } from '../lib/selectNav';
import { Icon } from './Icon';
import { Popup } from './Popup';

export interface SelectOption<V extends string | number> {
  value: V;
  label: string;
  /** Muted text after the label in the list. */
  hint?: string;
  disabled?: boolean;
  /** Options with the same group are listed under its name, in order. */
  group?: string;
}

/** Typed letters within this long make one type-ahead word. */
const TYPEAHEAD_MS = 600;

/**
 * A select for short, fixed lists, drawn by the page (not the OS) so its
 * list has the page's font and look; long lists use ComboBox, which can be
 * searched. It behaves like a native select: arrows, Home/End, Page keys,
 * type-ahead, Enter or Space to pick, Escape to close (without closing a
 * surrounding dialog). It is as wide as its longest option unless styled.
 */
export function Select<V extends string | number>({
  value, options, onChange, label, invalid, disabled, mono, className = 'sel', style, autoFocus,
}: {
  value: V;
  options: SelectOption<V>[];
  onChange: (value: V) => void;
  /** Accessible name. */
  label: string;
  invalid?: boolean;
  disabled?: boolean;
  /** Names (pods, containers) in IBM Plex Mono, the button and the list alike. */
  mono?: boolean;
  /** The button's look; `.sel` by default. */
  className?: string;
  style?: CSSProperties;
  /** Focused first when a Dialog opens. */
  autoFocus?: boolean;
}) {
  const listId = useId();
  const btn = useRef<HTMLButtonElement>(null);
  const [open, setOpen] = useState(false);
  const [active, setActive] = useState(-1);
  const typed = useRef({ text: '', at: 0 });
  const current = options.findIndex((o) => o.value === value);
  const disabledAt = options.map((o) => !!o.disabled);
  const labels = options.map((o) => o.label);

  useEffect(() => {
    if (open && active >= 0) document.getElementById(`${listId}-${active}`)?.scrollIntoView({ block: 'nearest' });
  }, [open, active, listId]);

  // A placeholder (disabled) value starts on the first option that can be picked.
  const show = (at = current >= 0 && !disabledAt[current] ? current : edgeIndex(disabledAt)) => {
    setActive(at);
    setOpen(true);
  };
  const pick = (i: number) => {
    const o = options[i];
    if (o && !o.disabled && o.value !== value) onChange(o.value);
    setOpen(false);
  };
  const typeahead = (ch: string) => {
    const now = Date.now();
    const t = typed.current;
    t.text = now - t.at < TYPEAHEAD_MS ? t.text + ch : ch;
    t.at = now;
    const from = open ? active : current;
    const i = typeaheadIndex(labels, disabledAt, from, t.text);
    if (open) setActive(i);
    else show(i);
  };

  const onKeyDown = (e: React.KeyboardEvent) => {
    const k = e.key;
    if (!open) {
      if (k === 'ArrowDown' || k === 'ArrowUp' || k === 'Enter' || k === ' ') {
        e.preventDefault();
        show();
      } else if (k.length === 1 && !e.ctrlKey && !e.metaKey && !e.altKey) {
        e.preventDefault();
        typeahead(k);
      }
      return;
    }
    const step: Record<string, number> = { ArrowDown: 1, ArrowUp: -1, PageDown: 10, PageUp: -10 };
    if (k in step) {
      e.preventDefault();
      setActive((i) => moveIndex(disabledAt, i, step[k]));
    } else if (k === 'Home' || k === 'End') {
      e.preventDefault();
      setActive(edgeIndex(disabledAt, k === 'End'));
    } else if (k === 'Enter' || (k === ' ' && Date.now() - typed.current.at >= TYPEAHEAD_MS)) {
      // Space picks, except as part of a word being typed.
      e.preventDefault();
      pick(active);
    } else if (k === 'Escape') {
      e.preventDefault();
      e.stopPropagation(); // keep a surrounding dialog open
      setOpen(false);
    } else if (k === 'Tab') {
      setOpen(false);
    } else if (k.length === 1 && !e.ctrlKey && !e.metaKey && !e.altKey) {
      e.preventDefault();
      typeahead(k);
    }
  };
  let lastGroup: string | undefined;
  return (
    <>
      <button
        ref={btn}
        type="button"
        className={`${className} sel-btn${mono ? ' mono' : ''}`}
        style={style}
        role="combobox"
        aria-label={label}
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-controls={open ? listId : undefined}
        aria-activedescendant={open && active >= 0 ? `${listId}-${active}` : undefined}
        aria-invalid={invalid || undefined}
        disabled={disabled}
        data-autofocus={autoFocus || undefined}
        onClick={() => (open ? setOpen(false) : show())}
        onKeyDown={onKeyDown}
        onBlur={() => setOpen(false)}
      >
        {/* Every label in one grid cell: the button is as wide as the longest, like a native select. */}
        <span className="sel-sizer">
          {options.map((o, i) => (
            <span key={String(o.value)} className={i === current ? undefined : 'sel-ghost'} aria-hidden={i !== current}>{o.label}</span>
          ))}
          {current < 0 && <span>{String(value)}</span>}
        </span>
        <Icon name="chevd" className="sel-chev" />
      </button>
      {open && (
        <Popup anchor={btn} id={listId} label={label} onClose={() => setOpen(false)}>
          {options.map((o, i) => {
            const head = o.group !== undefined && o.group !== lastGroup ? o.group : null;
            lastGroup = o.group;
            return [
              head !== null && <li key={`g:${head}`} role="presentation" className="cbx-group">{head}</li>,
              <li
                key={String(o.value)}
                id={`${listId}-${i}`}
                role="option"
                aria-selected={i === current}
                aria-disabled={o.disabled || undefined}
                className={`cbx-opt${i === active ? ' act' : ''}${mono ? ' mono' : ''}${o.disabled ? ' off' : ''}`}
                onMouseEnter={() => { if (!o.disabled) setActive(i); }}
                onClick={() => pick(i)}
              >
                <span className="cbx-l">{o.label}</span>
                {o.hint && <span className="muted">{o.hint}</span>}
              </li>,
            ];
          })}
        </Popup>
      )}
    </>
  );
}
