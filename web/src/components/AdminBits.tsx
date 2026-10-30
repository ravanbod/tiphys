import { Icon } from './Icon';

/** Inline error under a form field (renders nothing when there is no message). */
export function FieldError({ msg, id }: { msg: string | undefined; id?: string }) {
  if (!msg) return null;
  return (
    <span className="err-text" id={id} role="alert">
      {msg}
    </span>
  );
}

/** Small muted note shown to auditors, who see admin pages without write buttons. */
export function ReadOnlyNote() {
  return (
    <span className="muted row" style={{ gap: 6, fontSize: 12 }}>
      <Icon name="lock" size={13} />
      Read-only view
    </span>
  );
}
