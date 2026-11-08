import type { TeamMember } from '../api';
import { Avatar } from './Badges';
import { ComboBox } from './ComboBox';
import { Empty } from './Feedback';
import { Icon } from './Icon';

/**
 * A card listing a team's people (members or managers) with an "Add by name"
 * picker (picking someone adds them; it searches names and usernames) and
 * remove buttons when editable.
 */
export function TeamPeopleCard({
  title, people, candidates, editable, busy, noMatchText, candidatesError, emptyText, onAdd, onRemove,
}: {
  title: string;
  people: TeamMember[];
  /** Who may be added (people already listed are left out here). */
  candidates: TeamMember[];
  editable: boolean;
  busy: boolean;
  noMatchText: string;
  candidatesError?: boolean;
  emptyText: string;
  onAdd: (u: TeamMember) => Promise<boolean>;
  onRemove: (u: TeamMember) => void;
}) {
  const listed = new Set(people.map((p) => p.id));
  const open = candidates.filter((u) => !listed.has(u.id));
  const add = (id: string) => {
    const u = open.find((x) => String(x.id) === id);
    if (u) void onAdd(u);
  };

  return (
    <div className="card grow" style={{ flex: 1, minWidth: 0 }}>
      <div className="hd">
        {title}
        <span className="grow" />
        {editable && (
          <ComboBox
            small
            style={{ width: 220 }}
            label={`Add to ${title.toLowerCase()}`}
            value=""
            disabled={busy}
            onChange={add}
            placeholder="Add by name…"
            emptyText={noMatchText}
            options={open.map((u) => ({ value: String(u.id), label: u.displayName || u.username, hint: u.username }))}
          />
        )}
      </div>
      {editable && candidatesError && (
        <div style={{ padding: '6px 16px' }} className="muted">Could not load users for the picker.</div>
      )}
      <div className="tbl-wrap">
        <table className="tbl" style={{ minWidth: 0 }}>
          <tbody>
            {people.map((u) => (
              <tr key={u.id}>
                <td>
                  <span className="row">
                    <Avatar name={u.displayName || u.username} size={24} />
                    {u.displayName || u.username}
                  </span>
                </td>
                <td className="mono muted">{u.username}</td>
                <td style={{ textAlign: 'right' }}>
                  {editable && (
                    <button
                      type="button"
                      className="btn ghost sm"
                      aria-label={`Remove ${u.displayName || u.username}`}
                      disabled={busy}
                      onClick={() => onRemove(u)}
                    >
                      <Icon name="x" />
                    </button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {people.length === 0 && <Empty>{emptyText}</Empty>}
    </div>
  );
}
