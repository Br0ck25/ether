import { Button, Input, Select, SettingsSection, Switch, toast } from "@braedonsaunders/appkit-ui";
import { UserPlus } from "lucide-react";
import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { ChipGroup, NumberInput } from "../../components/fields";
import { ErrorPanel, LoadingRows } from "../../components/feedback";
import { SaveBar, useDraft } from "../../components/SaveBar";
import { SettingsRow } from "../../components/SettingsRow";
import {
  CONTENT_RATINGS,
  UserLimitRows,
  userLimits,
  type UserLimits,
} from "../../components/UserLimits";
import { viewerName } from "../../components/pickers";
import { api, apiFetch, errorMessage } from "../../lib/api";
import type { Viewer } from "../../lib/contract";
import { useAsync } from "../../lib/hooks";
import { ViewerLiveTVAccess } from "./sections/ViewerLiveTV";
import { KID_PRESETS, ListText, normalizePolicy, type SafetyPolicy } from "./shared";

const HOURS = Array.from({ length: 24 }, (_, h) => ({
  value: h,
  label: `${String(h).padStart(2, "0")}:00`,
}));

/**
 * Everything that decides what one viewer may watch and when: their stream
 * limit and rating cap, parental controls, allowed times and Live TV access.
 * One in-place form; the save bar writes whichever parts changed.
 */
export function ViewerAccess({ viewer }: { viewer: Viewer }) {
  const id = viewer.id;
  const name = viewerName(viewer);
  const navigate = useNavigate();
  const users = useAsync(() => api.users(), []);
  const config = useAsync(() => api.getConfig(), []);
  const policy = useAsync(
    async (signal) =>
      normalizePolicy(await apiFetch<SafetyPolicy>(`/safety/policies/${id}`, { signal })),
    [id],
  );
  const user = users.data?.find((u) => u.id === id);
  const limits = useDraft<UserLimits>(user ? userLimits(user) : null);
  const rules = useDraft<SafetyPolicy>(policy.data);
  const [saving, setSaving] = useState(false);
  const [presetBusy, setPresetBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);

  const patchLimits = (p: Partial<UserLimits>) => limits.setDraft((d) => (d ? { ...d, ...p } : d));
  const patchPolicy = (p: Partial<SafetyPolicy>) => rules.setDraft((d) => (d ? { ...d, ...p } : d));

  const applyPreset = async (age: string) => {
    setPresetBusy(true);
    try {
      const preset = await apiFetch<SafetyPolicy>(`/safety/policies/presets/kids?age=${age}`);
      rules.setDraft(normalizePolicy(preset));
    } catch (e) {
      toast.error("Couldn't load the preset", { description: errorMessage(e) });
    } finally {
      setPresetBusy(false);
    }
  };

  const save = async () => {
    setSaving(true);
    setError(null);
    try {
      if (limits.dirty && limits.draft && user) {
        const saved = await api.updateUser(id, limits.draft);
        const next = saved ?? { ...user, ...limits.draft };
        users.setData((list) => (list ?? []).map((u) => (u.id === id ? next : u)));
      }
      if (rules.dirty && rules.draft) {
        await apiFetch(`/safety/policies/${id}`, {
          method: "PUT",
          body: JSON.stringify(rules.draft),
        });
        policy.setData(rules.draft);
      }
      toast.success("Access saved", { description: `${name}'s access is up to date.` });
    } catch (e) {
      setError(e);
    } finally {
      setSaving(false);
    }
  };

  const l = limits.draft;
  const p = rules.draft;
  return (
    <div className="max-w-3xl space-y-6">
      {users.error ? (
        <ErrorPanel error={users.error} onRetry={() => void users.reload()} />
      ) : !users.data ? (
        <LoadingRows rows={2} />
      ) : !user || !l ? (
        <SettingsSection title="Streams and ratings">
          <p className="py-3 text-sm text-fg-muted">
            This viewer has no user record on the server yet, so there are no limits to set.
          </p>
        </SettingsSection>
      ) : (
        <>
          <SettingsSection title="Streams and ratings">
            <UserLimitRows
              value={l}
              onChange={(next) => patchLimits(next)}
              isOwner={user.isOwner}
              defaultStreamLimit={config.data?.server.defaultStreamLimit}
              streamLimitHint={
                <Link to="/server" className="font-medium text-primary hover:underline">
                  Change the server default
                </Link>
              }
            />
          </SettingsSection>
          <SettingsSection
            title="Account languages"
            description="Preferred languages reported to Plex apps for this account. Track selection rules live on the Audio & subtitles tab."
          >
            <SettingsRow
              title="Subtitle languages"
              description="Language codes in preference order, e.g. en, fr."
              control={
                <div className="w-56">
                  <ListText
                    ariaLabel="Account subtitle languages"
                    placeholder="en, fr"
                    value={l.subtitleLanguages}
                    onChange={(v) => patchLimits({ subtitleLanguages: v })}
                  />
                </div>
              }
            />
            <SettingsRow
              title="Audio languages"
              description="Preferred audio tracks in order."
              control={
                <div className="w-56">
                  <ListText
                    ariaLabel="Account audio languages"
                    placeholder="en"
                    value={l.audioLanguages}
                    onChange={(v) => patchLimits({ audioLanguages: v })}
                  />
                </div>
              }
            />
          </SettingsSection>
        </>
      )}

      {policy.error ? (
        <ErrorPanel error={policy.error} onRetry={() => void policy.reload()} />
      ) : !p ? (
        <LoadingRows rows={3} />
      ) : (
        <>
          <SettingsSection
            title="Parental controls"
            description="Adult titles are always excluded. Unrated titles are blocked when a rating limit applies. Where this and the rating cap above differ, the stricter one wins."
          >
            <SettingsRow
              title="Start from a preset"
              description="Replaces the parental controls, playback restrictions and allowed times below."
              control={
                <div className="flex flex-wrap gap-2">
                  {KID_PRESETS.map((preset) => (
                    <Button
                      key={preset.value}
                      variant="outline"
                      size="sm"
                      disabled={presetBusy}
                      onClick={() => void applyPreset(preset.value)}
                    >
                      {preset.label}
                    </Button>
                  ))}
                </div>
              }
            />
            <SettingsRow
              title="Maximum content rating"
              control={
                <div className="w-44">
                  <Select
                    aria-label="Maximum content rating"
                    value={p.maxContentRating}
                    onChange={(e) => patchPolicy({ maxContentRating: e.target.value })}
                  >
                    {CONTENT_RATINGS.map((r) => (
                      <option key={r.value} value={r.value}>
                        {r.label}
                      </option>
                    ))}
                  </Select>
                </div>
              }
            />
            <SettingsRow
              title="Blocked genres"
              description="Comma-separated, e.g. Horror, Thriller."
              control={
                <div className="w-64">
                  <ListText
                    ariaLabel="Blocked genres"
                    value={p.blockedGenres}
                    onChange={(v) => patchPolicy({ blockedGenres: v })}
                  />
                </div>
              }
            />
            <SettingsRow
              title="Blocked keywords"
              description="Comma-separated TMDB keywords."
              control={
                <div className="w-64">
                  <ListText
                    ariaLabel="Blocked keywords"
                    value={p.blockedKeywords}
                    onChange={(v) => patchPolicy({ blockedKeywords: v })}
                  />
                </div>
              }
            />
            <SettingsRow
              title="Invite someone with these controls"
              description="Start an invitation that gives new friends the saved controls above."
              control={
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => navigate(`/viewers/invitations?copy=${id}`)}
                >
                  <UserPlus size={14} /> Invite a friend
                </Button>
              }
            />
          </SettingsSection>

          <SettingsSection title="Playback restrictions">
            <SettingsRow
              title="Local network only"
              description="Block playback from outside your home network."
              control={
                <Switch
                  aria-label="Local network only"
                  checked={p.remoteDisabled}
                  onChange={(e) => patchPolicy({ remoteDisabled: e.target.checked })}
                />
              }
            />
            <SettingsRow
              title="Maximum bitrate"
              description="In kbps. 0 uses the server default."
              control={
                <NumberInput
                  aria-label="Maximum bitrate"
                  className="w-28"
                  min={0}
                  value={p.maxBitrateKbps}
                  onChange={(n) => patchPolicy({ maxBitrateKbps: Math.max(0, Math.round(n)) })}
                />
              }
            />
            <SettingsRow
              title="Concurrent streams"
              description="0 adds no extra limit. Where this and the stream limit above differ, the lower one wins."
              control={
                <NumberInput
                  aria-label="Concurrent streams"
                  className="w-24"
                  min={0}
                  value={p.streamLimit}
                  onChange={(n) => patchPolicy({ streamLimit: Math.max(0, Math.round(n)) })}
                />
              }
            />
          </SettingsSection>

          <SettingsSection
            title="Allowed times"
            description="Hours when this viewer can start playback. Leave every hour clear to allow any time."
          >
            <SettingsRow
              title="Timezone"
              control={
                <Input
                  aria-label="Playback timezone"
                  className="w-56"
                  value={p.timezone}
                  onChange={(e) => patchPolicy({ timezone: e.target.value })}
                />
              }
            />
            <SettingsRow stacked title="Allowed hours">
              <ChipGroup
                ariaLabel="Allowed hours"
                size="sm"
                options={HOURS}
                value={p.allowedHours}
                onChange={(v) => patchPolicy({ allowedHours: [...v].sort((a, b) => a - b) })}
              />
            </SettingsRow>
          </SettingsSection>
        </>
      )}

      <ViewerLiveTVAccess viewerId={id} />

      <SaveBar
        dirty={limits.dirty || rules.dirty}
        saving={saving}
        error={error}
        onSave={save}
        onDiscard={() => {
          limits.reset();
          rules.reset();
          setError(null);
        }}
      />
    </div>
  );
}
