import { SettingsSection, Spinner, Switch } from "@braedonsaunders/appkit-ui";
import { ErrorPanel } from "../../components/feedback";
import { SaveBar } from "../../components/SaveBar";
import { SettingsRow } from "../../components/SettingsRow";
import { useDiscoverySettings } from "../viewers/sections/WatchlistDefaults";

/**
 * Features that use the owner's language model, edited in place under
 * Settings › Connected services › Language model. Saved through the
 * discovery settings API alongside watchlist alerts.
 */
export function LanguageFeatures() {
  const s = useDiscoverySettings();
  if (s.doc.error) return <ErrorPanel error={s.doc.error} onRetry={() => void s.doc.reload()} />;
  if (!s.draft)
    return (
      <div className="py-6 text-center">
        <Spinner size={18} label="Loading language features" />
      </div>
    );
  const draft = s.draft;
  return (
    <>
      <SettingsSection
        title="Language model features"
        description="Optional. They use the language model above; search falls back to ordinary catalogue search when it's unavailable."
      >
        <SettingsRow
          title={<label htmlFor="llm-natural-search">Natural-language search</label>}
          description="Viewers can search Plex for things like “90s heist movies”."
          control={
            <Switch
              id="llm-natural-search"
              aria-label="Natural-language search"
              checked={draft.naturalSearch}
              onChange={(e) => s.setDraft({ ...draft, naturalSearch: e.currentTarget.checked })}
            />
          }
        />
        <SettingsRow
          title={<label htmlFor="llm-recaps">Recaps</label>}
          description="“Previously on” summaries built only from episodes the viewer has finished. Those episode overviews are sent to the language model."
          control={
            <Switch
              id="llm-recaps"
              aria-label="Recaps"
              checked={draft.recaps}
              onChange={(e) => s.setDraft({ ...draft, recaps: e.currentTarget.checked })}
            />
          }
        />
      </SettingsSection>
      <SaveBar
        dirty={s.dirty}
        saving={s.saving}
        error={s.error}
        onSave={() => s.save("Language features saved")}
        onDiscard={s.discard}
      />
    </>
  );
}
