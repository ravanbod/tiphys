import { useEffect, useId, useRef, useState } from 'react';
import type { CSSProperties } from 'react';
import { exactOption, filterOptions, optionLabel } from '../lib/comboFilter';
import type { ComboOption } from '../lib/comboFilter';
import { Icon } from './Icon';
import { Popup } from './Popup';

export type { ComboOption } from '../lib/comboFilter';

/** Rendering thousands of rows makes typing slow; past this the list asks to narrow. */
const MAX_SHOWN = 200;

/**
 * A select that can be searched: focus or click shows the options, typing
 * narrows them (every word, anywhere), arrows and Enter pick, Escape or
 * leaving keeps the current value. Only listed options can be chosen; a
 * value not among them is still shown as it is.
 *
 * With `free` it is a text field with suggestions instead: the text is the
 * value (onChange on every keystroke), the options only help to type it,
 * and onCommit fires on Enter, on leaving and on picking a suggestion.
 */
export function ComboBox({
  value, options, onChange, onCommit, free, placeholder, label, invalid, disabled, small, style, inputStyle, describedBy,
  emptyText = 'No match',
}: {
  value: string;
  options: ComboOption[];
  onChange: (value: string) => void;
  /** `free` only: the text is final (Enter, leaving, a picked suggestion). */
  onCommit?: (value: string) => void;
  free?: boolean;
  /** Shown when nothing is chosen. */
  placeholder?: string;
  /** Accessible name. */
  label: string;
  invalid?: boolean;
  disabled?: boolean;
  /** Filter-bar height (28px), like `.sel`. */
  small?: boolean;
  style?: CSSProperties;
  inputStyle?: CSSProperties;
  describedBy?: string;
  emptyText?: string;
}) {
  const listId = useId();
  const box = useRef<HTMLDivElement>(null);
  // null: closed, showing the chosen option; a string: open, searching for it
  // (in free mode the text typed since opening; '' lists every suggestion).
  const [q, setQ] = useState<string | null>(null);
  const [active, setActive] = useState(free ? -1 : 0);
  const open = q !== null && !disabled;
  const chosen = options.find((o) => o.value === value);
  const shownLabel = free ? value : chosen ? optionLabel(chosen) : value;
  const hits = open ? filterOptions(options, q) : [];
  const shown = hits.slice(0, MAX_SHOWN);
  // Free mode shows no list when no suggestion fits the text.
  const listed = open && (!free || shown.length > 0);

  useEffect(() => {
    if (open && active >= 0) document.getElementById(`${listId}-${active}`)?.scrollIntoView({ block: 'nearest' });
  }, [open, active, listId]);

  const openList = () => {
    if (disabled || open) return;
    setQ('');
    setActive(free ? -1 : Math.max(0, options.findIndex((o) => o.value === value)));
  };
  const close = () => setQ(null);
  const pick = (o: ComboOption | undefined) => {
    if (o && o.value !== value) onChange(o.value);
    if (o && free) onCommit?.(o.value);
    close();
  };

  return (
    <div ref={box} className={`cbx${open ? ' open' : ''}${small ? ' sm' : ''}${free ? ' free' : ''}`} style={style}>
      <input
        className="input"
        style={inputStyle}
        role="combobox"
        aria-label={label}
        aria-expanded={listed}
        aria-controls={listed ? listId : undefined}
        aria-autocomplete="list"
        aria-activedescendant={listed && shown[active] ? `${listId}-${active}` : undefined}
        aria-invalid={invalid || undefined}
        aria-describedby={describedBy}
        disabled={disabled}
        autoComplete="off"
        spellCheck={false}
        value={open && !free ? q : shownLabel}
        placeholder={open && !free && shownLabel ? shownLabel : placeholder}
        onFocus={openList}
        // A click on the field reopens it; clicks a wrapping <label> forwards are not mouse downs.
        onMouseDown={openList}
        onBlur={() => {
          if (free) onCommit?.(value);
          else if (q) pick(exactOption(options, q));
          close();
        }}
        onChange={(e) => {
          if (free) onChange(e.target.value);
          setQ(e.target.value);
          setActive(free ? -1 : 0);
        }}
        onKeyDown={(e) => {
          if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
            e.preventDefault();
            if (!open) return openList();
            const d = e.key === 'ArrowDown' ? 1 : -1;
            setActive((i) => Math.min(Math.max(i + d, 0), Math.max(shown.length - 1, 0)));
          } else if (e.key === 'Enter') {
            if (listed && (!free || active >= 0)) {
              e.preventDefault();
              pick(shown[active]);
            } else if (free) {
              e.preventDefault();
              onCommit?.(value);
              close();
            }
          } else if (e.key === 'Escape' && listed) {
            e.preventDefault();
            e.stopPropagation(); // keep a surrounding dialog open
            close();
          }
        }}
      />
      {!free && <Icon name="chevd" className="cbx-chev" />}
      {listed && (
        <Popup anchor={box} id={listId} label={label} onClose={close}>
          {shown.map((o, i) => (
            <li
              key={o.value}
              id={`${listId}-${i}`}
              role="option"
              aria-selected={o.value === value}
              className={`cbx-opt${i === active ? ' act' : ''}`}
              onMouseEnter={() => setActive(i)}
              onClick={() => pick(o)}
            >
              <span className="cbx-l">{optionLabel(o)}</span>
              {o.hint && <span className="muted">{o.hint}</span>}
            </li>
          ))}
          {hits.length === 0 && <li className="cbx-note muted" aria-disabled="true">{emptyText}</li>}
          {hits.length > shown.length && (
            <li className="cbx-note muted" aria-disabled="true">{hits.length - shown.length} more · type to narrow</li>
          )}
        </Popup>
      )}
    </div>
  );
}
