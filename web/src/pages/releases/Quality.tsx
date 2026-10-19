import {
  Badge,
  Button,
  EmptyState,
  SearchSelect,
  SettingsSection,
  confirmDialog,
} from "@braedonsaunders/appkit-ui";
import { Plus, SlidersHorizontal } from "lucide-react";
import { useMemo, useState } from "react";
import { ErrorPanel, LoadingRows } from "../../components/feedback";
import { ChipGroup, ListInput, NumberInput } from "../../components/fields";
import { Section } from "../../components/layout";
import { SaveBar, useDraft } from "../../components/SaveBar";
import { SettingsRow } from "../../components/SettingsRow";
import type { QualityProfile } from "../../lib/types";
import { QUALITY_AUDIO, QUALITY_CODECS, QUALITY_SOURCES } from "../settings/schema";
import {
  BoolRow,
  RecordDrawer,
  RecordItem,
  TextRow,
  recordErrors,
  type Errors,
} from "../sources/form";
import { useQuality, type Quality } from "./useQuality";

function blankProfile(n: number): QualityProfile {
  return {
    name: `Profile ${n}`,
    maxHeight: 1080,
    minHeight: 720,
    allowHdr: false,
    allowDolbyVision: false,
    preferredSources: ["webdl", "bluray"],
    preferredCodecs: ["h264", "hevc"],
    preferredAudio: ["eac3", "ac3", "aac"],
    minSizeMbPerMin: 5,
    maxSizeMbPerMin: 150,
    preferredLanguages: null,
    allowCam: false,
  };
}

const HEIGHTS = [
  { value: "0", label: "Any" },
  { value: "480", label: "480p" },
  { value: "720", label: "720p" },
  { value: "1080", label: "1080p" },
  { value: "2160", label: "4K (2160p)" },
];

const heightLabel = (h: number) => HEIGHTS.find((x) => x.value === String(h))?.label ?? `${h}p`;

export function profileSummary(p: QualityProfile) {
  const range =
    p.minHeight === p.maxHeight
      ? heightLabel(p.maxHeight)
      : `${heightLabel(p.minHeight)} to ${heightLabel(p.maxHeight)}`;
  return [
    range,
    p.allowDolbyVision ? "HDR and Dolby Vision" : p.allowHdr ? "HDR allowed" : "SDR only",
    p.preferredSources?.length ? `prefers ${p.preferredSources.join(", ")}` : "",
  ]
    .filter(Boolean)
    .join(" · ");
}

/** Ordered multi-pick: chips toggle membership; the order picked is the preference order. */
function PreferenceList({
  label,
  all,
  value,
  onChange,
}: {
  label: string;
  all: string[];
  value: string[] | null;
  onChange: (v: string[]) => void;
}) {
  const v = value ?? [];
  return (
    <div className="space-y-1.5">
      <ChipGroup
        ariaLabel={label}
        size="sm"
        value={v}
        onChange={(next) =>
          onChange(next.filter((x) => v.includes(x)).concat(next.filter((x) => !v.includes(x))))
        }
        options={[...all, ...v.filter((x) => !all.includes(x))].map((x) => ({
          value: x,
          label: x,
        }))}
      />
      <p className="text-xs text-fg-muted">
        {v.length ? `Preference: ${v.join(" › ")}` : "No preference"}
      </p>
    </div>
  );
}

/** Quality tab: the default profiles edited in place, and the profile list. */
export function QualityTab() {
  const { config, quality, commit } = useQuality();
  if (config.error) return <ErrorPanel error={config.error} onRetry={() => void config.reload()} />;
  if (!quality) return <LoadingRows rows={4} />;
  return (
    <>
      <QualityDefaults quality={quality} commit={commit} />
      <ProfileList quality={quality} commit={commit} />
    </>
  );
}

type Commit = (next: Quality, message: string) => Promise<void>;

function QualityDefaults({ quality, commit }: { quality: Quality; commit: Commit }) {
  const saved = useMemo(
    () => ({ default: quality.default, remote: quality.remote }),
    [quality.default, quality.remote],
  );
  const { draft, setDraft, dirty, reset } = useDraft(saved);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [errors, setErrors] = useState<Errors>({});
  const names = (quality.profiles ?? []).map((p) => ({ value: p.name, label: p.name }));
  if (!draft) return null;

  async function save() {
    if (!draft) return;
    setSaving(true);
    setError(null);
    setErrors({});
    try {
      await commit({ ...quality, ...draft }, "Default quality saved");
    } catch (e) {
      setError(e);
      setErrors(recordErrors(e, "quality."));
    } finally {
      setSaving(false);
    }
  }

  return (
    <>
      <SettingsSection
        title="Defaults"
        description="The profile a Plex app gets when no client rule matches it."
      >
        <SettingsRow
          title="On your network"
          description={
            errors.default ? (
              <span className="text-danger">{errors.default}</span>
            ) : (
              "Apps on the same network as this server."
            )
          }
          control={
            <div className="w-48">
              <SearchSelect
                value={draft.default}
                onChange={(v) => setDraft({ ...draft, default: v })}
                options={names}
                ariaLabel="Default profile"
                searchable={false}
              />
            </div>
          }
        />
        <SettingsRow
          title="Remote"
          description={
            errors.remote ? (
              <span className="text-danger">{errors.remote}</span>
            ) : (
              "Apps streaming from outside your network."
            )
          }
          control={
            <div className="w-48">
              <SearchSelect
                value={draft.remote}
                onChange={(v) => setDraft({ ...draft, remote: v })}
                options={names}
                ariaLabel="Remote profile"
                searchable={false}
              />
            </div>
          }
        />
      </SettingsSection>
      <SaveBar
        dirty={dirty}
        saving={saving}
        error={error}
        onSave={save}
        onDiscard={() => {
          reset();
          setError(null);
          setErrors({});
        }}
      />
    </>
  );
}

function ProfileList({ quality, commit }: { quality: Quality; commit: Commit }) {
  const profiles = quality.profiles ?? [];
  const rules = quality.rules ?? [];
  const [editing, setEditing] = useState<{ index: number; draft: QualityProfile } | null>(null);
  const [errors, setErrors] = useState<Errors>({});
  const [error, setError] = useState<unknown>(null);
  const [saving, setSaving] = useState(false);

  const open = (index: number, draft: QualityProfile) => {
    setErrors({});
    setError(null);
    setEditing({ index, draft: structuredClone(draft) });
  };

  async function save() {
    if (!editing) return;
    const { index, draft } = editing;
    const old = profiles[index];
    const list =
      index < profiles.length
        ? profiles.map((p, j) => (j === index ? draft : p))
        : [...profiles, draft];
    // Keep defaults and client rules pointing at a renamed profile.
    const renamed = old && old.name !== draft.name ? old.name : null;
    const next: Quality = renamed
      ? {
          ...quality,
          profiles: list,
          default: quality.default === renamed ? draft.name : quality.default,
          remote: quality.remote === renamed ? draft.name : quality.remote,
          rules: rules.map((r) => (r.profile === renamed ? { ...r, profile: draft.name } : r)),
        }
      : { ...quality, profiles: list };
    setSaving(true);
    setError(null);
    setErrors({});
    try {
      await commit(next, index < profiles.length ? "Profile saved" : "Profile added");
      setEditing(null);
    } catch (e) {
      setError(e);
      setErrors(recordErrors(e, `quality.profiles[${index}].`));
    } finally {
      setSaving(false);
    }
  }

  async function remove() {
    if (!editing) return;
    const { index, draft } = editing;
    const ok = await confirmDialog({
      title: "Remove profile?",
      message: `${draft.name || "This profile"} will be removed.`,
      confirmLabel: "Remove",
      tone: "danger",
    });
    if (!ok) return;
    setSaving(true);
    setError(null);
    try {
      await commit(
        { ...quality, profiles: profiles.filter((_, j) => j !== index) },
        "Profile removed",
      );
      setEditing(null);
    } catch (e) {
      setError(e);
    } finally {
      setSaving(false);
    }
  }

  const draft = editing?.draft;
  const isNew = editing ? editing.index >= profiles.length : false;
  const patch = (p: Partial<QualityProfile>) =>
    setEditing((e) => (e ? { ...e, draft: { ...e.draft, ...p } } : e));

  return (
    <>
      <Section
        title="Profiles"
        description="Resolution, HDR, source and language preferences used to rank releases."
        actions={
          <Button
            size="sm"
            onClick={() => open(profiles.length, blankProfile(profiles.length + 1))}
          >
            <Plus size={14} /> Add profile
          </Button>
        }
        bodyClassName=""
      >
        {profiles.length === 0 ? (
          <EmptyState
            icon={<SlidersHorizontal />}
            title="No profiles"
            description="Add a profile so Ether knows which releases to prefer."
          />
        ) : (
          <ul className="divide-y divide-border">
            {profiles.map((p, i) => {
              const usedBy = rules.filter((r) => r.profile === p.name).length;
              return (
                <RecordItem
                  key={`${p.name}-${i}`}
                  title={p.name || `Profile ${i + 1}`}
                  description={profileSummary(p)}
                  meta={
                    <>
                      {quality.default === p.name ? (
                        <Badge variant="info">On your network</Badge>
                      ) : null}
                      {quality.remote === p.name ? <Badge variant="info">Remote</Badge> : null}
                      {usedBy ? (
                        <Badge variant="secondary">
                          {usedBy === 1 ? "1 rule" : `${usedBy} rules`}
                        </Badge>
                      ) : null}
                    </>
                  }
                  editLabel={`Edit ${p.name || `profile ${i + 1}`}`}
                  onEdit={() => open(i, p)}
                />
              );
            })}
          </ul>
        )}
      </Section>

      <RecordDrawer
        open={editing !== null}
        onClose={() => !saving && setEditing(null)}
        title={isNew ? "New profile" : draft?.name || "Profile"}
        description="How releases are ranked for apps using this profile."
        onSave={() => void save()}
        saving={saving}
        saveLabel="Save profile"
        onRemove={isNew ? undefined : () => void remove()}
        removeLabel="Remove profile"
        removeDisabled={profiles.length <= 1}
        error={error}
      >
        {draft ? (
          <>
            <SettingsSection title="Picture">
              <TextRow
                label="Name"
                value={draft.name}
                error={errors.name}
                onChange={(v) => patch({ name: v })}
              />
              <SettingsRow
                title="Resolution"
                description={
                  errors.minHeight || errors.maxHeight ? (
                    <span className="text-danger">{errors.minHeight || errors.maxHeight}</span>
                  ) : (
                    "Lowest and highest acceptable."
                  )
                }
                control={
                  <span className="flex items-center gap-2">
                    <span className="w-32">
                      <SearchSelect
                        value={String(draft.minHeight)}
                        onChange={(v) => patch({ minHeight: Number(v) })}
                        options={HEIGHTS}
                        ariaLabel="Minimum resolution"
                        searchable={false}
                      />
                    </span>
                    <span className="text-sm text-fg-muted">to</span>
                    <span className="w-32">
                      <SearchSelect
                        value={String(draft.maxHeight)}
                        onChange={(v) => patch({ maxHeight: Number(v) })}
                        options={HEIGHTS}
                        ariaLabel="Maximum resolution"
                        searchable={false}
                      />
                    </span>
                  </span>
                }
              />
              <BoolRow
                label="Allow HDR"
                value={draft.allowHdr}
                help="Only used when the app supports it."
                onChange={(v) => patch({ allowHdr: v })}
              />
              <BoolRow
                label="Allow Dolby Vision"
                value={draft.allowDolbyVision}
                onChange={(v) => patch({ allowDolbyVision: v })}
              />
              <BoolRow
                label="Allow CAM releases"
                value={draft.allowCam}
                onChange={(v) => patch({ allowCam: v })}
              />
            </SettingsSection>
            <SettingsSection title="Preferences" description="Pick in order of preference.">
              <SettingsRow stacked title="Sources">
                <PreferenceList
                  label="Sources"
                  all={QUALITY_SOURCES}
                  value={draft.preferredSources}
                  onChange={(v) => patch({ preferredSources: v })}
                />
              </SettingsRow>
              <SettingsRow stacked title="Video codecs">
                <PreferenceList
                  label="Video codecs"
                  all={QUALITY_CODECS}
                  value={draft.preferredCodecs}
                  onChange={(v) => patch({ preferredCodecs: v })}
                />
              </SettingsRow>
              <SettingsRow stacked title="Audio">
                <PreferenceList
                  label="Audio"
                  all={QUALITY_AUDIO}
                  value={draft.preferredAudio}
                  onChange={(v) => patch({ preferredAudio: v })}
                />
              </SettingsRow>
              <SettingsRow
                stacked
                title="Languages"
                description="ISO codes in preference order. Empty accepts any."
              >
                <ListInput
                  ariaLabel="Languages"
                  value={draft.preferredLanguages}
                  onChange={(v) => patch({ preferredLanguages: v.length ? v.map(String) : null })}
                  placeholder="en, fr"
                />
              </SettingsRow>
            </SettingsSection>
            <SettingsSection title="Size">
              <SettingsRow
                title="Size per minute"
                description={
                  errors.minSizeMbPerMin || errors.maxSizeMbPerMin ? (
                    <span className="text-danger">
                      {errors.minSizeMbPerMin || errors.maxSizeMbPerMin}
                    </span>
                  ) : (
                    "MB per minute of runtime. Filters out fakes and bloated files."
                  )
                }
                control={
                  <span className="flex items-center gap-2">
                    <NumberInput
                      aria-label="Minimum MB per minute"
                      value={draft.minSizeMbPerMin}
                      min={0}
                      className="w-20"
                      onChange={(n) => patch({ minSizeMbPerMin: n })}
                    />
                    <span className="text-sm text-fg-muted">to</span>
                    <NumberInput
                      aria-label="Maximum MB per minute"
                      value={draft.maxSizeMbPerMin}
                      min={0}
                      className="w-20"
                      onChange={(n) => patch({ maxSizeMbPerMin: n })}
                    />
                    <span className="text-sm text-fg-muted">MB</span>
                  </span>
                }
              />
            </SettingsSection>
          </>
        ) : null}
      </RecordDrawer>
    </>
  );
}
