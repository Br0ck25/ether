import { useCallback, useEffect, useState } from "react";
import { api } from "../api";
import { ErrorBanner, Field, Notice, Spinner, TextInput } from "../components";
import type { EtherUser } from "../types";

const RATINGS = ["", "G", "PG", "PG-13", "R", "NC-17", "TV-Y", "TV-Y7", "TV-G", "TV-PG", "TV-14", "TV-MA"];

export default function UsersPanel() {
  const [users, setUsers] = useState<EtherUser[] | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [savedId, setSavedId] = useState<number | null>(null);

  const load = useCallback(async () => {
    try { setUsers(await api.users()); setError(null); } catch (e) { setError(e); }
  }, []);
  useEffect(() => { void load(); }, [load]);

  if (error) return <><ErrorBanner error={error} /><p className="muted">Users appear here once the server is claimed and shared with Plex friends.</p></>;
  if (!users) return <Spinner />;
  if (users.length === 0) return <Notice kind="info">No users yet. Share this server from plex.tv to add friends.</Notice>;

  const patch = (i: number, p: Partial<EtherUser>) =>
    setUsers(users.map((u, j) => (j === i ? { ...u, ...p } : u)));

  return (
    <div>
      {users.map((u, i) => (
        <section key={u.id} className="card">
          <h3>{u.thumb ? <img className="avatar" src={u.thumb} alt="" /> : null} {u.title || u.username} {u.isOwner ? <span className="pill pill-ok">Owner</span> : null}</h3>
          <p className="muted">{u.username}{u.email ? ` · ${u.email}` : ""}</p>
          <div className="grid2">
            <Field label="Stream limit" hint="0 = server default.">
              <TextInput type="number" min={0} value={u.streamLimit} onChange={(e) => patch(i, { streamLimit: Math.max(0, e.target.valueAsNumber || 0) })} />
            </Field>
            <Field label="Max content rating">
              <select className="input" value={u.maxContentRating} onChange={(e) => patch(i, { maxContentRating: e.target.value })} aria-label="Max content rating">
                {RATINGS.map((r) => <option key={r} value={r}>{r === "" ? "Unrestricted" : r}</option>)}
              </select>
            </Field>
            <Field label="Subtitle languages" hint="ISO codes, comma-separated, preference order.">
              <TextInput defaultValue={(u.subtitleLanguages ?? []).join(", ")} key={u.id + "-sub"}
                onBlur={(e) => patch(i, { subtitleLanguages: e.target.value.split(",").map((s) => s.trim()).filter(Boolean) })} />
            </Field>
            <Field label="Audio languages">
              <TextInput defaultValue={(u.audioLanguages ?? []).join(", ")} key={u.id + "-aud"}
                onBlur={(e) => patch(i, { audioLanguages: e.target.value.split(",").map((s) => s.trim()).filter(Boolean) })} />
            </Field>
          </div>
          <div className="row-actions">
            <button type="button" className="btn btn-primary btn-small" onClick={async () => {
              try {
                await api.updateUser(u.id, { streamLimit: u.streamLimit, maxContentRating: u.maxContentRating, subtitleLanguages: u.subtitleLanguages, audioLanguages: u.audioLanguages });
                setSavedId(u.id);
              } catch (e) { setError(e); }
            }}>Save user</button>
            {savedId === u.id ? <span className="pill pill-ok">Saved</span> : null}
          </div>
        </section>
      ))}
    </div>
  );
}
