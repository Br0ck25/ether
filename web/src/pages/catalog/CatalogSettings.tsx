import { SettingsSection } from "@braedonsaunders/appkit-ui";
import { Database, Globe2, Sparkles } from "lucide-react";
import { useState } from "react";
import { useApp } from "../../app-context";
import { ConfigSection } from "../../components/ConfigSection";
import { TestButton, type TestState } from "../../components/fields";
import { SettingsRow } from "../../components/SettingsRow";
import { LevelBadge } from "../../components/status";
import { api } from "../../lib/api";
import { CATALOG } from "../settings/schema";

export function CatalogSettings() {
  const { health } = useApp();
  const item = health.find((h) => h.id === "catalog");
  const [test, setTest] = useState<TestState>({ status: "idle" });
  const runTest = (key: string) => {
    setTest({ status: "running" });
    api.testTMDB(key).then(
      (r) =>
        setTest(
          r.ok
            ? { status: "ok", durationMs: r.durationMs }
            : { status: "fail", message: r.error ?? "Test failed" },
        ),
      (e) => setTest({ status: "fail", message: (e as Error).message }),
    );
  };
  return (
    <div className="catalog-scroll app-scroll">
      <div className="catalog-settings">
        <div className="space-y-5">
          <div>
            <p className="catalog-eyebrow">CATALOG SETTINGS</p>
            <h2 className="text-2xl font-semibold tracking-tight">Shape your library</h2>
            <p className="mt-2 text-sm text-fg-muted">
              Connect TMDB and choose the titles and metadata your viewers discover.
            </p>
          </div>
          <ConfigSection
            configKey="catalog"
            endpoint="catalog"
            groups={CATALOG}
            label="Catalog"
            test={(draft) => (
              <TestButton
                label="Test TMDB key"
                state={test}
                onTest={() => runTest(String(draft.tmdbKey ?? ""))}
              />
            )}
          />
        </div>
        <aside className="space-y-5" aria-label="Catalog status">
          {item ? (
            <SettingsSection title="Catalog health">
              <SettingsRow
                title="TMDB catalog"
                description={item.details.join(" · ")}
                control={<LevelBadge level={item.level} label={item.summary} />}
              />
            </SettingsSection>
          ) : null}
          <div className="catalog-settings-note">
            <Globe2 size={21} />
            <h3>Metadata, your way</h3>
            <p>
              Language and region tailor title names, descriptions, release dates and availability.
            </p>
          </div>
          <div className="catalog-settings-note">
            <Sparkles size={21} />
            <h3>A focused library</h3>
            <p>
              Popularity and vote thresholds shape browsing. Other released titles remain
              discoverable through Plex search.
            </p>
          </div>
          <div className="catalog-settings-note">
            <Database size={21} />
            <h3>Always up to date</h3>
            <p>
              Settings apply to new requests without a restart. Unknown and future release dates
              stay out of browsing.
            </p>
          </div>
        </aside>
      </div>
    </div>
  );
}
