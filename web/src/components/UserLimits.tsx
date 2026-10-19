import { Button, SearchSelect, SettingsRow, SettingsSection, toast } from "@braedonsaunders/appkit-ui";
import { Save } from "lucide-react";
import { useEffect, useState } from "react";
import { api, errorMessage } from "../lib/api";
import type { EtherUser } from "../lib/types";
import { ListInput, NumberInput } from "./fields";

export const CONTENT_RATINGS = [
  { value: "", label: "Unrestricted" },
  { value: "G", label: "G" }, { value: "PG", label: "PG" }, { value: "PG-13", label: "PG-13" }, { value: "R", label: "R" }, { value: "NC-17", label: "NC-17" },
  { value: "TV-Y", label: "TV-Y" }, { value: "TV-Y7", label: "TV-Y7" }, { value: "TV-G", label: "TV-G" }, { value: "TV-PG", label: "TV-PG" }, { value: "TV-14", label: "TV-14" }, { value: "TV-MA", label: "TV-MA" },
];

/** Stream limit, content-rating cap and language preferences for one user. */
export function UserLimitsForm({ user, onSaved, defaultStreamLimit }: { user: EtherUser; onSaved?: (u: EtherUser) => void; defaultStreamLimit?: number }) {
  const [draft, setDraft] = useState(user);
  const [saving, setSaving] = useState(false);
  useEffect(() => setDraft(user), [user]);
  const dirty = JSON.stringify(draft) !== JSON.stringify(user);
  const save = async () => {
    setSaving(true);
    try {
      const saved = await api.updateUser(user.id, {
        streamLimit: draft.streamLimit, maxContentRating: draft.maxContentRating,
        subtitleLanguages: draft.subtitleLanguages, audioLanguages: draft.audioLanguages,
      });
      onSaved?.(saved ?? draft);
      toast.success("Saved", { description: `Limits for ${user.title || user.username} updated.` });
    } catch (e) { toast.error("Couldn't save", { description: errorMessage(e) }); }
    finally { setSaving(false); }
  };
  return (
    <SettingsSection title="Limits and preferences" description={user.isOwner ? "The owner is never limited by content rating." : undefined}
      footer={<>
        <Button variant="ghost" size="sm" onClick={() => setDraft(user)} disabled={!dirty || saving}>Discard</Button>
        <Button size="sm" onClick={() => void save()} disabled={!dirty || saving}><Save size={14} /> {saving ? "Saving…" : "Save"}</Button>
      </>}>
      <SettingsRow title="Stream limit" description={`Simultaneous streams. 0 uses the server default${defaultStreamLimit !== undefined ? ` (${defaultStreamLimit})` : ""}.`}
        control={<NumberInput aria-label="Stream limit" className="w-24" value={draft.streamLimit} min={0} onChange={(n) => setDraft({ ...draft, streamLimit: Math.max(0, Math.round(n)) })} />} />
      <SettingsRow title="Content rating cap" description="Titles above this rating are hidden from this user."
        control={<div className="w-44"><SearchSelect value={draft.maxContentRating} onChange={(v) => setDraft({ ...draft, maxContentRating: v })} options={CONTENT_RATINGS} ariaLabel="Content rating cap" searchable={false} /></div>} />
      <SettingsRow stacked title="Subtitle languages" description="ISO codes in preference order, e.g. en, fr.">
        <ListInput ariaLabel="Subtitle languages" value={draft.subtitleLanguages} onChange={(v) => setDraft({ ...draft, subtitleLanguages: v.map(String) })} placeholder="en, fr" />
      </SettingsRow>
      <SettingsRow stacked title="Audio languages" description="Preferred audio tracks in order.">
        <ListInput ariaLabel="Audio languages" value={draft.audioLanguages} onChange={(v) => setDraft({ ...draft, audioLanguages: v.map(String) })} placeholder="en" />
      </SettingsRow>
    </SettingsSection>
  );
}
